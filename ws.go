package stx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const phoenixTopic = "phoenix"

// Defaults for a [Stream].
const (
	defaultHeartbeat   = 25 * time.Second
	defaultChannelPing = 30 * time.Second
	defaultJoinTimeout = 10 * time.Second
	defaultBufferSize  = 10000
	dialTimeout        = 15 * time.Second
	leaveTimeout       = 2 * time.Second
	readLimit          = 128 << 20
)

// Sentinel errors for streams.
var (
	// ErrStreamClosed: the stream was closed, or gave up reconnecting.
	ErrStreamClosed = errors.New("stx: stream closed")
	// ErrAlreadySubscribed: the stream already holds a subscription to this
	// topic. Close it first, or read from the one you have.
	ErrAlreadySubscribed = errors.New("stx: already subscribed")
	// ErrNotJoined: a channel command was sent while the topic is not joined,
	// for example during a reconnect.
	ErrNotJoined = errors.New("stx: channel not joined")
	// ErrChannelRefused: the exchange refused a join or a channel command;
	// see *ChannelError for its reason.
	ErrChannelRefused = errors.New("stx: channel refused")
	// ErrTimeout: no reply to a join or a channel command in time. The error
	// also matches context.DeadlineExceeded.
	ErrTimeout = errors.New("stx: no reply in time")
)

// ChannelError is the exchange refusing a join or a channel command, with the
// reason it gave, such as "market_ids_required" or "unauthorized".
type ChannelError struct {
	// Topic is the wire topic, for example "orders:7fb55544-3a7e-4f6b-bd66-fc89bd3a1087".
	Topic string
	// Event is what was sent: "phx_join", "select_market_ids", ...
	Event string
	// Reason is the reply's "reason", or the server event ("phx_close",
	// "phx_error") that ended a join before it was answered.
	Reason string
	// Reply is the reply's raw "response" object.
	Reply json.RawMessage

	interrupted bool
}

// Error returns "stx: EVENT on TOPIC refused: REASON".
func (e *ChannelError) Error() string {
	if e.interrupted {
		return fmt.Sprintf("stx: %s on %s while waiting for the reply to %s", e.Reason, e.Topic, e.Event)
	}
	reason := e.Reason
	if reason == "" {
		reason = string(e.Reply)
	}
	return fmt.Sprintf("stx: %s on %s refused: %s", e.Event, e.Topic, reason)
}

// Is matches [ErrChannelRefused] for a refusal, and [ErrUnauthorized] and
// [ErrInsufficientScope] for those reasons.
func (e *ChannelError) Is(target error) bool {
	switch target {
	case ErrChannelRefused:
		return !e.interrupted
	case ErrUnauthorized:
		return e.Reason == "unauthorized"
	case ErrInsufficientScope:
		return e.Reason == "insufficient_scope"
	}
	return false
}

// HandshakeError is a WebSocket handshake the exchange refused with an HTTP
// status, for example 403 for a bad signature or a missing User-Agent.
type HandshakeError struct {
	// StatusCode is the HTTP status, for example 403.
	StatusCode int
	// Reason is the exchange's x-stx-error header, such as
	// "5300 - OAuth token auth failed", when it sent one.
	Reason string
}

// Error returns "stx: websocket handshake refused: HTTP STATUS (REASON)".
func (e *HandshakeError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("stx: websocket handshake refused: HTTP %d (%s)", e.StatusCode, e.Reason)
	}
	return fmt.Sprintf("stx: websocket handshake refused: HTTP %d", e.StatusCode)
}

// Is matches [ErrUnauthorized] on 401 and [ErrForbidden] on 403.
func (e *HandshakeError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.StatusCode == 401
	case ErrForbidden:
		return e.StatusCode == 403
	}
	return false
}

// ReconnectPolicy is the backoff between reconnect attempts: the wait doubles
// from InitialBackoff up to MaxBackoff, each wait drawn between 50% and 100%
// of its value unless NoJitter is set.
type ReconnectPolicy struct {
	// InitialBackoff is the wait before the first attempt. Default 500ms.
	InitialBackoff time.Duration
	// MaxBackoff caps the wait. Default 30s.
	MaxBackoff time.Duration
	// MaxAttempts is how many attempts to make before giving up; 0 never
	// gives up.
	MaxAttempts int
	// NoJitter uses the exact doubling waits.
	NoJitter bool
}

