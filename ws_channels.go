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

// Topics with no ping command. The exchange refuses one on the combo topics
// and drops the market:<id> topic on it; the socket heartbeat keeps them up.
var unpingedChannels = map[string]bool{"market": true, "rfq": true, "combo_quote": true}

// errSubscriptionEnded: a join for a subscription that has ended.
var errSubscriptionEnded = errors.New("stx: subscription ended")

// Channel is a topic to subscribe to, with its join filters. Build one with
// a constructor such as [OrderbookChannel] or [OrdersChannel] and pass it to
// [Stream.Subscribe]. Each channel's events and payloads are documented at
// https://docs.stxapp.io/websockets/.
type Channel struct {
	name    string
	topic   string // empty for an account channel: name:<user id>
	private bool
	payload map[string]any
	watch   []string
	err     error
}

// Name returns the channel's name, for example "orderbook" or "orders".
func (c Channel) Name() string { return c.name }

func publicChannel(name string, payload map[string]any) Channel {
	return Channel{name: name, topic: name, payload: payload}
}

func accountChannel(name string, payload map[string]any) Channel {
	return Channel{name: name, private: true, payload: payload}
}

func withList(p map[string]any, key string, values []string) map[string]any {
	if p == nil {
		p = map[string]any{}
	}
	if values != nil {
		p[key] = append([]string(nil), values...)
	}
	return p
}

func marketIDs(ids []string) map[string]any {
	if len(ids) == 0 {
		return map[string]any{}
	}
	return withList(nil, "market_ids", ids)
}

// OrderbookChannel is the aggregated book of each market named: one "book"
// message per market on the exchange's publish cadence, each a full snapshot
// of that market's book to replace what you hold. At least one market id is
// required.
func OrderbookChannel(marketIDs ...string) Channel {
	c := publicChannel("orderbook", withList(nil, "market_ids", marketIDs))
	if len(marketIDs) == 0 {
		c.err = fmt.Errorf("%w: the orderbook channel needs at least one market id", ErrConfig)
	}
	return c
}

// TickerFilter narrows [TickerChannel]. Values match exactly, so "Football"
// and "football" differ.
type TickerFilter struct {
	Sports       []string
	Competitions []string
}

// TickerChannel sends a "ticker" message for each market whose price, top of
// book, volume or open interest moves. Nothing is sent on join.
func TickerChannel(f TickerFilter) Channel {
	p := withList(nil, "sports", f.Sports)
	return publicChannel("ticker", withList(p, "competitions", f.Competitions))
}

// TradesFilter narrows [TradesChannel].
type TradesFilter struct {
	MarketIDs []string
	EventIDs  []string
}

// TradesChannel sends every execution on the exchange as a "trade" message,
// anonymized. Action is the taker's side. Your own executions are on
// [FillsChannel].
func TradesChannel(f TradesFilter) Channel {
	p := withList(nil, "market_ids", f.MarketIDs)
	return publicChannel("trades", withList(p, "event_ids", f.EventIDs))
}

// EventsChannel sends each named event's traded volume as an "event"
// message whenever it changes. The join reply carries the current values. At
// least one event id is required.
func EventsChannel(eventIDs ...string) Channel {
	c := publicChannel("events", withList(nil, "event_ids", eventIDs))
	if len(eventIDs) == 0 {
		c.err = fmt.Errorf("%w: the events channel needs at least one event id", ErrConfig)
	}
	return c
}

// MarketsFilter narrows [MarketsChannel].
type MarketsFilter struct {
	// RuleFilters keeps only markets whose rules match one of these.
	RuleFilters []string
	// MessageTypes picks "market_created", "market_updated" or both.
	MessageTypes []string
}

// MarketsChannel sends "market_created" and "market_updated" for every
// market. Each message maps market id to a [MarketChange]; an update carries
// only the fields that changed. The exchange sends prices on this channel in
// cents; the stream converts them to dollar strings like the rest of the API.
func MarketsChannel(f MarketsFilter) Channel {
	p := withList(nil, "rule_filters", f.RuleFilters)
	return publicChannel("markets", withList(p, "message_types", f.MessageTypes))
}

// StatsRange is how far back a price series reaches.
type StatsRange string

// The ranges [MarketStatsChannel] and [Subscription.RequestSeries] take.
const (
	StatsDay   StatsRange = "day"
	StatsWeek  StatsRange = "week"
	StatsMonth StatsRange = "month"
	StatsAll   StatsRange = "all"
)

