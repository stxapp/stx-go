package stx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Combos are combinations of markets negotiated over two topics. A player
// asks for quotes on a set of legs and a stake ([ComboPlayer]); makers see
// every request and quote an amount ([ComboMaker]). When the player accepts,
// the exchange places one ordinary limit order for each side on the
// combination's market; from then on its orders, fills, positions and
// settlements arrive on the account channels. The protocol, with every
// refusal and when each push arrives, is at
// https://docs.stxapp.io/websockets/channels/combo-negotiation/.

const (
	comboPlayerChannel      = "rfq"
	comboMakerChannel       = "combo_quote"
	combosUnavailableReason = "unmatched topic"
	// An accept can take up to 30s on the exchange; wait a little longer.
	comboAcceptTimeout = 35 * time.Second
	comboHistorySize   = 1000
)

// ErrComboPriceMoved: an accept found nothing live at least as good as the
// amount; the request stays open. See ComboRefusedError.Best for the live quote.
var ErrComboPriceMoved = errors.New("stx: combo price moved")

// ComboRefusedError is a combo command or join the exchange refused.
type ComboRefusedError struct {
	// Command is what was sent: "request_quote", "accept", "phx_join", ...
	Command string
	// Topic is the wire topic.
	Topic string
	// Code is the refusal reason, such as "not_found" or "price_moved". A
	// "player_refused:<detail>" reason has Code "player_refused" and the rest
	// in Detail.
	Code   string
	Detail string
	// Reason is the reason exactly as sent.
	Reason string
	// Reply is the raw reply.
	Reply json.RawMessage
	// Best is the live best quote on "price_moved", or nil.
	Best *ComboBestQuote
}

// Error returns "stx: COMMAND on TOPIC refused: REASON".
func (e *ComboRefusedError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = string(e.Reply)
	}
	return fmt.Sprintf("stx: %s on %s refused: %s", e.Command, e.Topic, reason)
}

// Is matches [ErrComboPriceMoved], [ErrNotFound] for "not_found",
// [ErrUnauthorized] for "unauthorized" and [ErrInsufficientScope] for
// "insufficient_scope".
func (e *ComboRefusedError) Is(target error) bool {
	switch target {
	case ErrComboPriceMoved:
		return e.Code == "price_moved"
	case ErrNotFound:
		return e.Code == "not_found"
	case ErrUnauthorized:
		return e.Code == "unauthorized"
	case ErrInsufficientScope:
		return e.Code == "insufficient_scope"
	}
	return false
}

func comboRefusal(command, topic string, response json.RawMessage, host string) error {
	var body struct {
		Reason    *string `json:"reason"`
		QuoteID   string  `json:"quote_id"`
		Amount    Decimal `json:"amount"`
		ExpiresAt int64   `json:"expires_at"`
	}
	_ = json.Unmarshal(response, &body)
	if body.Reason == nil {
		return &ComboRefusedError{Command: command, Topic: topic, Reply: response}
	}
	reason := *body.Reason
	if reason == combosUnavailableReason {
		return &NotAvailableError{Feature: "combos", Host: host, Err: &ChannelError{Topic: topic, Event: command, Reason: reason, Reply: response}}
	}
	e := &ComboRefusedError{Command: command, Topic: topic, Code: reason, Reason: reason, Reply: response}
	if detail, ok := strings.CutPrefix(reason, "player_refused:"); ok {
		e.Code, e.Detail = "player_refused", detail
	}
	if e.Code == "price_moved" {
		e.Best = &ComboBestQuote{QuoteID: body.QuoteID, Amount: body.Amount, ExpiresAt: body.ExpiresAt}
	}
	return e
}

// comboJoinError turns a refused join into a combo error; anything else
// passes through.
func comboJoinError(err error, host string) error {
	var ce *ChannelError
	if errors.As(err, &ce) && !ce.interrupted {
		return comboRefusal("phx_join", ce.Topic, ce.Reply, host)
	}
	return err
}

