package stx

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// terminalOrderStatuses are the statuses after which an order is not open.
var terminalOrderStatuses = map[string]bool{"filled": true, "cancelled": true, "partially_cancelled": true, "rejected": true}

// AccountChangeKind says which part of an [AccountView] a message touched.
type AccountChangeKind string

// The parts of an account view.
const (
	AccountBalance   AccountChangeKind = "balance"
	AccountOrders    AccountChangeKind = "orders"
	AccountFills     AccountChangeKind = "fills"
	AccountPositions AccountChangeKind = "positions"
	AccountPayment   AccountChangeKind = "payment"
)

// AccountChange is one message an [AccountView] applied.
type AccountChange struct {
	Kind AccountChangeKind
	// Event is the wire event, for example "update" or "new_open_order".
	Event string
	// Snapshot is true for a join snapshot (the first seed, or after a
	// rejoin): that part was replaced, not patched.
	Snapshot bool
	// Data is the message's typed payload, as on [Message].
	Data any
}

// AccountState is an [AccountView]'s state at one moment.
type AccountState struct {
	// Balance is the latest balance summary, or nil before the first.
	Balance *Balance
	// OpenOrders are your open orders, newest first.
	OpenOrders []Order
	// Fills are your fills, newest first.
	Fills []Fill
	// Positions are your positions, one per market.
	Positions []Position
}

// AccountViewOptions configures [Stream.AccountView].
type AccountViewOptions struct {
	// MarketIDs keeps orders, fills and positions to these markets. The
	// balance is always the whole account.
	MarketIDs []string
	// AccountID picks one of your accounts for the balance; empty means the
	// account the stream signed in as.
	AccountID string
	// KeepFlatPositions keeps positions whose quantity is zero.
	KeepFlatPositions bool
	// MaxFills is the most fills kept; the oldest go first. Default 1000.
	MaxFills int
	// SnapshotTimeout bounds the wait for the four join snapshots. Default
	// 15s.
	SnapshotTimeout time.Duration
}

// AccountView is a live copy of your account, kept by the stream. It
// subscribes to balances, orders, fills and positions, seeds each part from
// the topic's join snapshot, and applies every update the exchange sends.
// After a reconnect the snapshots arrive again and replace each part. No
// REST call is made to keep it current.
//
// Read it with Snapshot whenever you like, or wait on Changes.
type AccountView struct {
	s       *Stream
	cancel  context.CancelFunc
	subs    [4]*Subscription
	changes chan AccountChange
	done    chan struct{}
	ready   chan struct{}

	keepFlat bool
	maxFills int

	mu        sync.Mutex
	balance   *Balance
	orders    map[string]Order
	fills     map[string]Fill
	positions map[string]Position
	seeds     map[AccountChangeKind]int
	closed    bool
	err       error
}

// AccountView subscribes to your account and returns once the four join
// snapshots have arrived. ctx bounds that wait and the view's life.
func (s *Stream) AccountView(ctx context.Context, opts AccountViewOptions) (*AccountView, error) {
	vctx, cancel := context.WithCancel(ctx)
	v := &AccountView{
		s:         s,
		cancel:    cancel,
		changes:   make(chan AccountChange, s.cfg.buffer),
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
		keepFlat:  opts.KeepFlatPositions,
		maxFills:  opts.MaxFills,
		orders:    map[string]Order{},
		fills:     map[string]Fill{},
		positions: map[string]Position{},
		seeds:     map[AccountChangeKind]int{},
	}
	if v.maxFills <= 0 {
		v.maxFills = 1000
	}
	channels := []Channel{
		BalancesChannel(opts.AccountID),
		OrdersChannel(OrdersFilter{MarketIDs: opts.MarketIDs}),
		FillsChannel(opts.MarketIDs...),
		PositionsChannel(opts.MarketIDs...),
	}
	for i, ch := range channels {
		sub, err := s.Subscribe(vctx, ch)
		if err != nil {
			cancel()
			for _, prev := range v.subs[:i] {
				_ = prev.Close()
			}
			return nil, err
		}
		v.subs[i] = sub
	}
	s.goTracked(v.run)

	timeout := opts.SnapshotTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-v.ready:
		return v, nil
	case <-timer.C:
		_ = v.Close()
		return nil, fmt.Errorf("%w: the account snapshots did not arrive within %s", ErrTimeout, timeout)
	case <-ctx.Done():
		_ = v.Close()
		return nil, ctx.Err()
	case <-v.done:
		err := v.Err()
		if err == nil {
			err = ErrStreamClosed
		}
		return nil, err
	}
}

func (v *AccountView) run() {
	defer close(v.done)
	defer close(v.changes)
	defer v.cancel()
	chans := [4]<-chan Message{}
	for i, sub := range v.subs {
		chans[i] = sub.Messages()
	}
	open := 4
	for open > 0 {
		var m Message
		var ok bool
		var i int
		select {
		case m, ok = <-chans[0]:
			i = 0
		case m, ok = <-chans[1]:
			i = 1
		case m, ok = <-chans[2]:
			i = 2
		case m, ok = <-chans[3]:
			i = 3
		}
		if !ok {
			if err := v.subs[i].Err(); err != nil {
				v.mu.Lock()
				if v.err == nil && !v.closed {
					v.err = err
				}
				v.mu.Unlock()
			}
			chans[i] = nil
			open--
			// One part gone means the view is no longer whole.
			v.cancel()
			continue
		}
		if c, applied := v.apply(m); applied {
			v.emit(c)
		}
	}
}

