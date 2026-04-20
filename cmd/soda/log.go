package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log <ticket>",
		Short: "Tail pipeline events for a ticket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			follow, _ := cmd.Flags().GetBool("follow")
			since, _ := cmd.Flags().GetString("since")
			phase, _ := cmd.Flags().GetString("phase")
			lastN, _ := cmd.Flags().GetInt("last")
			return runLog(cmd.OutOrStdout(), cfg.StateDir, args[0], follow, since, phase, lastN)
		},
	}

	cmd.Flags().BoolP("follow", "f", false, "follow new events (tail -f style)")
	cmd.Flags().String("since", "", "show events since duration (e.g. 5m, 1h)")
	cmd.Flags().String("phase", "", "filter events to a specific phase")
	cmd.Flags().IntP("last", "n", 0, "show only the last N events")

	return cmd
}

func runLog(w io.Writer, stateDir, ticketKey string, follow bool, since, phase string, lastN int) error {
	ticketDir := filepath.Join(stateDir, ticketKey)
	eventsPath := filepath.Join(ticketDir, "events.jsonl")

	// Parse --since if provided.
	var sinceTime time.Time
	if since != "" {
		dur, err := time.ParseDuration(since)
		if err != nil {
			return fmt.Errorf("log: invalid --since duration %q: %w", since, err)
		}
		sinceTime = time.Now().Add(-dur)
	}

	if !follow {
		return printEvents(w, eventsPath, sinceTime, phase, lastN)
	}

	return followEvents(w, eventsPath, sinceTime, phase, lastN)
}

// printEvents reads and prints all matching events from the file.
func printEvents(w io.Writer, path string, sinceTime time.Time, phase string, lastN int) error {
	events, err := readEventsFromPath(path)
	if err != nil {
		return err
	}

	filtered := filterEvents(events, sinceTime, phase)

	// Apply --last: show only the last N events.
	if lastN > 0 && len(filtered) > lastN {
		filtered = filtered[len(filtered)-lastN:]
	}

	for _, ev := range filtered {
		fmt.Fprintln(w, pipeline.FormatEvent(ev))
	}
	return nil
}

// followEvents prints existing events then polls for new ones until a
// terminal event is seen or the process receives SIGINT/SIGTERM.
func followEvents(w io.Writer, path string, sinceTime time.Time, phase string, lastN int) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Print existing events first.
	var offset int64
	events, newOffset, err := readEventsFromOffset(path, 0)
	if err != nil {
		// If the file doesn't exist yet in follow mode, start from scratch.
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	offset = newOffset

	filtered := filterEvents(events, sinceTime, phase)
	// Apply --last: show only the last N historical events (tail -f -n semantics).
	if lastN > 0 && len(filtered) > lastN {
		filtered = filtered[len(filtered)-lastN:]
	}
	for _, ev := range filtered {
		fmt.Fprintln(w, pipeline.FormatEvent(ev))
	}
	// Check terminal events on the unfiltered list so that --phase filters
	// cannot mask engine_completed / engine_failed (which have Phase="").
	for _, ev := range events {
		if isTerminalEvent(ev) {
			return nil
		}
	}

	// Poll loop.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			newEvents, newOff, readErr := readEventsFromOffset(path, offset)
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				return readErr
			}
			offset = newOff

			for _, ev := range filterEvents(newEvents, sinceTime, phase) {
				fmt.Fprintln(w, pipeline.FormatEvent(ev))
			}
			// Check terminal events on the unfiltered list.
			for _, ev := range newEvents {
				if isTerminalEvent(ev) {
					return nil
				}
			}
		}
	}
}

// readEventsFromPath reads all events from the file at path.
func readEventsFromPath(path string) ([]pipeline.Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("log: no events file found at %s (run 'soda run <ticket>' first)", path)
		}
		return nil, fmt.Errorf("log: read events: %w", err)
	}
	return parseEventsData(data), nil
}

// readEventsFromOffset reads events from path starting at byte offset.
// Returns the parsed events and the new offset. If the file does not exist,
// returns os.ErrNotExist so the caller can decide how to handle it.
func readEventsFromOffset(path string, offset int64) ([]pipeline.Event, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("log: seek: %w", err)
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, fmt.Errorf("log: read: %w", err)
	}

	if len(data) == 0 {
		return nil, offset, nil
	}

	// Only process complete lines; buffer partial lines for next read.
	lastNewline := -1
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lastNewline = i
			break
		}
	}
	if lastNewline < 0 {
		// No complete line yet.
		return nil, offset, nil
	}

	completeData := data[:lastNewline+1]
	newOffset := offset + int64(len(completeData))
	return parseEventsData(completeData), newOffset, nil
}

// parseEventsData parses JSONL data into events, skipping malformed lines.
func parseEventsData(data []byte) []pipeline.Event {
	var events []pipeline.Event
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev pipeline.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events
}

// filterEvents applies phase and since-time filters.
func filterEvents(events []pipeline.Event, sinceTime time.Time, phase string) []pipeline.Event {
	if sinceTime.IsZero() && phase == "" {
		return events
	}

	var filtered []pipeline.Event
	for _, ev := range events {
		if !sinceTime.IsZero() && ev.Timestamp.Before(sinceTime) {
			continue
		}
		if phase != "" && ev.Phase != phase {
			continue
		}
		filtered = append(filtered, ev)
	}
	return filtered
}

// isTerminalEvent returns true for events that indicate the pipeline
// has reached a terminal state.
func isTerminalEvent(ev pipeline.Event) bool {
	switch ev.Kind {
	case pipeline.EventEngineCompleted, pipeline.EventEngineFailed, pipeline.EventPipelineTimeout:
		return true
	}
	return false
}