// ComboOutcome is what the combination needs of a leg: "won" or "lost".
type ComboOutcome string

// The outcomes of a leg.
const (
	ComboWon  ComboOutcome = "won"
	ComboLost ComboOutcome = "lost"
)

// ComboLeg is one leg of a quote request: an open, ordinary market and the
// outcome the combination needs of it.
type ComboLeg struct {
	MarketID string       `json:"market_id"`
	Outcome  ComboOutcome `json:"outcome"`
}

// ComboRequest is the reply to [ComboPlayer.RequestQuote].
type ComboRequest struct {
	RFQID string `json:"rfq_id"`
}

// ComboCashOut is the reply to [ComboPlayer.CashOut].
type ComboCashOut struct {
	RFQID string `json:"rfq_id"`
	// MarketID is the combination being sold.
	MarketID string `json:"market_id"`
	// Quantity is set by the exchange: what you hold less your resting sells.
	Quantity Decimal `json:"quantity"`
}

// ComboPlacement is what an accept placed for you: your order on the
// combination's market. Its fills arrive on the fills channel.
type ComboPlacement struct {
	MarketID string  `json:"market_id"`
	OrderID  string  `json:"order_id"`
	Quantity Decimal `json:"quantity"`
	Price    Decimal `json:"price"`
}

// ComboBestQuote is the best live quote. Every field is empty when nothing
// is live. ExpiresAt is in Unix microseconds.
type ComboBestQuote struct {
	QuoteID   string  `json:"quote_id"`
	Amount    Decimal `json:"amount"`
	ExpiresAt int64   `json:"expires_at"`
}

// ComboSnapshot is the reply to [ComboPlayer.Snapshot]: the request now.
type ComboSnapshot struct {
	RFQID string `json:"rfq_id"`
	// MarketID is empty on a buy until a bind places, and the combination on
	// a cash out.
	MarketID string `json:"market_id"`
	// Status is "open", or "binding" while an acceptance waits for its next
	// bind attempt.
	Status string `json:"status"`
	// Side is "buy" for a request, "sell" for a cash out.
	Side string `json:"side"`
	// Amount is the stake on a buy; Quantity the size on a cash out.
	Amount    Decimal        `json:"amount"`
	Quantity  Decimal        `json:"quantity"`
	ExpiresAt int64          `json:"expires_at"`
	BestQuote ComboBestQuote `json:"best_quote"`
}

// ComboQuote is the reply to [ComboMaker.Quote]. It carries no price or
// size: the bind sets those.
type ComboQuote struct {
	QuoteID string  `json:"quote_id"`
	Amount  Decimal `json:"amount"`
	// ExternalID is the player's pseudonym.
	ExternalID string `json:"external_id"`
}

// ComboBestQuoteEvent is "best_quote" to the player: the best quote changed.
type ComboBestQuoteEvent struct {
	RFQID    string `json:"rfq_id"`
	MarketID string `json:"market_id"`
	ComboBestQuote
}

// ComboPlacedEvent is "placed" to the player: the bind placed your order.
type ComboPlacedEvent struct {
	RFQID string `json:"rfq_id"`
	ComboPlacement
}

// ComboRequestClosedEvent is "negotiation_closed" to the player: the
// request ended without placing. Reason is "cancelled", "expired",
// "leg_not_quotable", "unknown_leg", "player_refused" or "maker_refused".
type ComboRequestClosedEvent struct {
	RFQID    string `json:"rfq_id"`
	MarketID string `json:"market_id"`
	Reason   string `json:"reason"`
}

// ComboRFQLeg is one leg as makers see it. ShortName is the leg market's
// display name, when the exchange has one.
type ComboRFQLeg struct {
	MarketID  string       `json:"market_id"`
	Outcome   ComboOutcome `json:"outcome"`
	ShortName string       `json:"short_name"`
}