func (v *AccountView) emit(c AccountChange) {
	select {
	case v.changes <- c:
		return
	default:
	}
	select {
	case <-v.changes:
	default:
	}
	select {
	case v.changes <- c:
	default:
	}
}

// apply folds one message into the view.
func (v *AccountView) apply(m Message) (AccountChange, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c := AccountChange{Event: m.Event, Snapshot: m.Snapshot, Data: m.Data}
	switch d := m.Data.(type) {
	case *Balance:
		c.Kind = AccountBalance
		b := *d
		v.balance = &b
		if m.Event == "balances" {
			v.seeds[AccountBalance]++
		}
	case *PaymentUpdate:
		c.Kind = AccountPayment
	case *OrdersSnapshot:
		c.Kind = AccountOrders
		v.seeds[AccountOrders]++
		v.orders = map[string]Order{}
		for _, o := range d.Orders {
			v.upsertOrder(o)
		}
	case *Order:
		c.Kind = AccountOrders
		v.upsertOrder(*d)
	case *FillsSnapshot:
		c.Kind = AccountFills
		v.seeds[AccountFills]++
		v.fills = map[string]Fill{}
		for _, f := range d.Trades {
			v.upsertFill(f)
		}
	case *Fill:
		c.Kind = AccountFills
		v.upsertFill(*d)
	case *PositionsSnapshot:
		c.Kind = AccountPositions
		v.seeds[AccountPositions]++
		v.positions = map[string]Position{}
		for _, p := range d.Positions {
			v.upsertPosition(p)
		}
	case *PositionsUpdate:
		c.Kind = AccountPositions
		for _, p := range d.Positions {
			v.upsertPosition(p)
		}
	default:
		return c, false
	}
	if v.readyLocked() {
		select {
		case <-v.ready:
		default:
			close(v.ready)
		}
	}
	return c, true
}

func (v *AccountView) readyLocked() bool {
	return v.seeds[AccountBalance] > 0 && v.seeds[AccountOrders] > 0 && v.seeds[AccountFills] > 0 && v.seeds[AccountPositions] > 0
}

func (v *AccountView) upsertOrder(o Order) {
	if o.ID == "" {
		return
	}
	if terminalOrderStatuses[string(o.Status)] {
		delete(v.orders, o.ID)
		return
	}
	v.orders[o.ID] = o
}

func (v *AccountView) upsertFill(f Fill) {
	if f.TradeID == "" {
		return
	}
	v.fills[f.TradeID] = f
	if len(v.fills) > v.maxFills {
		all := v.sortedFills()
		for _, old := range all[v.maxFills:] {
			delete(v.fills, old.TradeID)
		}
	}
}

func (v *AccountView) upsertPosition(p Position) {
	key := p.MarketID
	if key == "" {
		key = p.ID
	}
	if key == "" {
		return
	}
	if !v.keepFlat && p.Position != "" && p.Position.IsZero() {
		delete(v.positions, key)
		return
	}
	v.positions[key] = p
}

func (v *AccountView) sortedFills() []Fill {
	out := make([]Fill, 0, len(v.fills))
	for _, f := range v.fills {
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return fillStamp(out[i]) > fillStamp(out[j]) })
	return out
}

// Snapshot returns the view's state now. The slices are yours to keep.
func (v *AccountView) Snapshot() AccountState {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := AccountState{Fills: v.sortedFills()}
	if v.balance != nil {
		b := *v.balance
		st.Balance = &b
	}
	st.OpenOrders = make([]Order, 0, len(v.orders))
	for _, o := range v.orders {
		st.OpenOrders = append(st.OpenOrders, o)
	}
	sort.SliceStable(st.OpenOrders, func(i, j int) bool { return orderStamp(st.OpenOrders[i]) > orderStamp(st.OpenOrders[j]) })
	st.Positions = make([]Position, 0, len(v.positions))
	for _, p := range v.positions {
		st.Positions = append(st.Positions, p)
	}
	sort.SliceStable(st.Positions, func(i, j int) bool { return st.Positions[i].MarketID < st.Positions[j].MarketID })
	return st
}

// MarshalJSON writes the current [AccountState].
func (v *AccountView) MarshalJSON() ([]byte, error) { return json.Marshal(v.Snapshot()) }

// Changes returns the channel every applied message is announced on, closed
// when the view ends. When you fall behind, the oldest change is dropped;
// Snapshot is always current.
func (v *AccountView) Changes() <-chan AccountChange { return v.changes }

// Ready reports whether every part has had its join snapshot.
func (v *AccountView) Ready() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.readyLocked()
}

// Done is closed when the view ends.
func (v *AccountView) Done() <-chan struct{} { return v.done }

// Err returns why the view ended, when one of its subscriptions ended with
// an error.
func (v *AccountView) Err() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.err
}

// Close leaves the four topics. The stream stays open.
func (v *AccountView) Close() error {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	for _, sub := range v.subs {
		if sub != nil {
			_ = sub.Close()
		}
	}
	v.cancel()
	return nil
}

// orderStamp and fillStamp order newest first: inserted_at in Unix
// microseconds, else the ISO time.
func orderStamp(o Order) int64 { return stamp(o.InsertedAt, o.Time) }

func fillStamp(f Fill) int64 { return stamp(f.InsertedAt, f.Time) }

func stamp(insertedAt int64, t time.Time) int64 {
	if insertedAt != 0 || t.IsZero() {
		return insertedAt
	}
	return t.UnixMicro()
}