// MarketStatsChannel is the price series of each market named. The join
// reply holds the history (read it with [Subscription.Reply] into a
// [MarketStatsReply]); "market_stats" then sends changed buckets to upsert
// by timestamp, and "market_stats_snapshot" a series to replace. An empty
// range means [StatsAll]. At least one market id is required.
func MarketStatsChannel(r StatsRange, marketIDs ...string) Channel {
	p := withList(nil, "market_ids", marketIDs)
	if r != "" {
		p["range"] = string(r)
	}
	c := publicChannel("market_stats", p)
	if len(marketIDs) == 0 {
		c.err = fmt.Errorf("%w: the market_stats channel needs at least one market id", ErrConfig)
	}
	return c
}

// MarketUpdatesChannel sends "created" and "updated" for the markets you
// watch: these, and any added with [Subscription.Watch]. Watches are sent
// again after every rejoin. Prices are converted from cents to dollar
// strings.
func MarketUpdatesChannel(watch ...string) Channel {
	c := publicChannel("market_updates", map[string]any{})
	c.watch = append([]string(nil), watch...)
	return c
}

// MarketChannel is one market in full, by id or symbol. The join reply is
// the market's current state with its book (read it with
// [Subscription.Reply] into a [MarketChange]); then "market_update" carries
// the fields that changed and "order_book_update" a fresh book. A resulted or
// voided market sends its last update, the exchange closes the topic, and
// the subscription ends. Prices are converted from cents to dollar strings.
func MarketChannel(marketIDOrSymbol string) Channel {
	c := publicChannel("market:"+marketIDOrSymbol, map[string]any{})
	c.name = "market"
	if marketIDOrSymbol == "" {
		c.err = fmt.Errorf("%w: the market channel needs a market id or symbol", ErrConfig)
	}
	return c
}

// OrdersFilter configures [OrdersChannel].
type OrdersFilter struct {
	// MarketIDs keeps only orders on these markets; empty means all.
	MarketIDs []string
	// CancelOnDisconnect arms cancel-on-disconnect for the orders you place
	// with it. The stream then pings at a third of the timeout the exchange
	// grants for as long as the topic is joined.
	CancelOnDisconnect bool
	// PingTimeout is the cancel-on-disconnect deadline to ask for. The
	// exchange clamps it to 5s to 20s. Default 5s.
	PingTimeout time.Duration
}

// OrdersChannel sends your open orders on join ("all_orders", an
// [OrdersSnapshot]), then each order whenever it changes ("new_open_order",
// an [Order]).
func OrdersChannel(f OrdersFilter) Channel {
	p := marketIDs(f.MarketIDs)
	if f.CancelOnDisconnect {
		p["cancel_on_disconnect"] = true
		ms := f.PingTimeout.Milliseconds()
		if ms <= 0 {
			ms = 5000
		}
		p["ping_timeout"] = ms
	}
	return accountChannel("orders", p)
}

// FillsChannel sends the fills behind your open positions on join
// ("all_trades", a [FillsSnapshot]), then one "trade" ([Fill]) per execution
// or status change. Empty marketIDs means all markets.
func FillsChannel(marketIDs_ ...string) Channel {
	return accountChannel("fills", marketIDs(marketIDs_))
}

// PositionsChannel sends your positions on join ("all_positions", a
// [PositionsSnapshot]), then "updated_positions" ([PositionsUpdate]) with
// only the positions that changed.
func PositionsChannel(marketIDs_ ...string) Channel {
	return accountChannel("positions", marketIDs(marketIDs_))
}

// SettlementsChannel sends "new_settlements" ([SettlementsUpdate]) as they
// are recorded. There is no snapshot; read history over REST.
func SettlementsChannel(marketIDs_ ...string) Channel {
	return accountChannel("settlements", marketIDs(marketIDs_))
}

// BalancesChannel sends your balance on join ("balances", a [Balance]), the
// same summary on every change ("update"), and "payment_update"
// ([PaymentUpdate]) as payments move. An empty accountID means the account
// the socket signed in as.
func BalancesChannel(accountID string) Channel {
	p := map[string]any{}
	if accountID != "" {
		p["account_id"] = accountID
	}
	return accountChannel("balances", p)
}

// AccountChannel carries everything the orders, fills, positions,
// settlements and balances channels carry, on one topic. Do not also
// subscribe to one of those (you would get every message twice), and use
// [OrdersChannel] if you need cancel-on-disconnect.
func AccountChannel(marketIDs_ ...string) Channel {
	return accountChannel("account", marketIDs(marketIDs_))
}