func (p ReconnectPolicy) delay(attempt int) time.Duration {
	initial, max := p.InitialBackoff, p.MaxBackoff
	if initial <= 0 {
		initial = 500 * time.Millisecond
	}
	if max <= 0 {
		max = 30 * time.Second
	}
	d := initial
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	if !p.NoJitter {
		d = time.Duration(float64(d) * (0.5 + rand.Float64()*0.5))
	}
	return d
}

// StreamOption configures a [Stream].
type StreamOption func(*streamConfig)

type streamConfig struct {
	heartbeat   time.Duration
	channelPing time.Duration
	reconnect   bool
	policy      ReconnectPolicy
	onReconnect func()
	joinTimeout time.Duration
	buffer      int
	logger      *slog.Logger
	userID      string
}

// WithHeartbeatInterval sets the time between socket heartbeats. The
// exchange closes a socket silent for 60 seconds. Default 25s.
func WithHeartbeatInterval(d time.Duration) StreamOption {
	return func(c *streamConfig) { c.heartbeat = d }
}

// WithChannelPingInterval sets the time between channel pings on every joined
// topic that answers them; 0 turns them off. The orders channel with
// cancel-on-disconnect always pings at a third of the timeout the exchange
// granted, whatever this is. Default 30s.
func WithChannelPingInterval(d time.Duration) StreamOption {
	return func(c *streamConfig) { c.channelPing = d }
}

// WithReconnectPolicy sets the backoff between reconnect attempts.
func WithReconnectPolicy(p ReconnectPolicy) StreamOption {
	return func(c *streamConfig) { c.policy = p }
}

// WithoutReconnect ends the stream, and every subscription on it, when the
// socket drops, instead of reconnecting.
func WithoutReconnect() StreamOption {
	return func(c *streamConfig) { c.reconnect = false }
}

// WithOnReconnect sets a function called after every reconnect and rejoin,
// and after a topic the exchange closed is rejoined while the socket stayed
// up. It is the moment to reload over REST what you cannot rebuild from the
// snapshots the account channels send again on rejoin, such as history.
// It runs on its own goroutine.
func WithOnReconnect(fn func()) StreamOption {
	return func(c *streamConfig) { c.onReconnect = fn }
}

// WithJoinTimeout sets how long to wait for the reply to a join or a channel
// command. Default 10s.
func WithJoinTimeout(d time.Duration) StreamOption {
	return func(c *streamConfig) { c.joinTimeout = d }
}

// WithBufferSize sets how many messages each subscription buffers for you.
// When you fall behind and the buffer is full, the oldest message is dropped
// and a warning is logged. Default 10000.
func WithBufferSize(n int) StreamOption {
	return func(c *streamConfig) { c.buffer = n }
}

// WithStreamLogger sets where the stream logs reconnects, rejoins and
// dropped messages. Default: the client's logger.
func WithStreamLogger(l *slog.Logger) StreamOption {
	return func(c *streamConfig) { c.logger = l }
}

// WithUserID gives the stream your user id, which the account topics are
// named by, so it does not ask GET /api/v1/me for it.
func WithUserID(id string) StreamOption {
	return func(c *streamConfig) { c.userID = id }
}

// streamBackend is what a Stream needs from the client.
type streamBackend struct {
	url        string
	host       string
	signed     bool
	httpClient *http.Client
	logger     *slog.Logger
	// headers returns the handshake headers, signed afresh.
	headers func(ctx context.Context, reconnect bool) (http.Header, error)
	// recoverAuth lets the credentials recover once from a refused handshake
	// (refresh a token); it reports whether trying again can help.
	recoverAuth func(ctx context.Context) bool
	userID      func(ctx context.Context) (string, error)
}

