package stx

import (
	"context"
	"encoding/json"
)

// OrderSlip costs orders before you place them, on order_slip:<user_id>.
// Register an order with Add and the exchange sends what it would cost (risk,
// fee, how much would fill now and at which prices) as an
// "order_numbers_batch", then again every time the book moves. Nothing is
// placed. Get one with [Stream.OrderSlip].
//
// Entries live as long as the topic: after a reconnect the stream rejoins,
// but you add your entries again. The protocol is at
// https://docs.stxapp.io/websockets/channels/order-slip/.
type OrderSlip struct{ sub *Subscription }

// OrderSlip joins order_slip:<user_id>. ctx bounds the join and the slip's
// life.
func (s *Stream) OrderSlip(ctx context.Context) (*OrderSlip, error) {
	sub, err := s.subscribe(ctx, accountChannel("order_slip", map[string]any{}))
	if err != nil {
		return nil, err
	}
	return &OrderSlip{sub: sub}, nil
}

// OrderSlipEntry is an order to cost.
type OrderSlipEntry struct {
	MarketID string `json:"market_id"`
	// Side is "buy" or "sell".
	Side string `json:"side"`
	// Quantity is the contracts you are considering.
	Quantity Decimal `json:"qty"`
	// MaxPrice is the market's max price in dollars, for example "1".
	MaxPrice Decimal `json:"max_price"`
	// LimitPrice is your limit in dollars; leave it empty for a market order.
	LimitPrice Decimal `json:"limit_price,omitempty"`
}

// OrderSlipRef identifies a registered entry. SelfMatch is true when one of
// your own open orders is in the way, so placing it would be refused.
type OrderSlipRef struct {
	Ref       string `json:"ref"`
	SelfMatch bool   `json:"self_match"`
}

// OrderSlipFill is one price level of an entry's immediate fill.
type OrderSlipFill struct {
	Price    Decimal `json:"price"`
	Quantity Decimal `json:"qty"`
	Fee      Decimal `json:"fee"`
}

// OrderNumbers is one entry's projected cost.
type OrderNumbers struct {
	Ref         string          `json:"ref"`
	MarketID    string          `json:"market_id"`
	SelfMatch   bool            `json:"self_match"`
	Risk        Decimal         `json:"risk"`
	Fee         Decimal         `json:"fee"`
	RiskWithFee Decimal         `json:"risk_with_fee"`
	FillQty     Decimal         `json:"fill_qty"`
	UnfilledQty Decimal         `json:"unfilled_qty"`
	FillLevels  []OrderSlipFill `json:"est_fill_at_price"`
}

// OrderNumbersBatch is "order_numbers_batch": every entry whose numbers
// changed.
type OrderNumbersBatch struct {
	Updates []OrderNumbers `json:"updates"`
}

// SelfMatchBatch is "self_match_batch": entries whose self-match flag
// flipped.
type SelfMatchBatch struct {
	Updates []OrderSlipRef `json:"updates"`
}

// Add registers an entry and returns its ref. Its first numbers arrive on
// [OrderSlip.Messages] at once.
func (o *OrderSlip) Add(ctx context.Context, e OrderSlipEntry) (OrderSlipRef, error) {
	var out OrderSlipRef
	raw, err := o.sub.Push(ctx, "add_order", e)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

// Remove unregisters an entry by its ref.
func (o *OrderSlip) Remove(ctx context.Context, ref string) error {
	_, err := o.sub.Push(ctx, "remove_order", map[string]string{"ref": ref})
	return err
}

// Messages returns the channel the projections arrive on, with Data a
// *[OrderNumbersBatch] or a *[SelfMatchBatch].
func (o *OrderSlip) Messages() <-chan Message { return o.sub.Messages() }

// Topic returns the wire topic.
func (o *OrderSlip) Topic() string { return o.sub.topic }

// Done is closed when the slip ends.
func (o *OrderSlip) Done() <-chan struct{} { return o.sub.Done() }

// Err returns why the slip ended, like [Subscription.Err].
func (o *OrderSlip) Err() error { return o.sub.Err() }

// Close leaves the topic; every entry goes with it.
func (o *OrderSlip) Close() error { return o.sub.Close() }
