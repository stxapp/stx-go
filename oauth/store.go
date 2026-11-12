package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TokenStore keeps members' tokens, keyed by any stable string you use for a
// member (your user id, for example). Get returns (nil, nil) when it holds
// nothing for the key. Implement it over your own database and encrypt the
// tokens at rest; [MemoryTokenStore] and [FileTokenStore] suit one process.
//
// Implementations must be safe for concurrent use.
type TokenStore interface {
	Get(ctx context.Context, memberKey string) (*Token, error)
	Set(ctx context.Context, memberKey string, t *Token) error
	Delete(ctx context.Context, memberKey string) error
}

// Locker is an optional [TokenStore] method. Implement it when several
// processes share the store, so only one refreshes a member at a time: a
// second, concurrent refresh with the same rotating refresh token trips
// reuse detection and revokes the grant. Within one process the package
// already refreshes each member once at a time.
type Locker interface {
	WithLock(ctx context.Context, memberKey string, fn func(ctx context.Context) error) error
}

// MemoryTokenStore is an in-memory [TokenStore] for scripts, demos and
// tests. The zero value is ready to use.
type MemoryTokenStore struct {
	mu     sync.Mutex
	tokens map[string]Token
}

// Get returns a copy of the member's tokens, or nil.
func (s *MemoryTokenStore) Get(_ context.Context, key string) (*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[key]
	if !ok {
		return nil, nil
	}
	return &t, nil
}

// Set stores a copy of t.
func (s *MemoryTokenStore) Set(_ context.Context, key string, t *Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = map[string]Token{}
	}
	s.tokens[key] = *t
	return nil
}

// Delete forgets the member's tokens.
func (s *MemoryTokenStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, key)
	return nil
}

// FileTokenStore is a [TokenStore] kept in one JSON file, written with mode
// 0600 and replaced atomically. It suits a command-line tool or a single
// server process; the tokens are not encrypted.
type FileTokenStore struct {
	path string
	mu   sync.Mutex
}

// NewFileTokenStore returns a store backed by path. The file and its
// directory are created on the first Set.
func NewFileTokenStore(path string) *FileTokenStore { return &FileTokenStore{path: path} }

// Get returns the member's tokens, or nil.
func (s *FileTokenStore) Get(_ context.Context, key string) (*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return nil, err
	}
	t, ok := all[key]
	if !ok {
		return nil, nil
	}
	return &t, nil
}

// Set stores t for the member.
func (s *FileTokenStore) Set(_ context.Context, key string, t *Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return err
	}
	all[key] = *t
	return s.write(all)
}

// Delete forgets the member's tokens.
func (s *FileTokenStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := all[key]; !ok {
		return nil
	}
	delete(all, key)
	return s.write(all)
}

func (s *FileTokenStore) read() (map[string]Token, error) {
	all := map[string]Token{}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return all, nil
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	return all, nil
}

func (s *FileTokenStore) write(all map[string]Token) error {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".stx-tokens-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

// PendingAuthorization is one sign-in in progress, kept server-side under
// its state between the authorize redirect and the callback.
type PendingAuthorization struct {
	// CodeVerifier is the PKCE verifier the code exchange presents.
	CodeVerifier string `json:"code_verifier"`
	// RedirectURL was sent to the authorize endpoint; the exchange repeats it.
	RedirectURL string `json:"redirect_url"`
	// Scope is the scope asked for, space-delimited.
	Scope string `json:"scope,omitempty"`
	// Expires is when the entry stops being valid.
	Expires time.Time `json:"expires"`
	// Data is yours, carried to the callback: a user id, an app id, a return URL.
	Data map[string]string `json:"data,omitempty"`
}

// PendingStore keeps sign-ins in progress, keyed by state. Take returns and
// removes the entry in one step, so a replayed callback finds nothing; it
// returns (nil, nil) for an unknown or expired state. Implementations must
// be safe for concurrent use.
type PendingStore interface {
	Put(ctx context.Context, state string, p *PendingAuthorization) error
	Take(ctx context.Context, state string) (*PendingAuthorization, error)
}

// MemoryPendingStore is an in-memory [PendingStore] for one process. The
// zero value is ready to use. Expired entries are dropped.
type MemoryPendingStore struct {
	mu      sync.Mutex
	entries map[string]PendingAuthorization
}

// Put stores p under state.
func (s *MemoryPendingStore) Put(_ context.Context, state string, p *PendingAuthorization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]PendingAuthorization{}
	}
	now := time.Now()
	for k, v := range s.entries {
		if !now.Before(v.Expires) {
			delete(s.entries, k)
		}
	}
	s.entries[state] = *p
	return nil
}

// Take returns and removes the entry for state.
func (s *MemoryPendingStore) Take(_ context.Context, state string) (*PendingAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.entries[state]
	delete(s.entries, state)
	if !ok || !time.Now().Before(p.Expires) {
		return nil, nil
	}
	return &p, nil
}

// Len returns the number of sign-ins in progress.
func (s *MemoryPendingStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	now := time.Now()
	for _, v := range s.entries {
		if now.Before(v.Expires) {
			n++
		}
	}
	return n
}
