package ticket

import (
	"context"
	"fmt"
)

// StaticSource serves a single pre-baked ticket without any external
// dependencies. It is used by the demo command to provide a ticket
// without requiring credentials or network access.
type StaticSource struct {
	ticket Ticket
}

// NewStaticSource creates a StaticSource that serves the given ticket.
// Returns an error if the ticket key is empty.
func NewStaticSource(t Ticket) (*StaticSource, error) {
	if t.Key == "" {
		return nil, fmt.Errorf("ticket: static source requires a non-empty key")
	}
	return &StaticSource{ticket: t}, nil
}

// Fetch returns the pre-baked ticket when the key matches, or an error
// if the requested key does not match the embedded ticket.
func (s *StaticSource) Fetch(_ context.Context, key string) (*Ticket, error) {
	if key != s.ticket.Key {
		return nil, fmt.Errorf("ticket: static source has no ticket with key %q", key)
	}
	// Return a copy to prevent mutation.
	t := s.ticket
	return &t, nil
}

// List returns a single-element slice containing the pre-baked ticket.
// The query parameter is ignored.
func (s *StaticSource) List(_ context.Context, _ string) ([]Ticket, error) {
	return []Ticket{s.ticket}, nil
}

// Verify StaticSource satisfies Source interface at compile time.
var _ Source = (*StaticSource)(nil)