// UserInfoChannel sends your profile right after joining, then on every
// change ("user_updated", a [UserInfo]).
func UserInfoChannel() Channel {
	return accountChannel("user_info", map[string]any{})
}

// snapshotEvents are the events that carry state on join, by channel.
var snapshotEvents = map[string][]string{
	"orders":    {"all_orders"},
	"fills":     {"all_trades"},
	"positions": {"all_positions"},
	"balances":  {"balances"},
	"account":   {"all_orders", "all_trades", "all_positions", "balances"},
	"user_info": {"user_updated"},
}

func isSnapshot(channel, event string) bool {
	for _, e := range snapshotEvents[channel] {
		if e == event {
			return true
		}
	}
	return false
}

// Subscribe joins ch and returns the subscription. Messages arrive on
// [Subscription.Messages] until ctx is done, the subscription is closed, or
// the stream ends; then the channel is closed. ctx bounds the join and the
// subscription's whole life.
//
// A refused join returns a *[ChannelError] with the exchange's reason, such
// as "unauthorized". The account channels need a signed stream.
func (s *Stream) Subscribe(ctx context.Context, ch Channel) (*Subscription, error) {
	if ch.err != nil {
		return nil, ch.err
	}
	return s.subscribe(ctx, ch)
}

func (s *Stream) subscribe(ctx context.Context, ch Channel) (*Subscription, error) {
	topic := ch.topic
	if ch.private {
		if !s.b.signed {
			return nil, configError("the %s channel is private and needs credentials; the stream is not signed", ch.name)
		}
		id, err := s.resolveUserID(ctx)
		if err != nil {
			return nil, err
		}
		topic = ch.name + ":" + id
	}
	sub := &Subscription{
		s:       s,
		topic:   topic,
		name:    ch.name,
		payload: ch.payload,
		watches: append([]string(nil), ch.watch...),
		msgs:    make(chan Message, s.cfg.buffer),
		done:    make(chan struct{}),
	}
	if sub.payload == nil {
		sub.payload = map[string]any{}
	}
	if !unpingedChannels[ch.name] {
		sub.pingBase = s.cfg.channelPing
	}
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		return nil, ErrStreamClosed
	}
	if s.subs[topic] != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w to %s", ErrAlreadySubscribed, topic)
	}
	s.subs[topic] = sub
	s.mu.Unlock()

	fail := func(err error) (*Subscription, error) {
		s.mu.Lock()
		sub.closing = true
		if s.subs[topic] == sub {
			delete(s.subs, topic)
		}
		s.mu.Unlock()
		sub.end(err)
		return nil, err
	}
	if err := s.waitConnected(ctx); err != nil {
		return fail(err)
	}
	if _, err := sub.join(ctx); err != nil {
		return fail(sub.joinError(err))
	}
	s.goTracked(func() {
		select {
		case <-ctx.Done():
			sub.leave(ctx.Err())
		case <-sub.done:
		}
	})
	return sub, nil
}

// Subscription is one joined topic on a [Stream].
type Subscription struct {
	s     *Stream
	topic string
	name  string

	msgs   chan Message
	done   chan struct{}
	sendMu sync.Mutex
	ended  bool

	joinMu sync.Mutex // one join at a time

	// Guarded by s.mu.
	payload     map[string]any
	watches     []string
	joinRef     string
	joined      bool
	joinedGen   int
	restoredGen int
	reply       json.RawMessage
	closing     bool
	finished    bool
	err         error
	pingBase    time.Duration
	pingEvery   time.Duration
	stopPing    chan struct{}
}

// Topic returns the wire topic, for example "orders:7fb55544-3a7e-4f6b-bd66-fc89bd3a1087".
func (sub *Subscription) Topic() string { return sub.topic }

// Messages returns the channel messages arrive on. It is closed when the
// subscription ends; [Subscription.Err] then says why.
func (sub *Subscription) Messages() <-chan Message { return sub.msgs }

// Done is closed when the subscription ends.
func (sub *Subscription) Done() <-chan struct{} { return sub.done }

// Err returns why the subscription ended: the context's error, the stream's
// error, or a *[ChannelError] when a rejoin was refused for good. It is nil
// while the subscription runs, after Close, and after the exchange closed a
// finished market.
func (sub *Subscription) Err() error {
	sub.s.mu.Lock()
	defer sub.s.mu.Unlock()
	return sub.err
}