// Stream is one WebSocket connection to the exchange, carrying every topic
// you subscribe to. Get one with [Client.Stream].
//
// The stream signs the handshake afresh on every connect, sends the socket
// heartbeat, pings each joined topic (at a third of the granted timeout on
// orders with cancel-on-disconnect, so two pings can be lost before it
// fires), reconnects with backoff after a drop and rejoins every topic with
// its current filters, and rejoins a topic the exchange closes while the
// socket stays up. The account channels send their snapshots again on every
// rejoin.
//
// A Stream is safe for concurrent use. Close it when you are done.
type Stream struct {
	b   streamBackend
	cfg streamConfig
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // every goroutine the stream starts, but hooks
	hooks  sync.WaitGroup

	ref            atomic.Uint64
	reconnects     atomic.Int64
	channelRejoins atomic.Int64

	mu      sync.Mutex
	conn    *websocket.Conn
	gen     int
	up      chan struct{} // closed while a connection is installed
	pending map[string]*pending
	subs    map[string]*Subscription
	hbRef   string
	userID  string
	closing bool // Close is leaving the topics
	closed  bool
	err     error

	shutdownOnce sync.Once
	done         chan struct{}
}

type reply struct {
	status   string
	response json.RawMessage
	err      error
}

type pending struct {
	topic string
	event string
	join  *Subscription
	ch    chan reply
}

func newStream(b streamBackend, opts []StreamOption) *Stream {
	cfg := streamConfig{
		heartbeat:   defaultHeartbeat,
		channelPing: defaultChannelPing,
		reconnect:   true,
		joinTimeout: defaultJoinTimeout,
		buffer:      defaultBufferSize,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.heartbeat <= 0 {
		cfg.heartbeat = defaultHeartbeat
	}
	if cfg.channelPing < 0 {
		cfg.channelPing = 0
	}
	if cfg.joinTimeout <= 0 {
		cfg.joinTimeout = defaultJoinTimeout
	}
	if cfg.buffer <= 0 {
		cfg.buffer = defaultBufferSize
	}
	log := cfg.logger
	if log == nil {
		log = b.logger
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if b.httpClient == nil {
		b.httpClient = http.DefaultClient
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Stream{
		b:       b,
		cfg:     cfg,
		log:     log.With("component", "stx.stream"),
		ctx:     ctx,
		cancel:  cancel,
		up:      make(chan struct{}),
		pending: map[string]*pending{},
		subs:    map[string]*Subscription{},
		userID:  cfg.userID,
		done:    make(chan struct{}),
	}
}

// start dials the first connection and starts the stream's goroutines.
func (s *Stream) start(ctx context.Context) error {
	conn, err := s.dial(ctx, false)
	if err != nil {
		s.cancel()
		return err
	}
	gen, ok := s.install(conn)
	if !ok {
		_ = conn.CloseNow()
		return ErrStreamClosed
	}
	s.wg.Add(1)
	go s.run(conn, gen)
	return nil
}

// Done is closed when the stream ends: after Close, or when it gives up
// reconnecting.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Err returns why the stream ended on its own (credentials that cannot
// recover, reconnect attempts used up), or nil while it runs and after Close.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Connected reports whether the socket is up right now.
func (s *Stream) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

// Reconnects returns how many times the socket has reconnected.
func (s *Stream) Reconnects() int { return int(s.reconnects.Load()) }

// ChannelRejoins returns how many topics were rejoined after the exchange
// closed them while the socket stayed up.
func (s *Stream) ChannelRejoins() int { return int(s.channelRejoins.Load()) }

// Close leaves every topic, closes the socket and ends every subscription.
// It waits for the stream's goroutines to finish. Calling it again does
// nothing.
func (s *Stream) Close() error {
	s.mu.Lock()
	already := s.closed || s.closing
	s.closing = true
	subs := make([]*Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	if !already {
		ctx, cancel := context.WithTimeout(context.Background(), leaveTimeout)
		var leaves sync.WaitGroup
		for _, sub := range subs {
			leaves.Add(1)
			go func() {
				defer leaves.Done()
				sub.sendLeave(ctx)
			}()
		}
		leaves.Wait()
		cancel()
	}
	s.shutdown(nil)
	s.wg.Wait()
	return nil
}

// shutdown ends the stream: every subscription, the socket and the
// goroutines' context. err is why, or nil after Close.
func (s *Stream) shutdown(err error) {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.err = err
		subs := make([]*Subscription, 0, len(s.subs))
		for _, sub := range s.subs {
			subs = append(subs, sub)
		}
		s.subs = map[string]*Subscription{}
		conn := s.conn
		s.conn = nil
		s.mu.Unlock()
		end := err
		if end == nil {
			end = ErrStreamClosed
		}
		for _, sub := range subs {
			sub.end(end)
		}
		s.cancel()
		if conn != nil {
			_ = conn.CloseNow()
		}
		s.failPending("", ErrStreamClosed)
		close(s.done)
	})
}

func (s *Stream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Stream) goTracked(fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

func (s *Stream) raiseReconnect() {
	if s.cfg.onReconnect == nil {
		return
	}
	s.hooks.Add(1)
	go func() {
		defer s.hooks.Done()
		s.cfg.onReconnect()
	}()
}

// ---------------------------------------------------------------------------
// Connection
// ---------------------------------------------------------------------------

func (s *Stream) dial(ctx context.Context, reconnect bool) (*websocket.Conn, error) {
	conn, err := s.dialOnce(ctx, reconnect)
	var hs *HandshakeError
	if err != nil && errors.As(err, &hs) && (hs.StatusCode == 401 || hs.StatusCode == 403) &&
		s.b.recoverAuth != nil && s.b.recoverAuth(ctx) {
		conn, err = s.dialOnce(ctx, true)
	}
	return conn, err
}

func (s *Stream) dialOnce(ctx context.Context, reconnect bool) (*websocket.Conn, error) {
	header, err := s.b.headers(ctx, reconnect)
	if err != nil {
		return nil, err
	}
	hc := *s.b.httpClient
	hc.Timeout = 0 // the dial context bounds the handshake
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, resp, err := websocket.Dial(dctx, s.b.url, &websocket.DialOptions{HTTPHeader: header, HTTPClient: &hc})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != 0 {
			return nil, &HandshakeError{StatusCode: resp.StatusCode, Reason: resp.Header.Get("x-stx-error")}
		}
		return nil, &TransportError{Method: http.MethodGet, Path: socketPath, Err: err}
	}
	conn.SetReadLimit(readLimit)
	return conn, nil
}

// run owns the connection: it serves one, and after a drop reconnects and
// serves the next, until the stream ends.
func (s *Stream) run(conn *websocket.Conn, gen int) {
	defer s.wg.Done()
	for {
		s.serve(conn, gen)
		s.dropped(gen)
		if s.isClosed() {
			return
		}
		if !s.cfg.reconnect {
			s.log.Warn("socket dropped; reconnect is off")
			s.shutdown(fmt.Errorf("%w: socket dropped", ErrTransport))
			return
		}
		conn = s.redial()
		if conn == nil {
			return
		}
		var ok bool
		if gen, ok = s.install(conn); !ok {
			_ = conn.CloseNow()
			return
		}
		g := gen
		s.goTracked(func() { s.rejoinAll(g) })
	}
}

func (s *Stream) install(conn *websocket.Conn) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, false
	}
	s.conn = conn
	s.gen++
	s.hbRef = ""
	close(s.up)
	return s.gen, true
}