// ComboRFQOpenedEvent is "rfq_opened" to makers: a player opened a request.
// Price it against MaxPrice and TickSize, not constants.
type ComboRFQOpenedEvent struct {
	RFQID string `json:"rfq_id"`
	// MarketID is empty on a buy and the combination on a cash out.
	MarketID string `json:"market_id"`
	// ExternalID is the player's pseudonym.
	ExternalID string `json:"external_id"`
	// Side is the player's side; you take the other.
	Side      string        `json:"side"`
	Amount    Decimal       `json:"amount"`
	Quantity  Decimal       `json:"quantity"`
	MaxPrice  Decimal       `json:"max_price"`
	TickSize  Decimal       `json:"tick_size"`
	ExpiresAt int64         `json:"expires_at"`
	Legs      []ComboRFQLeg `json:"legs"`
}

// ComboRFQClosedEvent is "rfq_closed" to makers: a request ended. Reason is
// "placed", "cancelled", "expired", "leg_not_quotable", "unknown_leg",
// "player_refused", "maker_refused" or "ended".
type ComboRFQClosedEvent struct {
	RFQID    string `json:"rfq_id"`
	MarketID string `json:"market_id"`
	Reason   string `json:"reason"`
}

// ComboQuoteAcceptedEvent is "quote_accepted" to the maker: this quote is
// about to be bound.
type ComboQuoteAcceptedEvent struct {
	RFQID      string `json:"rfq_id"`
	MarketID   string `json:"market_id"`
	ExternalID string `json:"external_id"`
	QuoteID    string `json:"quote_id"`
}

// ComboMakerPlacedEvent is "placed" to the maker: the quote bound and your
// order was placed, at the same size and price as the player's.
type ComboMakerPlacedEvent struct {
	RFQID      string  `json:"rfq_id"`
	MarketID   string  `json:"market_id"`
	ExternalID string  `json:"external_id"`
	QuoteID    string  `json:"quote_id"`
	OrderID    string  `json:"order_id"`
	Quantity   Decimal `json:"quantity"`
	Price      Decimal `json:"price"`
}

// ComboQuoteClosedEvent is "negotiation_closed" to the maker: this quote
// stopped standing. Reason is "placed_elsewhere", "maker_refused",
// "cancelled", "expired", "leg_not_quotable", "unknown_leg" or
// "player_refused".
type ComboQuoteClosedEvent struct {
	RFQID      string `json:"rfq_id"`
	MarketID   string `json:"market_id"`
	ExternalID string `json:"external_id"`
	QuoteID    string `json:"quote_id"`
	Reason     string `json:"reason"`
}

// ComboEvent is one push on a combo topic.
type ComboEvent struct {
	// Event is the event name, for example "best_quote" or "rfq_opened".
	Event string
	// RFQID is the request it is about; QuoteID the quote, on maker events
	// that name one.
	RFQID   string
	QuoteID string
	Payload json.RawMessage
	// Data is the typed payload. To the player: *ComboBestQuoteEvent,
	// *ComboPlacedEvent or *ComboRequestClosedEvent. To the maker:
	// *ComboRFQOpenedEvent, *ComboRFQClosedEvent, *ComboQuoteAcceptedEvent,
	// *ComboMakerPlacedEvent or *ComboQuoteClosedEvent.
	Data any
}

func playerEvent(event string) any {
	switch event {
	case "best_quote":
		return new(ComboBestQuoteEvent)
	case "placed":
		return new(ComboPlacedEvent)
	case "negotiation_closed":
		return new(ComboRequestClosedEvent)
	}
	return nil
}

func makerEvent(event string) any {
	switch event {
	case "rfq_opened":
		return new(ComboRFQOpenedEvent)
	case "rfq_closed":
		return new(ComboRFQClosedEvent)
	case "quote_accepted":
		return new(ComboQuoteAcceptedEvent)
	case "placed":
		return new(ComboMakerPlacedEvent)
	case "negotiation_closed":
		return new(ComboQuoteClosedEvent)
	}
	return nil
}

