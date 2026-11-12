package stx

// LeaderboardRank is a rank and the value ranked on, for one leaderboard
// metric. Value is in dollars on money metrics, a count on count metrics and
// a ratio between 0 and 1 on ratio metrics.
type LeaderboardRank struct {
	// Rank is the dense rank, 1-based.
	Rank int64 `json:"rank"`
	// Value is the value ranked on.
	Value Decimal `json:"value"`
}