// Joined reports whether the topic is joined right now. It is false while
// the stream reconnects.
func (sub *Subscription) Joined() bool {
	sub.s.mu.Lock()
	defer sub.s.mu.Unlock()
	return sub.joined
}

// Reply returns the latest join reply's response: the applied filters, and
// on some channels the state, such as the series on market_stats or the
// whole market on market:<id> (prices in dollar strings).
func (sub *Subscription) Reply() json.RawMessage {
	sub.s.mu.Lock()
	defer sub.s.mu.Unlock()
	return sub.reply
}

// DecodeReply decodes the latest join reply into v.
func (sub *Subscription) DecodeReply(v any) error {
	return json.Unmarshal(sub.Reply(), v)
}

// Close leaves the topic and ends the subscription. Messages already
// buffered stay readable until the channel closes.
func (sub *Subscription) Close() error {
	sub.leave(nil)
	return nil
}

// leave sends phx_leave and ends the subscription with cause.
func (sub *Subscription) leave(cause error) {
	s := sub.s
	s.mu.Lock()
	if sub.closing {
		// Another path is ending it (a leave in flight, the stream closing):
		// wait for that, so Close returns once the topic is left. The topic
		// stays registered, so a closing stream still ends it.
		s.mu.Unlock()
		select {
		case <-sub.done:
		case <-s.done:
		}
		return
	}
	sub.closing = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, leaveTimeout)
	sub.sendLeave(ctx)
	cancel()
	s.mu.Lock()
	if s.subs[sub.topic] == sub {
		delete(s.subs, sub.topic)
	}
	s.mu.Unlock()
	sub.end(cause)
}

func (sub *Subscription) sendLeave(ctx context.Context) {
	s := sub.s
	s.mu.Lock()
	sub.closing = true
	joined, ref := sub.joined, sub.joinRef
	s.mu.Unlock()
	if joined {
		_, _ = s.request(ctx, sub.topic, "phx_leave", struct{}{}, ref, "", leaveTimeout, nil)
	}
}

// end closes the message channel, keeping what is buffered.
func (sub *Subscription) end(cause error) {
	s := sub.s
	s.mu.Lock()
	first := !sub.finished
	sub.finished = true
	sub.closing = true
	sub.joined = false
	sub.stopPingerLocked()
	if first {
		sub.err = cause
	}
	s.mu.Unlock()
	if !first {
		return
	}
	sub.sendMu.Lock()
	defer sub.sendMu.Unlock()
	sub.ended = true
	close(sub.msgs)
	close(sub.done)
}

// deliver queues a message, dropping the oldest when the buffer is full.
func (sub *Subscription) deliver(m Message) {
	sub.sendMu.Lock()
	defer sub.sendMu.Unlock()
	if sub.ended {
		return
	}
	select {
	case sub.msgs <- m:
		return
	default:
	}
	select {
	case <-sub.msgs:
		sub.s.log.Warn("subscription buffer full; dropped the oldest message", "topic", sub.topic)
	default:
	}
	select {
	case sub.msgs <- m:
	default:
	}
}

// resetLocked marks the topic as needing a join. s.mu is held.
func (sub *Subscription) resetLocked() {
	sub.joined = false
	sub.restoredGen = 0
	sub.stopPingerLocked()
}

func (sub *Subscription) stopPingerLocked() {
	if sub.stopPing != nil {
		close(sub.stopPing)
		sub.stopPing = nil
	}
	sub.pingEvery = 0
}

