package stx

// Bool returns a pointer to v, for the tri-state filters and fields such as
// MarketsQuery.Trading and OrderRequest.CancelOnDisconnect.
func Bool(v bool) *bool { return &v }

func pageQuery(q *params, limit int, cursor string) {
	q.integer("limit", int64(limit))
	q.str("cursor", cursor)
}
