package ticket

import "context"

// Source provides read access to tickets from an external tracking system.
type Source interface {
	Fetch(ctx context.Context, key string) (*Ticket, error)
	List(ctx context.Context, query string) ([]Ticket, error)
}