// join joins the topic on the current connection unless it already is,
// then restores its watches and pinger. It reports whether it did anything.
func (sub *Subscription) join(ctx context.Context) (bool, error) {
	sub.joinMu.Lock()
	defer sub.joinMu.Unlock()
	s := sub.s

	s.mu.Lock()
	if sub.closing {
		s.mu.Unlock()
		return false, errSubscriptionEnded
	}
	if s.conn == nil {
		s.mu.Unlock()
		return false, fmt.Errorf("%w: socket is not connected", ErrTransport)
	}
	gen := s.gen
	needJoin := !(sub.joined && sub.joinedGen == gen)
	if !needJoin && sub.restoredGen == gen {
		s.mu.Unlock()
		return false, nil
	}
	var ref string
	var payload map[string]any
	if needJoin {
		ref = s.nextRef()
		sub.resetLocked()
		sub.joinRef = ref
		payload = make(map[string]any, len(sub.payload))
		for k, v := range sub.payload {
			payload[k] = v
		}
	}
	s.mu.Unlock()

	if needJoin {
		// A join's ref doubles as its join_ref, as Phoenix clients do.
		if _, err := s.request(ctx, sub.topic, "phx_join", payload, ref, ref, 0, sub); err != nil {
			return true, err
		}
		s.mu.Lock()
		ok := sub.joined && sub.joinRef == ref
		s.mu.Unlock()
		if !ok {
			return true, &ChannelError{Topic: sub.topic, Event: "phx_join", Reason: "phx_close", interrupted: true}
		}
	}

	// Restore what the join payload does not carry, then the pinger.
	s.mu.Lock()
	watches := append([]string(nil), sub.watches...)
	joinRef := sub.joinRef
	s.mu.Unlock()
	if len(watches) > 0 {
		if _, err := s.request(ctx, sub.topic, "watch", watches, joinRef, "", 0, nil); err != nil {
			return true, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub.closing || !sub.joined || sub.joinRef != joinRef || sub.joinedGen != gen {
		return true, &ChannelError{Topic: sub.topic, Event: "watch", Reason: "phx_close", interrupted: true}
	}
	sub.startPingerLocked(joinRef)
	sub.restoredGen = gen
	return true, nil
}

// joinError maps a refused join for the channel; combos override it.
func (sub *Subscription) joinError(err error) error {
	if sub.name == comboPlayerChannel || sub.name == comboMakerChannel {
		return comboJoinError(err, sub.s.b.host)
	}
	return err
}

// terminalRefusal: a rejoin refused for this reason will not succeed.
func (sub *Subscription) terminalRefusal(reason string) bool {
	if reason == "unauthorized" {
		return true
	}
	return reason == combosUnavailableReason && (sub.name == comboPlayerChannel || sub.name == comboMakerChannel)
}

// startPingerLocked pings the topic while this join lasts. On orders with
// cancel-on-disconnect it pings at a third of the granted timeout, so two
// pings can be lost before the exchange cancels. s.mu is held.
func (sub *Subscription) startPingerLocked(joinRef string) {
	sub.stopPingerLocked()
	every := sub.pingBase
	if cod, _ := sub.payload["cancel_on_disconnect"].(bool); cod && !unpingedChannels[sub.name] {
		var r struct {
			PingTimeout *float64 `json:"ping_timeout"`
		}
		if json.Unmarshal(sub.reply, &r) == nil && r.PingTimeout != nil && *r.PingTimeout > 0 {
			third := time.Duration(*r.PingTimeout/3) * time.Millisecond
			if every == 0 || third < every {
				every = third
			}
		}
	}
	sub.pingEvery = every
	if every <= 0 {
		return
	}
	stop := make(chan struct{})
	sub.stopPing = stop
	s := sub.s
	timeout := every
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}
	s.goTracked(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-s.ctx.Done():
				return
			case <-t.C:
			}
			if _, err := s.request(s.ctx, sub.topic, "ping", struct{}{}, joinRef, "", timeout, nil); err != nil {
				select {
				case <-stop:
					return
				default:
					s.log.Warn("channel ping failed", "topic", sub.topic, "err", err)
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// Push sends event on the topic and returns the reply's response. A refusal
// is a *[ChannelError].
func (sub *Subscription) Push(ctx context.Context, event string, payload any) (json.RawMessage, error) {
	return sub.push(ctx, event, payload, 0)
}

func (sub *Subscription) push(ctx context.Context, event string, payload any, timeout time.Duration) (json.RawMessage, error) {
	s := sub.s
	s.mu.Lock()
	joined, ref := sub.joined, sub.joinRef
	s.mu.Unlock()
	if !joined {
		return nil, fmt.Errorf("%w: %s", ErrNotJoined, sub.topic)
	}
	if payload == nil {
		payload = struct{}{}
	}
	return s.request(ctx, sub.topic, event, payload, ref, "", timeout, nil)
}

// Ping sends the channel ping. On orders with cancel-on-disconnect it resets
// the deadline; the stream already pings on a timer.
func (sub *Subscription) Ping(ctx context.Context) error {
	_, err := sub.Push(ctx, "ping", nil)
	return err
}

// setPayload records a filter so a rejoin keeps it.
func (sub *Subscription) setPayload(key string, value any) {
	sub.s.mu.Lock()
	sub.payload[key] = value
	sub.s.mu.Unlock()
}

func listOrNil(v []string) any {
	if v == nil {
		return nil
	}
	return append([]string(nil), v...)
}

// SelectMarketIDs changes the market filter without rejoining, on orderbook,
// market_stats and the account channels. nil clears it on the account
// channels; orderbook and market_stats need at least one id.
func (sub *Subscription) SelectMarketIDs(ctx context.Context, marketIDs []string) (json.RawMessage, error) {
	v := listOrNil(marketIDs)
	r, err := sub.Push(ctx, "select_market_ids", map[string]any{"market_ids": v})
	if err == nil {
		sub.setPayload("market_ids", v)
	}
	return r, err
}

// SelectEventIDs changes which events stream on the events channel. At least
// one id is required.
func (sub *Subscription) SelectEventIDs(ctx context.Context, eventIDs []string) (json.RawMessage, error) {
	v := listOrNil(eventIDs)
	r, err := sub.Push(ctx, "select_event_ids", map[string]any{"event_ids": v})
	if err == nil {
		sub.setPayload("event_ids", v)
	}
	return r, err
}

// SelectFilters changes the filters on ticker ("sports", "competitions") and
// trades ("market_ids", "event_ids") without rejoining. A nil list clears
// that filter.
func (sub *Subscription) SelectFilters(ctx context.Context, filters map[string][]string) (json.RawMessage, error) {
	payload := make(map[string]any, len(filters))
	for k, v := range filters {
		payload[k] = listOrNil(v)
	}
	r, err := sub.Push(ctx, "select_filters", payload)
	if err == nil {
		for k, v := range payload {
			sub.setPayload(k, v)
		}
	}
	return r, err
}

// SelectRuleFilters changes the rules filter on markets; nil turns it off.
func (sub *Subscription) SelectRuleFilters(ctx context.Context, rules []string) (json.RawMessage, error) {
	v := listOrNil(rules)
	r, err := sub.Push(ctx, "select_rule_filters", map[string]any{"rule_filters": v})
	if err == nil {
		sub.setPayload("rule_filters", v)
	}
	return r, err
}

// SelectMessageTypes picks "market_created", "market_updated" or both on
// markets; nil means both.
func (sub *Subscription) SelectMessageTypes(ctx context.Context, types []string) (json.RawMessage, error) {
	v := listOrNil(types)
	r, err := sub.Push(ctx, "select_message_types", map[string]any{"message_types": v})
	if err == nil {
		sub.setPayload("message_types", v)
	}
	return r, err
}

// Watch adds markets to a market_updates subscription. Watches are sent
// again after every rejoin.
func (sub *Subscription) Watch(ctx context.Context, marketIDs ...string) (json.RawMessage, error) {
	list := append([]string{}, marketIDs...)
	r, err := sub.Push(ctx, "watch", list)
	if err == nil {
		sub.s.mu.Lock()
		for _, id := range list {
			if !containsString(sub.watches, id) {
				sub.watches = append(sub.watches, id)
			}
		}
		sub.s.mu.Unlock()
	}
	return r, err
}

// RequestSnapshot asks a market:<id> subscription to send the market's
// current "market_update" and "order_book_update" again now.
func (sub *Subscription) RequestSnapshot(ctx context.Context) error {
	// The exchange answers with the pushes, not a reply.
	return sub.cast(ctx, "request_snapshot", struct{}{})
}

// cast sends an event that the exchange answers with pushes rather than a
// reply, so nothing waits for one.
func (sub *Subscription) cast(ctx context.Context, event string, payload any) error {
	s := sub.s
	s.mu.Lock()
	joined, joinRef, conn, closed := sub.joined, sub.joinRef, s.conn, s.closed
	s.mu.Unlock()
	switch {
	case closed:
		return ErrStreamClosed
	case !joined:
		return fmt.Errorf("%w: %s", ErrNotJoined, sub.topic)
	case conn == nil:
		return fmt.Errorf("%w: socket is not connected", ErrTransport)
	}
	if err := s.write(ctx, conn, &joinRef, s.nextRef(), sub.topic, event, payload); err != nil {
		return fmt.Errorf("%w: %v", ErrTransport, err)
	}
	return nil
}

// RequestSeries fetches price history at r for these markets on a
// market_stats subscription, without changing what streams.
func (sub *Subscription) RequestSeries(ctx context.Context, r StatsRange, marketIDs ...string) (*MarketStatsReply, error) {
	if r == "" {
		r = StatsAll
	}
	raw, err := sub.Push(ctx, "request_series", map[string]any{"market_ids": append([]string{}, marketIDs...), "range": string(r)})
	if err != nil {
		return nil, err
	}
	var out MarketStatsReply
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// channelName returns the topic without the user id.
func channelName(topic string) string {
	name, _, _ := strings.Cut(topic, ":")
	return name
}
