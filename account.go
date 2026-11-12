package stx

import (
	"context"
	"errors"
	"sync"
)

// AccountService covers identity, balance, positions, the public profile and
// terms acceptance. Use it through [Client.Account].
type AccountService struct {
	c *Client

	mu     sync.Mutex
	userID string
}

// Me returns who this key belongs to (GET /api/v1/me): the user id the
// account channels are keyed by, the account id, and the key's scope
// ("read_only" or "read_write").
func (s *AccountService) Me(ctx context.Context) (*Me, error) {
	me, err := doOne[*Me](ctx, s.c, "me_get", callArgs{})
	if err == nil && me != nil && me.UserID != "" {
		s.mu.Lock()
		s.userID = me.UserID
		s.mu.Unlock()
	}
	return me, err
}

// UserID returns the user id from [AccountService.Me], fetched once and
// cached. When Me is forbidden for this credential, the id comes from
// [AccountService.Balance], which carries the same user id.
func (s *AccountService) UserID(ctx context.Context) (string, error) {
	s.mu.Lock()
	id := s.userID
	s.mu.Unlock()
	if id != "" {
		return id, nil
	}
	me, err := s.Me(ctx)
	if err == nil {
		if me == nil || me.UserID == "" {
			return "", errors.New("stx: GET /api/v1/me returned no user_id")
		}
		return me.UserID, nil
	}
	if !errors.Is(err, ErrForbidden) {
		return "", err
	}
	bal, berr := s.Balance(ctx)
	if berr != nil || bal == nil || bal.UserID == "" {
		return "", err
	}
	s.mu.Lock()
	s.userID = bal.UserID
	s.mu.Unlock()
	return bal.UserID, nil
}

// Balance returns the balance, liabilities and fee schedule (GET
// /api/v1/account/balance), the same object the balances channel pushes.
func (s *AccountService) Balance(ctx context.Context) (*Balance, error) {
	return doOne[*Balance](ctx, s.c, "account_balance_get", callArgs{})
}

// PositionsQuery filters [AccountService.Positions].
type PositionsQuery struct {
	MarketIDs []string
}

// Positions returns your open positions (GET /api/v1/positions), the same
// objects the positions channel sends on join. Position is positive when
// long, negative when short.
func (s *AccountService) Positions(ctx context.Context, q *PositionsQuery) ([]Position, error) {
	var p params
	if q != nil {
		p.list("market_ids", q.MarketIDs)
	}
	return doOne[[]Position](ctx, s.c, "positions_get", callArgs{query: p})
}

// ProfileAvatar is a generated avatar. Any style and palette pair renders;
// the seed selects the pattern.
type ProfileAvatar struct {
	// Style is one of "dots", "rings", "stripes", "grid".
	Style string `json:"style"`
	// Palette is one of "ember", "forest", "ocean", "grape", "slate", "mint",
	// "rose", "gold".
	Palette string `json:"palette"`
	// Seed is 1 to 32 characters; the same seed always renders the same image.
	Seed string `json:"seed"`
}

// ProfileUpdate holds the public-profile fields to change. Leave a field
// unset to keep it; at least one must be set.
type ProfileUpdate struct {
	// Handle is the new public handle: 3 to 24 lowercase letters, digits and
	// inner "." or "_". It may change once every 30 days.
	Handle string `json:"handle,omitempty"`
	// Avatar is a new avatar.
	Avatar *ProfileAvatar `json:"avatar,omitempty"`
	// LeaderboardOptIn joins (true) or leaves (false) the public boards.
	LeaderboardOptIn *bool `json:"leaderboard_opt_in,omitempty"`
}

// UpdateProfile changes your public handle, avatar or leaderboard opt-in
// (PATCH /api/v1/me/profile) and returns the updated identity. Where the
// exchange has no leaderboard it returns an error matching
// [ErrNotAvailable].
func (s *AccountService) UpdateProfile(ctx context.Context, update ProfileUpdate) (*Me, error) {
	if update.Handle == "" && update.Avatar == nil && update.LeaderboardOptIn == nil {
		return nil, errors.New("stx: UpdateProfile needs at least one field")
	}
	return doOne[*Me](ctx, s.c, "me_profile_patch", callArgs{body: update})
}

// AcceptTermsOptions sets which consents [AccountService.AcceptTerms]
// records. Nil fields default to true for terms and privacy, and are left
// out for house rules (the exchange then derives it).
type AcceptTermsOptions struct {
	AcceptTerms      *bool
	AcceptPrivacy    *bool
	AcceptHouseRules *bool
}

// AcceptTerms accepts the current terms for your own account (POST
// /api/v1/tnc/accept) and returns the server's message. An app cannot
// accept terms for a member: with a member's OAuth token the exchange
// answers 403, and the member accepts on an STX page instead.
func (s *AccountService) AcceptTerms(ctx context.Context, deviceID string, opts *AcceptTermsOptions) (string, error) {
	if opts == nil {
		opts = &AcceptTermsOptions{}
	}
	body := struct {
		DeviceID         string `json:"device_id"`
		AcceptTerms      bool   `json:"accept_terms"`
		AcceptPrivacy    bool   `json:"accept_privacy"`
		AcceptHouseRules *bool  `json:"accept_house_rules,omitempty"`
	}{deviceID, true, true, opts.AcceptHouseRules}
	if opts.AcceptTerms != nil {
		body.AcceptTerms = *opts.AcceptTerms
	}
	if opts.AcceptPrivacy != nil {
		body.AcceptPrivacy = *opts.AcceptPrivacy
	}
	return doOne[string](ctx, s.c, "tnc_accept_post", callArgs{body: body})
}
