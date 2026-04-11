package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Silence unused import errors — these are used by later tasks.
var (
	_ = json.Marshal
	_ = errors.Is
	_ = fmt.Sprintf
	_ = strings.Contains
)

func TestLoadOrCreate(t *testing.T) {
	t.Run("creates_new_state", func(t *testing.T) {
		dir := t.TempDir()

		state, err := LoadOrCreate(dir, "PROJ-123")
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}

		if state.ticket != "PROJ-123" {
			t.Errorf("ticket = %q, want %q", state.ticket, "PROJ-123")
		}
		if state.meta.Ticket != "PROJ-123" {
			t.Errorf("meta.Ticket = %q, want %q", state.meta.Ticket, "PROJ-123")
		}
		if state.meta.StartedAt.IsZero() {
			t.Error("StartedAt should be set")
		}
		if state.meta.Phases == nil {
			t.Error("Phases should be initialized")
		}

		// Directory structure should exist
		stateDir := filepath.Join(dir, "PROJ-123")
		if _, err := os.Stat(stateDir); err != nil {
			t.Errorf("state dir should exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "logs")); err != nil {
			t.Errorf("logs dir should exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "meta.json")); err != nil {
			t.Errorf("meta.json should exist: %v", err)
		}
	})

	t.Run("resumes_existing_state", func(t *testing.T) {
		dir := t.TempDir()

		// Create initial state
		state1, err := LoadOrCreate(dir, "PROJ-456")
		if err != nil {
			t.Fatal(err)
		}
		originalStartedAt := state1.meta.StartedAt

		// Load again — should resume, not overwrite
		state2, err := LoadOrCreate(dir, "PROJ-456")
		if err != nil {
			t.Fatalf("resume LoadOrCreate: %v", err)
		}
		if !state2.meta.StartedAt.Equal(originalStartedAt) {
			t.Errorf("StartedAt changed on resume: got %v, want %v",
				state2.meta.StartedAt, originalStartedAt)
		}
	})

	t.Run("lock_not_acquired", func(t *testing.T) {
		dir := t.TempDir()

		state, err := LoadOrCreate(dir, "PROJ-789")
		if err != nil {
			t.Fatal(err)
		}
		if state.lockFd != nil {
			t.Error("lockFd should be nil after LoadOrCreate")
		}
	})
}

func TestValidateTicketKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid_key", "PROJ-123", false},
		{"valid_with_underscores", "MY_PROJECT-42", false},
		{"empty_string", "", true},
		{"contains_slash", "PROJ/123", true},
		{"contains_backslash", "PROJ\\123", true},
		{"contains_dotdot", "PROJ..123", true},
		{"path_traversal", "../etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTicketKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTicketKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestLoadOrCreate_InvalidKey(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadOrCreate(dir, "../evil")
	if err == nil {
		t.Fatal("expected error for invalid ticket key")
	}
}
