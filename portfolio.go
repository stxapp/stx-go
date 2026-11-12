package stx

import (
	"context"
	"iter"
)

// PortfolioService reads fills, settlements, per-market stats and money
// movements. Use it through [Client.Portfolio].
type PortfolioService struct{ c *Client }

// FillsQuery filters [PortfolioService.Fills].
type FillsQuery struct {
	MarketIDs []string
	OrderIDs  []string
	// Status is "created", "open", "settled" or "cancelled".
	Status string
	Limit  int
	Cursor string
}

func (q *FillsQuery) params(cursor string) params {
	var p params
	if q == nil {
		q = &FillsQuery{}
	}
	p.list("market_ids", q.MarketIDs)
	p.list("order_ids", q.OrderIDs)
	p.str("status", q.Status)
	pageQuery(&p, q.Limit, cursor)
	return p
}

// Fills returns one page of your executions (GET /api/v1/fills).
func (s *PortfolioService) Fills(ctx context.Context, q *FillsQuery) (*Page[Fill], error) {
	var cursor string
	if q != nil {
		cursor = q.Cursor
	}
	return doPage[Fill](ctx, s.c, "fills_get", q.params(cursor))
}

// AllFills returns every fill matching q, following the cursor.
func (s *PortfolioService) AllFills(ctx context.Context, q *FillsQuery) iter.Seq2[Fill, error] {
	return all(ctx, func(ctx context.Context, cursor string) (*Page[Fill], error) {
		return doPage[Fill](ctx, s.c, "fills_get", q.params(cursor))
	})
}

// SettlementsQuery filters [PortfolioService.Settlements].
type SettlementsQuery struct {
	MarketIDs []string
	// Type is "closed_short", "closed_long", "expired_short" or
	// "expired_long".
	Type   string
	Limit  int
	Cursor string
}

func (q *SettlementsQuery) params(cursor string) params {
	var p params
	if q == nil {
		q = &SettlementsQuery{}
	}
	p.list("market_ids", q.MarketIDs)
	p.str("type", q.Type)
	pageQuery(&p, q.Limit, cursor)
	return p
}

// Settlements returns one page of settlements on your account (GET
// /api/v1/portfolio/settlements).
func (s *PortfolioService) Settlements(ctx context.Context, q *SettlementsQuery) (*Page[Settlement], error) {
	var cursor string
	if q != nil {
		cursor = q.Cursor
	}
	return doPage[Settlement](ctx, s.c, "portfolio_settlements_get", q.params(cursor))
}

// AllSettlements returns every settlement matching q, following the cursor.
func (s *PortfolioService) AllSettlements(ctx context.Context, q *SettlementsQuery) iter.Seq2[Settlement, error] {
	return all(ctx, func(ctx context.Context, cursor string) (*Page[Settlement], error) {
		return doPage[Settlement](ctx, s.c, "portfolio_settlements_get", q.params(cursor))
	})
}

// MarketStatsQuery filters [PortfolioService.MarketStats].
type MarketStatsQuery struct {
	MarketIDs              []string
	EventIDs               []string
	ExcludeZeroSettlements *bool
	// FromTime and ToTime bound the window, as Unix timestamps.
	FromTime     int64
	ToTime       int64
	Sports       []string
	Competitions []string
	Limit        int
	Cursor       string
}

func (q *MarketStatsQuery) params(cursor string) params {
	var p params
	if q == nil {
		q = &MarketStatsQuery{}
	}
	p.list("market_ids", q.MarketIDs)
	p.list("event_ids", q.EventIDs)
	p.boolean("exclude_zero_settlements", q.ExcludeZeroSettlements)
	p.integer("from_time", q.FromTime)
	p.integer("to_time", q.ToTime)
	p.list("sports", q.Sports)
	p.list("competitions", q.Competitions)
	pageQuery(&p, q.Limit, cursor)
	return p
}

// MarketStats returns one page of your exposure and P&L per market (GET
// /api/v1/account/market_stats). This is not the market_stats channel,
// which carries price history.
func (s *PortfolioService) MarketStats(ctx context.Context, q *MarketStatsQuery) (*Page[MarketStat], error) {
	var cursor string
	if q != nil {
		cursor = q.Cursor
	}
	return doPage[MarketStat](ctx, s.c, "account_market_stats_get", q.params(cursor))
}