// ComboWait selects the event [ComboPlayer.WaitFor] and
// [ComboMaker.WaitFor] wait for. Empty fields match anything.
type ComboWait struct {
	// Events are the event names to match, for example "best_quote".
	Events  []string
	RFQID   string
	QuoteID string
}

func (w ComboWait) match(e ComboEvent) bool {
	if len(w.Events) > 0 && !containsString(w.Events, e.Event) {
		return false
	}
	return (w.RFQID == "" || e.RFQID == w.RFQID) && (w.QuoteID == "" || e.QuoteID == w.QuoteID)
}

type comboWaiter struct {
	w  ComboWait
	ch chan ComboEvent
}

// comboSession is what the player and maker share: the joined topic, typed
// events, a history and waiting.
type comboSession struct {
	sub    *Subscription
	typeOf func(string) any
	events chan ComboEvent
	done   chan struct{}

	mu      sync.Mutex
	history []ComboEvent
	waiters []*comboWaiter
}

func (s *Stream) joinCombo(ctx context.Context, name string, typeOf func(string) any) (*comboSession, error) {
	sub, err := s.subscribe(ctx, accountChannel(name, map[string]any{}))
	if err != nil {
		return nil, err
	}
	cs := &comboSession{sub: sub, typeOf: typeOf, events: make(chan ComboEvent, s.cfg.buffer), done: make(chan struct{})}
	s.goTracked(cs.run)
	return cs, nil
}

func (cs *comboSession) run() {
	defer close(cs.done)
	defer close(cs.events)
	for m := range cs.sub.Messages() {
		target := cs.typeOf(m.Event)
		if target == nil {
			continue
		}
		if json.Unmarshal(m.Payload, target) != nil {
			continue
		}
		var ids struct {
			RFQID   string `json:"rfq_id"`
			QuoteID string `json:"quote_id"`
		}
		_ = json.Unmarshal(m.Payload, &ids)
		e := ComboEvent{Event: m.Event, RFQID: ids.RFQID, QuoteID: ids.QuoteID, Payload: m.Payload, Data: target}
		cs.mu.Lock()
		cs.history = append(cs.history, e)
		if len(cs.history) > comboHistorySize {
			cs.history = cs.history[len(cs.history)-comboHistorySize:]
		}
		kept := cs.waiters[:0]
		for _, w := range cs.waiters {
			if w.w.match(e) {
				w.ch <- e
			} else {
				kept = append(kept, w)
			}
		}
		cs.waiters = kept
		cs.mu.Unlock()
		select {
		case cs.events <- e:
		default:
			select {
			case <-cs.events:
				cs.sub.s.log.Warn("combo event buffer full; dropped the oldest event", "topic", cs.sub.topic)
			default:
			}
			select {
			case cs.events <- e:
			default:
			}
		}
	}
}

func (cs *comboSession) externalID() string {
	var r struct {
		ExternalID string `json:"external_id"`
	}
	_ = json.Unmarshal(cs.sub.Reply(), &r)
	return r.ExternalID
}

func (cs *comboSession) historyCopy() []ComboEvent {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]ComboEvent(nil), cs.history...)
}

func (cs *comboSession) waitFor(ctx context.Context, w ComboWait) (ComboEvent, error) {
	cs.mu.Lock()
	for _, e := range cs.history {
		if w.match(e) {
			cs.mu.Unlock()
			return e, nil
		}
	}
	waiter := &comboWaiter{w: w, ch: make(chan ComboEvent, 1)}
	cs.waiters = append(cs.waiters, waiter)
	cs.mu.Unlock()
	remove := func() {
		cs.mu.Lock()
		for i, x := range cs.waiters {
			if x == waiter {
				cs.waiters = append(cs.waiters[:i], cs.waiters[i+1:]...)
				break
			}
		}
		cs.mu.Unlock()
	}
	select {
	case e := <-waiter.ch:
		return e, nil
	case <-ctx.Done():
		remove()
		return ComboEvent{}, ctx.Err()
	case <-cs.done:
		remove()
		select {
		case e := <-waiter.ch:
			return e, nil
		default:
		}
		if err := cs.sub.Err(); err != nil {
			return ComboEvent{}, err
		}
		return ComboEvent{}, ErrStreamClosed
	}
}