// dropped forgets a connection that ended: replies it owed fail, and every
// topic waits to be joined again.
func (s *Stream) dropped(gen int) {
	s.mu.Lock()
	if s.gen == gen && s.conn != nil {
		s.conn = nil
		s.up = make(chan struct{})
	}
	for _, sub := range s.subs {
		sub.resetLocked()
	}
	s.mu.Unlock()
	s.failPending("", fmt.Errorf("%w: socket dropped", ErrTransport))
}

// redial reconnects with backoff. It returns nil when the stream ended.
func (s *Stream) redial() *websocket.Conn {
	var last error
	for attempt := 1; ; attempt++ {
		if max := s.cfg.policy.MaxAttempts; max > 0 && attempt > max {
			s.log.Error("giving up reconnecting", "attempts", attempt-1, "err", last)
			s.shutdown(fmt.Errorf("stx: gave up reconnecting after %d attempts: %w", attempt-1, last))
			return nil
		}
		if !sleepCtx(s.ctx, s.cfg.policy.delay(attempt)) {
			return nil
		}
		conn, err := s.dial(s.ctx, true)
		if err == nil {
			return conn
		}
		if s.ctx.Err() != nil {
			return nil
		}
		// Credentials that cannot come back end the stream rather than
		// hammering the exchange forever.
		if errors.Is(err, ErrConfig) || errors.Is(err, ErrInvalidToken) {
			s.log.Error("giving up reconnecting", "err", err)
			s.shutdown(err)
			return nil
		}
		s.log.Warn("reconnect attempt failed", "attempt", attempt, "err", err)
		last = err
	}
}

