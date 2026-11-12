package stx

import (
	"context"
	"iter"
)

// Page is one page of a list endpoint. Cursor is the opaque value to pass
// back for the next page; it is empty on the last page.
type Page[T any] struct {
	Items  []T
	Cursor string
}

// HasMore reports whether another page exists.
func (p *Page[T]) HasMore() bool { return p != nil && p.Cursor != "" }

// pageFunc fetches the page starting at cursor ("" for the first).
type pageFunc[T any] func(ctx context.Context, cursor string) (*Page[T], error)

// all walks every page, yielding each item. On an error it yields the zero
// value and the error once, and stops.
func all[T any](ctx context.Context, fetch pageFunc[T]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		cursor := ""
		for {
			page, err := fetch(ctx, cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range page.Items {
				if !yield(item, nil) {
					return
				}
			}
			// Stop on the last page, and on a cursor that does not move, which
			// would otherwise loop forever.
			if !page.HasMore() || page.Cursor == cursor {
				return
			}
			cursor = page.Cursor
		}
	}
}