// AllMarketStats returns every per-market stat row matching q, following
// the cursor.
func (s *PortfolioService) AllMarketStats(ctx context.Context, q *MarketStatsQuery) iter.Seq2[MarketStat, error] {
	return all(ctx, func(ctx context.Context, cursor string) (*Page[MarketStat], error) {
		return doPage[MarketStat](ctx, s.c, "account_market_stats_get", q.params(cursor))
	})
}

// PageQuery is the paging for the history lists that take no filters.
type PageQuery struct {
	Limit  int
	Cursor string
}

func (q *PageQuery) params(cursor string) params {
	var p params
	if q == nil {
		q = &PageQuery{}
	}
	pageQuery(&p, q.Limit, cursor)
	return p
}

func (q *PageQuery) cursor() string {
	if q == nil {
		return ""
	}
	return q.Cursor
}

func historyPage[T any](ctx context.Context, c *Client, opID string, q *PageQuery) (*Page[T], error) {
	return doPage[T](ctx, c, opID, q.params(q.cursor()))
}

func historyAll[T any](ctx context.Context, c *Client, opID string, q *PageQuery) iter.Seq2[T, error] {
	return all(ctx, func(ctx context.Context, cursor string) (*Page[T], error) {
		return doPage[T](ctx, c, opID, q.params(cursor))
	})
}

// Deposits returns one page of deposits (GET /api/v1/portfolio/deposits).
func (s *PortfolioService) Deposits(ctx context.Context, q *PageQuery) (*Page[PaymentTransaction], error) {
	return historyPage[PaymentTransaction](ctx, s.c, "portfolio_deposits_get", q)
}

// AllDeposits returns every deposit, following the cursor.
func (s *PortfolioService) AllDeposits(ctx context.Context, q *PageQuery) iter.Seq2[PaymentTransaction, error] {
	return historyAll[PaymentTransaction](ctx, s.c, "portfolio_deposits_get", q)
}

// Withdrawals returns one page of withdrawals (GET
// /api/v1/portfolio/withdrawals).
func (s *PortfolioService) Withdrawals(ctx context.Context, q *PageQuery) (*Page[PaymentTransaction], error) {
	return historyPage[PaymentTransaction](ctx, s.c, "portfolio_withdrawals_get", q)
}

// AllWithdrawals returns every withdrawal, following the cursor.
func (s *PortfolioService) AllWithdrawals(ctx context.Context, q *PageQuery) iter.Seq2[PaymentTransaction, error] {
	return historyAll[PaymentTransaction](ctx, s.c, "portfolio_withdrawals_get", q)
}

// Adjustments returns one page of manual balance adjustments (GET
// /api/v1/portfolio/adjustments).
func (s *PortfolioService) Adjustments(ctx context.Context, q *PageQuery) (*Page[PaymentTransaction], error) {
	return historyPage[PaymentTransaction](ctx, s.c, "portfolio_adjustments_get", q)
}

// AllAdjustments returns every adjustment, following the cursor.
func (s *PortfolioService) AllAdjustments(ctx context.Context, q *PageQuery) iter.Seq2[PaymentTransaction, error] {
	return historyAll[PaymentTransaction](ctx, s.c, "portfolio_adjustments_get", q)
}

// Fees returns one page of fee and fee-refund entries (GET
// /api/v1/portfolio/fees).
func (s *PortfolioService) Fees(ctx context.Context, q *PageQuery) (*Page[FeeTransaction], error) {
	return historyPage[FeeTransaction](ctx, s.c, "portfolio_fees_get", q)
}

// AllFees returns every fee entry, following the cursor.
func (s *PortfolioService) AllFees(ctx context.Context, q *PageQuery) iter.Seq2[FeeTransaction, error] {
	return historyAll[FeeTransaction](ctx, s.c, "portfolio_fees_get", q)
}

// Loyalty returns one page of loyalty entries (GET
// /api/v1/portfolio/loyalty). Where the exchange has no loyalty program it
// returns an error matching [ErrNotAvailable].
func (s *PortfolioService) Loyalty(ctx context.Context, q *PageQuery) (*Page[Transaction], error) {
	return historyPage[Transaction](ctx, s.c, "portfolio_loyalty_get", q)
}

// AllLoyalty returns every loyalty entry, following the cursor.
func (s *PortfolioService) AllLoyalty(ctx context.Context, q *PageQuery) iter.Seq2[Transaction, error] {
	return historyAll[Transaction](ctx, s.c, "portfolio_loyalty_get", q)
}
