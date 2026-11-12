package stx

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
)

// MarketsService lists markets and market types, and creates markets. Use it
// through [Client.Markets].
type MarketsService struct{ c *Client }

// MarketsQuery filters [MarketsService.List]. Filters combine with AND; a
// list filter matches any of its values.
type MarketsQuery struct {
	MarketIDs []string
	EventIDs  []string
	// Status is one or more market statuses, such as "pre_open" and "open".
	Status []string
	// Trading true lists only markets accepting orders now; false only those
	// that are not.
	Trading      *bool
	Sports       []string
	Competitions []string
	// SortBy is "event_start"; SortDirection is "asc" or "desc".
	SortBy        string
	SortDirection string
	// Limit is the page size: default 100, maximum 200.
	Limit  int
	Cursor string
}

func (q *MarketsQuery) params(cursor string) params {
	var p params
	if q == nil {
		q = &MarketsQuery{}
	}
	p.list("market_ids", q.MarketIDs)
	p.list("event_ids", q.EventIDs)
	p.list("status", q.Status)
	p.boolean("trading", q.Trading)
	p.list("sports", q.Sports)
	p.list("competitions", q.Competitions)
	p.str("sort_by[name]", q.SortBy)
	p.str("sort_by[direction]", q.SortDirection)
	pageQuery(&p, q.Limit, cursor)
	return p
}

// List returns one page of markets (GET /api/v1/markets).
func (s *MarketsService) List(ctx context.Context, q *MarketsQuery) (*Page[Market], error) {
	return doPage[Market](ctx, s.c, "markets_get", q.params(cursorOf(q)))
}

// All returns every market matching q, following the cursor. q.Cursor is
// ignored.
func (s *MarketsService) All(ctx context.Context, q *MarketsQuery) iter.Seq2[Market, error] {
	return all(ctx, func(ctx context.Context, cursor string) (*Page[Market], error) {
		return doPage[Market](ctx, s.c, "markets_get", q.params(cursor))
	})
}

func cursorOf(q *MarketsQuery) string {
	if q == nil {
		return ""
	}
	return q.Cursor
}

// Get returns one market by id. It returns an error matching [ErrNotFound]
// when there is none.
func (s *MarketsService) Get(ctx context.Context, marketID string) (*Market, error) {
	page, err := s.List(ctx, &MarketsQuery{MarketIDs: []string{marketID}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return nil, &APIError{StatusCode: 404, Method: "GET", Path: "/api/v1/markets", Message: fmt.Sprintf("market %s not found", marketID)}
	}
	return &page.Items[0], nil
}

// MarketTypeField is one field a market entry takes besides its rule.
type MarketTypeField struct {
	// Type is its JSON type, such as "string" or "number".
	Type        string `json:"type"`
	Description string `json:"description"`
}

// EventStat is a stat a market can be created on.
type EventStat struct {
	// Stat is the key to send, such as "POINTS".
	Stat string `json:"stat"`
	// Name is the display name, such as "Points".
	Name string `json:"name"`
}

// MarketTypeEventType is the stats one event type offers.
type MarketTypeEventType struct {
	Sport string      `json:"sport"`
	Stats []EventStat `json:"stats"`
}

// MarketType is one market rule. PartnerCreate says whether
// [MarketsService.Create] takes it; for those, Fields and EventTypes
// describe an entry, and for the others EventTypeNames lists where the rule
// runs.
type MarketType struct {
	PartnerCreate bool
	// Fields are the fields an entry takes besides its rule, by wire name.
	Fields map[string]MarketTypeField
	// EventTypes are the event types the rule runs on, keyed by event type
	// such as "NFL", with their stats.
	EventTypes map[string]MarketTypeEventType
	// EventTypeNames lists the event types of a rule that cannot be created.
	EventTypeNames []string
}

// UnmarshalJSON reads either shape of a market type.
func (m *MarketType) UnmarshalJSON(data []byte) error {
	var raw struct {
		PartnerCreate bool                       `json:"partner_create"`
		Fields        map[string]MarketTypeField `json:"fields"`
		EventTypes    json.RawMessage            `json:"event_types"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = MarketType{PartnerCreate: raw.PartnerCreate, Fields: raw.Fields}
	if len(raw.EventTypes) > 0 && raw.EventTypes[0] == '[' {
		return json.Unmarshal(raw.EventTypes, &m.EventTypeNames)
	}
	if len(raw.EventTypes) > 0 && raw.EventTypes[0] == '{' {
		return json.Unmarshal(raw.EventTypes, &m.EventTypes)
	}
	return nil
}

// MarketTypes are the market rules, keyed by rule such as
// "player_stat_line".
type MarketTypes map[string]MarketType

// Types returns the market types (GET /api/v1/types). It changes only when
// STX adds a rule or a stat, so read it once and keep it. It needs an API
// key or an OAuth app token with the events scope, and returns an error
// matching [ErrNotAvailable] where the exchange does not have it.
func (s *MarketsService) Types(ctx context.Context) (MarketTypes, error) {
	return doOne[MarketTypes](ctx, s.c, "types_get", callArgs{})
}