func (cs *comboSession) command(ctx context.Context, command string, payload any, timeout time.Duration, out any) error {
	raw, err := cs.sub.push(ctx, command, payload, timeout)
	if err != nil {
		var ce *ChannelError
		if errors.As(err, &ce) && !ce.interrupted {
			return comboRefusal(command, cs.sub.topic, ce.Reply, cs.sub.s.b.host)
		}
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func checkAmount(name string, d Decimal) error {
	if !d.Valid() {
		return fmt.Errorf("%w: %s must be a decimal such as \"250.00\", got %q", ErrConfig, name, string(d))
	}
	return nil
}

// CombosService trades combos: requests for quote as a player (taker), and
// quotes as a maker. Use it through [Client.Combos]. Its Player and Maker
// open a stream of their own; to share one socket with other subscriptions,
// use [Stream.ComboPlayer] and [Stream.ComboMaker].
//
// Where an exchange does not offer combos, joining returns an error matching
// [ErrNotAvailable].
type CombosService struct{ c *Client }

// Player opens a stream and joins the player topic. Closing the player
// closes that stream.
func (s *CombosService) Player(ctx context.Context, opts ...StreamOption) (*ComboPlayer, error) {
	st, err := s.c.Stream(ctx, opts...)
	if err != nil {
		return nil, err
	}
	p, err := st.ComboPlayer(ctx)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	p.owned = st
	return p, nil
}

// Maker opens a stream and joins the maker topic. Closing the maker closes
// that stream.
func (s *CombosService) Maker(ctx context.Context, opts ...StreamOption) (*ComboMaker, error) {
	st, err := s.c.Stream(ctx, opts...)
	if err != nil {
		return nil, err
	}
	m, err := st.ComboMaker(ctx)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	m.owned = st
	return m, nil
}

// Available reports whether this exchange offers combos, by joining a
// player topic nobody owns: the exchange answers "unmatched topic" where it
// has no combos, and refuses the join for another reason where it does.
// Nothing is created.
func (s *CombosService) Available(ctx context.Context) (bool, error) {
	st, err := s.c.Stream(ctx, WithoutReconnect())
	if err != nil {
		return false, err
	}
	defer st.Close()
	return combosAvailable(ctx, st)
}

// comboProbeUser is a user id no account has.
const comboProbeUser = "00000000-0000-4000-8000-000000000000"

func combosAvailable(ctx context.Context, st *Stream) (bool, error) {
	ch := Channel{name: comboPlayerChannel, topic: comboPlayerChannel + ":" + comboProbeUser, payload: map[string]any{}}
	sub, err := st.subscribe(ctx, ch)
	var refused *ComboRefusedError
	switch {
	case err == nil:
		_ = sub.Close()
		return true, nil
	case errors.Is(err, ErrNotAvailable):
		return false, nil
	case errors.As(err, &refused):
		return true, nil
	}
	return false, err
}

func init() {
	probeCombos = func(ctx context.Context, c *Client) *bool {
		ok, err := c.Combos.Available(ctx)
		if err != nil {
			c.logger.Debug("combos probe failed", "err", err)
			return nil
		}
		return &ok
	}
}

// ComboPlayer is the player's side of combos, on rfq:<user_id>. Get one with
// [Stream.ComboPlayer].
//
// A request belongs to this topic: closing it or a dropped socket ends the
// request "cancelled", and the stream's rejoin does not bring it back. Every
// refusal is a *[ComboRefusedError] with the exchange's reason in Code.
type ComboPlayer struct {
	cs    *comboSession
	owned *Stream
}

// ComboPlayer joins rfq:<user_id>. Where combos are not available it returns
// a *[NotAvailableError] (errors.Is(err, ErrNotAvailable)); other refusals,
// such as a read-only key, are a *[ComboRefusedError]. ctx bounds the join and the
// player's life.
func (s *Stream) ComboPlayer(ctx context.Context) (*ComboPlayer, error) {
	cs, err := s.joinCombo(ctx, comboPlayerChannel, playerEvent)
	if err != nil {
		return nil, err
	}
	return &ComboPlayer{cs: cs}, nil
}

// ExternalID returns your pseudonym, which makers see on your requests.
func (p *ComboPlayer) ExternalID() string { return p.cs.externalID() }

// Topic returns the wire topic.
func (p *ComboPlayer) Topic() string { return p.cs.sub.topic }

// Events returns the channel typed events arrive on, closed when the player
// ends.
func (p *ComboPlayer) Events() <-chan ComboEvent { return p.cs.events }

// History returns the events received so far (the latest 1000), oldest
// first.
func (p *ComboPlayer) History() []ComboEvent { return p.cs.historyCopy() }

// WaitFor returns the first event that matches w, looking first at the
// events already received. It takes nothing from [ComboPlayer.Events].
func (p *ComboPlayer) WaitFor(ctx context.Context, w ComboWait) (ComboEvent, error) {
	return p.cs.waitFor(ctx, w)
}

// Done is closed when the player ends.
func (p *ComboPlayer) Done() <-chan struct{} { return p.cs.done }

// Err returns why the player ended, like [Subscription.Err].
func (p *ComboPlayer) Err() error { return p.cs.sub.Err() }

// Close leaves the topic. Every request still open on it ends "cancelled".
// To see each request's own close first, cancel it and wait for its
// "negotiation_closed" before closing.
func (p *ComboPlayer) Close() error {
	err := p.cs.sub.Close()
	if p.owned != nil {
		return p.owned.Close()
	}
	return err
}

// RequestQuote asks makers to quote on these legs for a stake in dollars,
// such as "250.00" (whole cents). Makers quote what you win; quotes arrive
// as "best_quote" events.
func (p *ComboPlayer) RequestQuote(ctx context.Context, legs []ComboLeg, amount Decimal) (ComboRequest, error) {
	var out ComboRequest
	if err := checkAmount("amount", amount); err != nil {
		return out, err
	}
	err := p.cs.command(ctx, "request_quote", map[string]any{"legs": legs, "amount": amount}, 0, &out)
	return out, err
}

// CashOut asks makers to quote for what you hold of a combination, by the
// market id from "placed". The exchange sets the size.
func (p *ComboPlayer) CashOut(ctx context.Context, marketID string) (ComboCashOut, error) {
	var out ComboCashOut
	err := p.cs.command(ctx, "cash_out", map[string]any{"market_id": marketID}, 0, &out)
	return out, err
}

// Accept binds the request at amount or better, normally the amount
// "best_quote" showed; the best live quote binds, so the placement can be
// better than amount. It waits up to 35 seconds unless ctx ends first.
//
// When nothing at least as good is live it returns a *[ComboRefusedError] matching
// [ErrComboPriceMoved], with the live quote in Best; the request stays open
// and you can accept again. The reply says what was placed; fills arrive on
// the fills channel.
func (p *ComboPlayer) Accept(ctx context.Context, rfqID, quoteID string, amount Decimal) (ComboPlacement, error) {
	var out ComboPlacement
	if err := checkAmount("amount", amount); err != nil {
		return out, err
	}
	err := p.cs.command(ctx, "accept", map[string]any{"rfq_id": rfqID, "quote_id": quoteID, "amount": amount}, comboAcceptTimeout, &out)
	return out, err
}

// Cancel ends the request and drops every quote on it.
func (p *ComboPlayer) Cancel(ctx context.Context, rfqID string) error {
	return p.cs.command(ctx, "cancel", map[string]any{"rfq_id": rfqID}, 0, nil)
}

// Snapshot returns the request now, with the live best quote.
func (p *ComboPlayer) Snapshot(ctx context.Context, rfqID string) (ComboSnapshot, error) {
	var out ComboSnapshot
	err := p.cs.command(ctx, "snapshot", map[string]any{"rfq_id": rfqID}, 0, &out)
	return out, err
}

// ComboMaker is the maker's side of combos, on combo_quote:<user_id>. Get
// one with [Stream.ComboMaker]. Joining subscribes to every request opened
// from then on ("rfq_opened"); earlier requests are not replayed.
//
// A quote belongs to this topic: closing it or a dropped socket withdraws
// it. Every refusal is a *[ComboRefusedError] with the exchange's reason in Code.
type ComboMaker struct {
	cs    *comboSession
	owned *Stream
}

// ComboMaker joins combo_quote:<user_id>. Where combos are not available it
// returns a *[NotAvailableError]. ctx bounds the join and the maker's life.
func (s *Stream) ComboMaker(ctx context.Context) (*ComboMaker, error) {
	cs, err := s.joinCombo(ctx, comboMakerChannel, makerEvent)
	if err != nil {
		return nil, err
	}
	return &ComboMaker{cs: cs}, nil
}

// ExternalID returns your pseudonym. Makers' pseudonyms are shown to no one.
func (m *ComboMaker) ExternalID() string { return m.cs.externalID() }

// Topic returns the wire topic.
func (m *ComboMaker) Topic() string { return m.cs.sub.topic }

// Events returns the channel typed events arrive on, closed when the maker
// ends.
func (m *ComboMaker) Events() <-chan ComboEvent { return m.cs.events }

// History returns the events received so far (the latest 1000), oldest
// first.
func (m *ComboMaker) History() []ComboEvent { return m.cs.historyCopy() }

// WaitFor returns the first event that matches w, looking first at the
// events already received. It takes nothing from [ComboMaker.Events].
func (m *ComboMaker) WaitFor(ctx context.Context, w ComboWait) (ComboEvent, error) {
	return m.cs.waitFor(ctx, w)
}

// Done is closed when the maker ends.
func (m *ComboMaker) Done() <-chan struct{} { return m.cs.done }

// Err returns why the maker ended, like [Subscription.Err].
func (m *ComboMaker) Err() error { return m.cs.sub.Err() }

// Close leaves the topic, which withdraws every quote placed on it.
func (m *ComboMaker) Close() error {
	err := m.cs.sub.Close()
	if m.owned != nil {
		return m.owned.Close()
	}
	return err
}

// ComboQuoteRequest is a maker's quote.
type ComboQuoteRequest struct {
	// RFQID is the request, from "rfq_opened".
	RFQID string
	// Amount, in dollars: on a buy what the player wins, on a cash out what
	// you pay for the whole size.
	Amount Decimal
	// ExpiresIn is how long the quote stands, counted from receipt, in whole
	// milliseconds. The quote ends no later than its request.
	ExpiresIn time.Duration
	// ClientOrderID (at most 255 bytes) is copied onto your order if the
	// quote binds.
	ClientOrderID string
}

// Quote offers an amount on a request. Price it against the request's
// MaxPrice and TickSize.
func (m *ComboMaker) Quote(ctx context.Context, q ComboQuoteRequest) (ComboQuote, error) {
	var out ComboQuote
	if err := checkAmount("amount", q.Amount); err != nil {
		return out, err
	}
	payload := map[string]any{"rfq_id": q.RFQID, "amount": q.Amount, "expires_in_ms": q.ExpiresIn.Milliseconds()}
	if q.ClientOrderID != "" {
		payload["client_order_id"] = q.ClientOrderID
	}
	err := m.cs.command(ctx, "quote", payload, 0, &out)
	return out, err
}

// WithdrawQuote removes one of your quotes.
func (m *ComboMaker) WithdrawQuote(ctx context.Context, rfqID, quoteID string) error {
	return m.cs.command(ctx, "withdraw_quote", map[string]any{"rfq_id": rfqID, "quote_id": quoteID}, 0, nil)
}