// rejoinAll joins every topic on a new connection.
func (s *Stream) rejoinAll(gen int) {
	s.mu.Lock()
	subs := make([]*Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	for _, sub := range subs {
		if _, err := sub.join(s.ctx); err != nil {
			var ce *ChannelError
			if errors.As(err, &ce) {
				// Refused: the socket is fine, so retry this topic on its own.
				s.log.Warn("rejoin refused; retrying", "topic", sub.topic, "err", err)
				s.goTracked(func() { s.rejoinLater(sub, false) })
				continue
			}
			s.log.Info("rejoin failed", "topic", sub.topic, "err", err)
			if s.currentGen() != gen {
				return // dropped again; the next connection rejoins
			}
		}
	}
	if s.isClosed() || s.currentGen() != gen {
		return
	}
	s.reconnects.Add(1)
	s.log.Info("reconnected", "reconnects", s.reconnects.Load())
	s.raiseReconnect()
}

// rejoinLater rejoins one topic with backoff. serverClosed: the exchange
// closed it while the socket stayed up; otherwise a rejoin refused after a
// reconnect is being retried.
func (s *Stream) rejoinLater(sub *Subscription, serverClosed bool) {
	for attempt := 1; ; attempt++ {
		select {
		case <-sub.done:
			return
		default:
		}
		if !sleepCtx(s.ctx, s.cfg.policy.delay(attempt)) {
			return
		}
		if !s.Connected() {
			return // the reconnect rejoins it
		}
		did, err := sub.join(s.ctx)
		if err == nil {
			if did && serverClosed {
				s.channelRejoins.Add(1)
				s.raiseReconnect()
			}
			return
		}
		s.log.Warn("rejoin failed", "topic", sub.topic, "err", err)
		var ce *ChannelError
		if errors.As(err, &ce) && !ce.interrupted && sub.terminalRefusal(ce.Reason) {
			sub.end(sub.joinError(err))
			return
		}
		if errors.Is(err, errSubscriptionEnded) {
			return
		}
	}
}

func (s *Stream) currentGen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// waitConnected waits for a connection, during a reconnect's backoff.
func (s *Stream) waitConnected(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return ErrStreamClosed
		}
		if s.conn != nil {
			s.mu.Unlock()
			return nil
		}
		up := s.up
		s.mu.Unlock()
		select {
		case <-up:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return ErrStreamClosed
		}
	}
}

// serve reads frames until the connection ends, and heartbeats meanwhile.
func (s *Stream) serve(conn *websocket.Conn, gen int) {
	hbCtx, stopHB := context.WithCancel(s.ctx)
	var hb sync.WaitGroup
	hb.Add(1)
	go func() {
		defer hb.Done()
		s.heartbeat(hbCtx, conn)
	}()
	defer func() {
		stopHB()
		hb.Wait()
		_ = conn.CloseNow()
	}()
	for {
		typ, data, err := conn.Read(s.ctx)
		if err != nil {
			if s.ctx.Err() == nil {
				s.log.Info("socket closed", "err", err)
			}
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		s.dispatch(gen, data)
	}
}

func (s *Stream) heartbeat(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(s.cfg.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		if s.hbRef != "" {
			s.mu.Unlock()
			s.log.Warn("heartbeat unanswered; reconnecting")
			_ = conn.CloseNow()
			return
		}
		ref := s.nextRef()
		s.hbRef = ref
		s.mu.Unlock()
		if err := s.write(ctx, conn, nil, ref, phoenixTopic, "heartbeat", struct{}{}); err != nil {
			_ = conn.CloseNow()
			return
		}
	}
}

func (s *Stream) nextRef() string {
	return strconv.FormatUint(s.ref.Add(1), 10)
}

func (s *Stream) write(ctx context.Context, conn *websocket.Conn, joinRef *string, ref, topic, event string, payload any) error {
	frame, err := json.Marshal([]any{joinRef, ref, topic, event, payload})
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, s.cfg.joinTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, frame)
}

