package stx

import (
	"context"
	"iter"
)

// EventsService lists events and their players. Use it through
// [Client.Events].
type EventsService struct{ c *Client }

// EventsQuery filters [EventsService.List].
type EventsQuery struct {
	EventIDs     []string
	Sports       []string
	Competitions []string
	EventTypes   []string
	Title        string
	// Status is "scheduled", "in_progress", "completed" or "cancelled".
	Status   string
	Promoted *bool
	// SortBy is "start_time"; SortDirection is "asc" or "desc".
	SortBy        string
	SortDirection string
	Limit         int
	Cursor        string
}

func (q *EventsQuery) params(cursor string) params {
	var p params
	if q == nil {
		q = &EventsQuery{}
	}
	p.list("event_ids", q.EventIDs)
	p.list("sports", q.Sports)
	p.list("competitions", q.Competitions)
	p.list("event_types", q.EventTypes)
	p.str("title", q.Title)
	p.str("status", q.Status)
	p.boolean("promoted", q.Promoted)
	p.str("sort_by[name]", q.SortBy)
	p.str("sort_by[direction]", q.SortDirection)
	pageQuery(&p, q.Limit, cursor)
	return p
}

// List returns one page of events (GET /api/v1/events).
func (s *EventsService) List(ctx context.Context, q *EventsQuery) (*Page[Event], error) {
	cursor := ""
	if q != nil {
		cursor = q.Cursor
	}
	return doPage[Event](ctx, s.c, "events_get", q.params(cursor))
}

// All returns every event matching q, following the cursor.
func (s *EventsService) All(ctx context.Context, q *EventsQuery) iter.Seq2[Event, error] {
	return all(ctx, func(ctx context.Context, cursor string) (*Page[Event], error) {
		return doPage[Event](ctx, s.c, "events_get", q.params(cursor))
	})
}

// EventPlayer is one player on an event's roster.
type EventPlayer struct {
	// PlayerID is what a new market names the player by.
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Jersey   string `json:"jersey,omitempty"`
	Position string `json:"position,omitempty"`
}

// EventTeam is one team's roster, sorted by player name.
type EventTeam struct {
	Team    string        `json:"team"`
	Players []EventPlayer `json:"players"`
}

// EventPlayers are an event's rosters and the stats a market on it can use.
type EventPlayers struct {
	EventID string `json:"event_id"`
	// Teams is empty until the rosters are published.
	Teams []EventTeam `json:"teams"`
	// Stats are the stats this event accepts.
	Stats []EventStat `json:"stats"`
}

// Players returns the event's team rosters and the stats a market on it can
// use (GET /api/v1/events/{event_id}/players). It needs an API key or an
// OAuth app token with the events scope, and returns an error matching
// [ErrNotAvailable] where the exchange does not have it.
func (s *EventsService) Players(ctx context.Context, eventID string) (*EventPlayers, error) {
	return doOne[*EventPlayers](ctx, s.c, "events__id_players_get", callArgs{pathParams: map[string]string{"event_id": eventID}})
}
