package ticket

import (
	"context"
	"testing"
)

func TestStaticSource_Fetch(t *testing.T) {
	src, err := NewStaticSource(Ticket{
		Key:     "DEMO-1",
		Summary: "Fix handler status code",
		Type:    "bug",
	})
	if err != nil {
		t.Fatalf("NewStaticSource: %v", err)
	}

	ctx := context.Background()

	ticket, err := src.Fetch(ctx, "DEMO-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ticket.Key != "DEMO-1" {
		t.Errorf("Key = %q, want %q", ticket.Key, "DEMO-1")
	}
	if ticket.Summary != "Fix handler status code" {
		t.Errorf("Summary = %q, want %q", ticket.Summary, "Fix handler status code")
	}
	if ticket.Type != "bug" {
		t.Errorf("Type = %q, want %q", ticket.Type, "bug")
	}
}

func TestStaticSource_Fetch_WrongKey(t *testing.T) {
	src, err := NewStaticSource(Ticket{
		Key:     "DEMO-1",
		Summary: "Fix handler status code",
	})
	if err != nil {
		t.Fatalf("NewStaticSource: %v", err)
	}

	ctx := context.Background()

	_, err = src.Fetch(ctx, "WRONG-KEY")
	if err == nil {
		t.Fatal("Fetch should fail with wrong key")
	}
}

func TestStaticSource_List(t *testing.T) {
	src, err := NewStaticSource(Ticket{
		Key:     "DEMO-1",
		Summary: "Fix handler status code",
	})
	if err != nil {
		t.Fatalf("NewStaticSource: %v", err)
	}

	ctx := context.Background()

	tickets, err := src.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tickets) != 1 {
		t.Fatalf("List returned %d tickets, want 1", len(tickets))
	}
	if tickets[0].Key != "DEMO-1" {
		t.Errorf("tickets[0].Key = %q, want %q", tickets[0].Key, "DEMO-1")
	}
}

func TestStaticSource_EmptyKey(t *testing.T) {
	_, err := NewStaticSource(Ticket{Summary: "no key"})
	if err == nil {
		t.Fatal("NewStaticSource should fail with empty key")
	}
}

func TestStaticSource_FetchReturnsCopy(t *testing.T) {
	src, err := NewStaticSource(Ticket{
		Key:     "DEMO-1",
		Summary: "Original summary",
	})
	if err != nil {
		t.Fatalf("NewStaticSource: %v", err)
	}

	ctx := context.Background()

	ticket1, err := src.Fetch(ctx, "DEMO-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Mutate the returned ticket.
	ticket1.Summary = "Mutated"

	// Second fetch should still return the original.
	ticket2, err := src.Fetch(ctx, "DEMO-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ticket2.Summary != "Original summary" {
		t.Errorf("Summary = %q, want %q (mutation leaked)", ticket2.Summary, "Original summary")
	}
}