// request sends a frame and waits for its reply's response. A reply with a
// status other than "ok" is a *ChannelError.
func (s *Stream) request(ctx context.Context, topic, event string, payload any, joinRef, ref string, timeout time.Duration, join *Subscription) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = s.nextRef()
	}
	if timeout <= 0 {
		timeout = s.cfg.joinTimeout
	}
	p := &pending{topic: topic, event: event, join: join, ch: make(chan reply, 1)}
	s.mu.Lock()
	conn := s.conn
	if s.closed || conn == nil {
		s.mu.Unlock()
		if s.closed {
			return nil, ErrStreamClosed
		}
		return nil, fmt.Errorf("%w: socket is not connected", ErrTransport)
	}
	s.pending[ref] = p
	s.mu.Unlock()
	forget := func() {
		s.mu.Lock()
		delete(s.pending, ref)
		s.mu.Unlock()
	}
	var jr *string
	if joinRef != "" {
		jr = &joinRef
	}
	// The write is bounded by the stream, not by ctx: the websocket library
	// closes the whole connection when a write's context ends mid-frame, which
	// would drop every topic (and fire cancel-on-disconnect) over one caller.
	if err := s.write(s.ctx, conn, jr, ref, topic, event, payload); err != nil {
		forget()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &TransportError{Method: event, Path: topic, Err: err}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-p.ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.status != "ok" {
			var body struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(r.response, &body)
			return nil, &ChannelError{Topic: topic, Event: event, Reason: body.Reason, Reply: r.response}
		}
		return r.response, nil
	case <-timer.C:
		forget()
		return nil, fmt.Errorf("%w: %s on %s within %s: %w", ErrTimeout, event, topic, timeout, context.DeadlineExceeded)
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrStreamClosed
	}
}

// failPending fails every reply still owed, or those for one topic.
func (s *Stream) failPending(topic string, err error) {
	s.mu.Lock()
	var fail []*pending
	for ref, p := range s.pending {
		if topic == "" || p.topic == topic {
			fail = append(fail, p)
			delete(s.pending, ref)
		}
	}
	s.mu.Unlock()
	for _, p := range fail {
		p.ch <- reply{err: err}
	}
}

// ---------------------------------------------------------------------------
// Inbound frames
// ---------------------------------------------------------------------------

func refString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func (s *Stream) dispatch(gen int, data []byte) {
	var frame []json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil || len(frame) != 5 {
		s.log.Warn("unexpected frame", "frame", truncate(string(data), 200))
		return
	}
	joinRef, ref := refString(frame[0]), refString(frame[1])
	var topic, event string
	if json.Unmarshal(frame[2], &topic) != nil || json.Unmarshal(frame[3], &event) != nil {
		s.log.Warn("unexpected frame", "frame", truncate(string(data), 200))
		return
	}
	payload := frame[4]

	if event == "phx_reply" {
		s.onReply(gen, topic, ref, payload)
		return
	}

	s.mu.Lock()
	sub := s.subs[topic]
	if sub == nil {
		s.mu.Unlock()
		return
	}
	if event == "phx_close" || event == "phx_error" {
		s.onServerClose(sub, event, joinRef) // unlocks
		return
	}
	current := sub.joinRef
	s.mu.Unlock()
	if joinRef != "" && current != "" && joinRef != current {
		return // a frame for an earlier join of this topic
	}
	sub.deliver(newMessage(topic, event, payload, s.log))
}

func (s *Stream) onReply(gen int, topic, ref string, payload json.RawMessage) {
	var body struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	_ = json.Unmarshal(payload, &body)
	s.mu.Lock()
	if topic == phoenixTopic && ref == s.hbRef {
		s.hbRef = ""
	}
	p := s.pending[ref]
	if p == nil {
		s.mu.Unlock()
		return
	}
	delete(s.pending, ref)
	if p.join != nil && body.Status == "ok" && p.join.joinRef == ref && !p.join.closing {
		// Joined from here, in frame order, so a close that follows the reply
		// finds the topic joined.
		sub := p.join
		sub.joined = true
		sub.joinedGen = gen
		sub.reply = convertReply(sub.name, body.Response)
	}
	s.mu.Unlock()
	p.ch <- reply{status: body.Status, response: body.Response}
}

