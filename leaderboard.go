package stx

import "context"

// LeaderboardService reads the public leaderboard and your standing. Use it
// through [Client.Leaderboard]. Where the exchange has no leaderboard, its
// methods return an error matching [ErrNotAvailable].
type LeaderboardService struct{ c *Client }

// LeaderboardQuery selects a board.
type LeaderboardQuery struct {
	// Period is "daily", "weekly", "monthly", "yearly" or "all".
	Period string
	// Category is "all" or a sport key.
	Category string
	// Metric is "volume", "profit", "predictions", "markets", "win_rate",
	// "biggest_win", "return" or "streak". Not used by Me.
	Metric string
	// Limit is the number of rows. Not used by Me.
	Limit int
}

// Get returns ranked members for a period, sport and metric (GET
// /api/v1/leaderboard). Rows carry a public handle and avatar, never an
// account id.
func (s *LeaderboardService) Get(ctx context.Context, q *LeaderboardQuery) (*Leaderboard, error) {
	var p params
	if q != nil {
		p.str("period", q.Period)
		p.str("category", q.Category)
		p.str("metric", q.Metric)
		p.integer("limit", int64(q.Limit))
	}
	return doOne[*Leaderboard](ctx, s.c, "leaderboard_get", callArgs{query: p})
}

// Me returns your own standing, including your rank outside the shown list
// (GET /api/v1/leaderboard/me). Only Period and Category are used.
func (s *LeaderboardService) Me(ctx context.Context, q *LeaderboardQuery) (*LeaderboardStanding, error) {
	var p params
	if q != nil {
		p.str("period", q.Period)
		p.str("category", q.Category)
	}
	return doOne[*LeaderboardStanding](ctx, s.c, "leaderboard_me_get", callArgs{query: p})
}
