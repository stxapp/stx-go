package stx

import (
	"context"
	"errors"
	"fmt"
	"iter"
)

// Order actions, types and expirations.
const (
	Buy  = "buy"
	Sell = "sell"

	Limit       = "limit"
	MarketOrder = "market"

	GoodTillStart = "good_till_start"
	GoodTillTime  = "good_till_time"
)

// OrdersService places, lists and cancels orders. Use it through
// [Client.Orders].
type OrdersService struct{ c *Client }

// OrderRequest is one new order.
type OrderRequest struct {
	MarketID string `json:"market_id"`
	// Action is [Buy] or [Sell].
	Action string `json:"action"`
	// OrderType is [Limit] or [MarketOrder].
	OrderType string `json:"order_type"`
	// Quantity is the number of contracts, such as "100". Required.
	Quantity Decimal `json:"quantity"`
	// Price is in dollars, such as "0.58". Required for a limit order.
	Price Decimal `json:"price,omitempty"`
	// ClientOrderID is your own reference, echoed back. Set it so you can
	// look an order up after a timeout: a POST is never retried.
	ClientOrderID string `json:"client_order_id,omitempty"`
	// Expiration is [GoodTillStart] or [GoodTillTime].
	Expiration string `json:"expiration,omitempty"`
	// ExpirationTime is Unix microseconds, with Expiration GoodTillTime.
	ExpirationTime int64 `json:"expiration_time,omitempty"`
	// CancelOnDisconnect cancels the order if the orders channel stops
	// heartbeating.
	CancelOnDisconnect *bool  `json:"cancel_on_disconnect,omitempty"`
	DeviceID           string `json:"device_id,omitempty"`
}

func (o OrderRequest) check() error {
	switch {
	case o.MarketID == "":
		return errors.New("stx: MarketID is required")
	case o.Action == "":
		return errors.New("stx: Action is required")
	case o.OrderType == "":
		return errors.New("stx: OrderType is required")
	case o.Quantity == "":
		return errors.New("stx: Quantity is required")
	case !o.Quantity.Valid():
		return fmt.Errorf("stx: Quantity %q is not a decimal number", o.Quantity)
	case o.Price != "" && !o.Price.Valid():
		return fmt.Errorf("stx: Price %q is not a decimal number", o.Price)
	case o.OrderType == Limit && o.Price == "":
		return errors.New("stx: Price is required for a limit order")
	}
	return nil
}

// Place places one order (POST /api/v1/orders). The exchange validates it:
// a bad price step or a fractional quantity returns an error matching
// [ErrBadRequest] or [ErrRejected] with the API's message.
//
// A POST is never retried after a 5xx or a dropped connection; set
// ClientOrderID and look the order up with [OrdersService.List] if that
// happens.
func (s *OrdersService) Place(ctx context.Context, order OrderRequest) (*Order, error) {
	if err := order.check(); err != nil {
		return nil, err
	}
	return doOne[*Order](ctx, s.c, "orders_post", callArgs{body: order})
}

// BatchOrderResult is one entry of [OrdersService.PlaceBatch]: the placed
// order, or why it was not placed.
type BatchOrderResult struct {
	Order  *Order   `json:"order,omitempty"`
	Errors []string `json:"errors,omitempty"`
}

// OK reports whether the order was placed.
func (r BatchOrderResult) OK() bool { return r.Order != nil && len(r.Errors) == 0 }

// PlaceBatch places several orders in one call (POST
// /api/v1/orders/batched). It returns one result per order, in order; a
// rejected order carries Errors and does not stop the others.
func (s *OrdersService) PlaceBatch(ctx context.Context, orders []OrderRequest) ([]BatchOrderResult, error) {
	for i, o := range orders {
		if err := o.check(); err != nil {
			return nil, fmt.Errorf("order %d: %w", i, err)
		}
	}
	body := struct {
		Orders []OrderRequest `json:"orders"`
	}{orders}
	return doOne[[]BatchOrderResult](ctx, s.c, "orders_batched_post", callArgs{body: body})
}

// Get returns one of your orders (GET /api/v1/orders/{order_id}).
func (s *OrdersService) Get(ctx context.Context, orderID string) (*Order, error) {
	return doOne[*Order](ctx, s.c, "orders__id_get", callArgs{pathParams: map[string]string{"order_id": orderID}})
}

// OrdersQuery filters [OrdersService.List].
type OrdersQuery struct {
	OrderIDs       []string
	ClientOrderIDs []string
	MarketIDs      []string
	// Status is one or more order statuses, such as "accepted", "delayed"
	// and "open".
	Status []string
	Limit  int
	Cursor string
}

func (q *OrdersQuery) params(cursor string) params {
	var p params
	if q == nil {
		q = &OrdersQuery{}
	}
	p.list("order_ids", q.OrderIDs)
	p.list("client_order_ids", q.ClientOrderIDs)
	p.list("market_ids", q.MarketIDs)
	p.list("status", q.Status)
	pageQuery(&p, q.Limit, cursor)
	return p
}

// List returns one page of your orders, newest first (GET /api/v1/orders).
func (s *OrdersService) List(ctx context.Context, q *OrdersQuery) (*Page[Order], error) {
	cursor := ""
	if q != nil {
		cursor = q.Cursor
	}
	return doPage[Order](ctx, s.c, "orders_get", q.params(cursor))
}

// All returns every order matching q, following the cursor.
func (s *OrdersService) All(ctx context.Context, q *OrdersQuery) iter.Seq2[Order, error] {
	return all(ctx, func(ctx context.Context, cursor string) (*Page[Order], error) {
		return doPage[Order](ctx, s.c, "orders_get", q.params(cursor))
	})
}

// Cancel requests a cancel (DELETE /api/v1/orders/{order_id}). A fill
// already in flight can still land.
func (s *OrdersService) Cancel(ctx context.Context, orderID string) (*Cancellation, error) {
	return doOne[*Cancellation](ctx, s.c, "orders__order_id_delete", callArgs{pathParams: map[string]string{"order_id": orderID}})
}

// CancelBatch cancels the named orders (DELETE /api/v1/orders/batched).
func (s *OrdersService) CancelBatch(ctx context.Context, orderIDs []string) ([]Cancellation, error) {
	type ref struct {
		OrderID string `json:"order_id"`
	}
	body := struct {
		Orders []ref `json:"orders"`
	}{}
	for _, id := range orderIDs {
		body.Orders = append(body.Orders, ref{id})
	}
	return doOne[[]Cancellation](ctx, s.c, "orders_batched_delete", callArgs{body: body})
}

// CancelAll cancels every order resting on the account (DELETE
// /api/v1/orders/all).
func (s *OrdersService) CancelAll(ctx context.Context) ([]Cancellation, error) {
	return doOne[[]Cancellation](ctx, s.c, "orders_all_delete", callArgs{})
}