// onServerClose handles phx_close or phx_error for a topic. s.mu is held
// and is released here.
func (s *Stream) onServerClose(sub *Subscription, event, joinRef string) {
	// Only a close of the current join counts; one for an earlier join is stale.
	if joinRef == "" || joinRef != sub.joinRef || sub.closing || s.closed {
		s.mu.Unlock()
		return
	}
	interrupted := &ChannelError{Topic: sub.topic, Event: "phx_join", Reason: event, interrupted: true}
	if !sub.joined {
		// The join in flight was ended: it fails now and its caller retries.
		// A closed market ends here.
		p := s.pending[joinRef]
		if p != nil {
			delete(s.pending, joinRef)
		}
		finish := event == "phx_close" && sub.name == "market"
		s.mu.Unlock()
		if p != nil {
			p.ch <- reply{err: interrupted}
		}
		if finish {
			s.finishMarket(sub)
		}
		return
	}
	sub.resetLocked()
	finish := event == "phx_close" && sub.name == "market"
	s.mu.Unlock()
	// Requests on the ended join get no reply: fail them now.
	s.failPending(sub.topic, &ChannelError{Topic: sub.topic, Event: event, Reason: event, interrupted: true})
	if finish {
		s.finishMarket(sub)
		return
	}
	// The exchange ended the topic (its cancel-on-disconnect timer fired, for
	// example): rejoin it.
	s.log.Warn("topic closed by the exchange; rejoining", "topic", sub.topic, "event", event)
	s.goTracked(func() { s.rejoinLater(sub, true) })
}

// finishMarket ends a market:<id> subscription the exchange closed: a
// resulted or voided market sends its last update, then the topic closes.
func (s *Stream) finishMarket(sub *Subscription) {
	s.mu.Lock()
	sub.closing = true
	if s.subs[sub.topic] == sub {
		delete(s.subs, sub.topic)
	}
	s.mu.Unlock()
	sub.end(nil)
}

// resolveUserID returns the user id the account topics are named by.
func (s *Stream) resolveUserID(ctx context.Context) (string, error) {
	s.mu.Lock()
	id := s.userID
	s.mu.Unlock()
	if id != "" {
		return id, nil
	}
	if s.b.userID == nil {
		return "", configError("the stream needs your user id; pass WithUserID")
	}
	id, err := s.b.userID(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.userID = id
	s.mu.Unlock()
	return id, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Stream opens a WebSocket stream to the exchange: one connection that
// carries every topic you subscribe to. The handshake is signed with the
// client's credentials when they can sign it; without credentials the
// stream carries the market data channels only. A refused handshake returns
// a *[HandshakeError].
//
// ctx bounds the first connect only; the stream runs until you Close it.
func (c *Client) Stream(ctx context.Context, opts ...StreamOption) (*Stream, error) {
	s := newStream(c.streamBackend(), opts)
	if err := s.start(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (c *Client) streamBackend() streamBackend {
	wsAuth, _ := c.auth.(WebSocketAuth)
	ua := c.userAgent
	b := streamBackend{
		url:        c.socketURL,
		host:       c.host,
		signed:     wsAuth != nil,
		httpClient: c.httpClient,
		logger:     c.logger,
		userID:     c.Account.UserID,
		headers: func(ctx context.Context, reconnect bool) (http.Header, error) {
			h := http.Header{}
			if wsAuth != nil {
				signed, err := wsAuth.WebSocketHeaders(ctx, reconnect)
				if err != nil {
					return nil, err
				}
				for k, v := range signed {
					h[k] = v
				}
			}
			// The exchange refuses a handshake without a User-Agent.
			h.Set("User-Agent", ua)
			return h, nil
		},
	}
	if re, ok := c.auth.(Reauthenticator); ok {
		b.recoverAuth = func(ctx context.Context) bool {
			ok, err := re.Reauthenticate(ctx)
			return err == nil && ok
		}
	}
	return b
}
