package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

// parseDuration parses a duration string that supports d (days) and w (weeks)
// in addition to the standard Go time.Duration units. Compound durations like
// "1w2d" and "1d12h" are supported.
func parseDuration(s string) (time.Duration, error) {
	// Try stdlib first — handles pure Go durations like "24h", "30m", etc.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Tokenise (number)(unit) pairs. Examples: "30d", "1w2d", "1d12h".
	var total time.Duration
	remaining := strings.TrimSpace(s)
	if remaining == "" {
		return 0, fmt.Errorf("parseDuration: empty string")
	}

	parsed := false
	for len(remaining) > 0 {
		// Skip leading whitespace.
		remaining = strings.TrimLeftFunc(remaining, unicode.IsSpace)
		if len(remaining) == 0 {
			break
		}

		// Find the numeric prefix.
		numEnd := 0
		for numEnd < len(remaining) && (remaining[numEnd] >= '0' && remaining[numEnd] <= '9' || remaining[numEnd] == '.') {
			numEnd++
		}
		if numEnd == 0 {
			return 0, fmt.Errorf("parseDuration: invalid duration %q", s)
		}

		numStr := remaining[:numEnd]
		remaining = remaining[numEnd:]

		// Find the unit suffix.
		unitEnd := 0
		for unitEnd < len(remaining) && unicode.IsLetter(rune(remaining[unitEnd])) {
			unitEnd++
		}
		if unitEnd == 0 {
			return 0, fmt.Errorf("parseDuration: missing unit in %q", s)
		}

		unit := remaining[:unitEnd]
		remaining = remaining[unitEnd:]

		num, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, fmt.Errorf("parseDuration: invalid number %q in %q", numStr, s)
		}

		switch unit {
		case "d":
			total += time.Duration(num * float64(24*time.Hour))
		case "w":
			total += time.Duration(num * float64(7*24*time.Hour))
		default:
			// Delegate non-custom units to stdlib (e.g. "h", "m", "s").
			d, parseErr := time.ParseDuration(numStr + unit)
			if parseErr != nil {
				return 0, fmt.Errorf("parseDuration: unknown unit %q in %q", unit, s)
			}
			total += d
		}
		parsed = true
	}

	if !parsed {
		return 0, fmt.Errorf("parseDuration: invalid duration %q", s)
	}
	return total, nil
}

// gcIsTerminal returns true if the pipeline is in a state safe for garbage
// collection. Unlike isTerminal (used by clean), this rejects paused and
// pending phases — a user may intend to resume those sessions.
func gcIsTerminal(meta *pipeline.PipelineMeta) bool {
	if len(meta.Phases) == 0 {
		return true
	}
	for _, ps := range meta.Phases {
		switch ps.Status {
		case pipeline.PhaseRunning, pipeline.PhaseRetrying, pipeline.PhasePaused, pipeline.PhasePending:
			return false
		}
	}
	return true
}

// dirSize sums the sizes of all regular files under path via filepath.Walk.
// Permission errors on individual files are silently skipped.
func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsPermission(walkErr) {
				return nil
			}
			return walkErr
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("dirSize: %w", err)
	}
	return total, nil
}

// gcSessions iterates session directories in stateDir and removes those that
// are older than olderThan, not locked, have no worktree, and are in a terminal
// state. When dryRun is true it prints what would be removed without deleting.
func gcSessions(ctx context.Context, stateDir string, olderThan time.Duration, dryRun bool) error {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No sessions found.")
			return nil
		}
		return fmt.Errorf("gc: read state dir: %w", err)
	}

	var removedCount int
	var freedBytes int64

	for _, entry := range entries {
		// Check context cancellation at the top of every iteration.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Skip non-directory entries (e.g. cost.json ledger).
		if !entry.IsDir() {
			continue
		}

		ticketDir := filepath.Join(stateDir, entry.Name())

		// Age filter: check directory mtime.
		if olderThan > 0 {
			info, statErr := entry.Info()
			if statErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: %s: stat: %v\n", entry.Name(), statErr)
				continue
			}
			age := time.Since(info.ModTime())
			if age < olderThan {
				continue
			}
		}

		// Lock check: skip if a pipeline is running.
		lockPath := filepath.Join(ticketDir, "lock")
		if !tryLock(lockPath) {
			fmt.Fprintf(os.Stderr, "Skipping %s: pipeline is running\n", entry.Name())
			continue
		}

		// Read meta.json — skip if unreadable.
		metaPath := filepath.Join(ticketDir, "meta.json")
		meta, metaErr := pipeline.ReadMeta(metaPath)
		if metaErr != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", entry.Name(), metaErr)
			continue
		}

		// Worktree check: skip sessions that still have an active worktree.
		if meta.Worktree != "" {
			fmt.Fprintf(os.Stderr, "Skipping %s: worktree still exists (%s)\n", entry.Name(), meta.Worktree)
			continue
		}

		// Terminal check: only GC sessions in terminal state.
		if !gcIsTerminal(meta) {
			fmt.Fprintf(os.Stderr, "Skipping %s: not in terminal state\n", entry.Name())
			continue
		}

		// Calculate size before removal.
		size, sizeErr := dirSize(ticketDir)
		if sizeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: %s: cannot compute size: %v\n", entry.Name(), sizeErr)
			size = 0
		}

		if dryRun {
			fmt.Printf("Would remove %s (%s)\n", entry.Name(), formatBytes(size))
		} else {
			if rmErr := os.RemoveAll(ticketDir); rmErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: %s: remove: %v\n", entry.Name(), rmErr)
				continue
			}
			fmt.Printf("Removed %s (%s)\n", entry.Name(), formatBytes(size))
		}
		removedCount++
		freedBytes += size
	}

	if removedCount == 0 {
		fmt.Println("Nothing to collect.")
	} else {
		verb := "Freed"
		if dryRun {
			verb = "Would free"
		}
		fmt.Printf("%s %d session(s), %s\n", verb, removedCount, formatBytes(freedBytes))
	}
	return nil
}

// formatBytes returns a human-readable byte count.
func formatBytes(bytes int64) string {
	const (
		kb = 1024
		mb = kb * 1024
	)
	switch {
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func newGcCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Garbage collect old session data",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			olderThanStr, _ := cmd.Flags().GetString("older-than")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			olderThan, parseErr := parseDuration(olderThanStr)
			if parseErr != nil {
				return fmt.Errorf("gc: invalid --older-than value: %w", parseErr)
			}

			return gcSessions(cmd.Context(), cfg.StateDir, olderThan, dryRun)
		},
	}

	cmd.Flags().String("older-than", "30d", "minimum age of sessions to collect (e.g. 30d, 2w, 1w2d)")
	cmd.Flags().Bool("dry-run", false, "show what would be collected without removing")

	return cmd
}
