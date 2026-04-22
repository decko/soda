package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newAttachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <ticket>",
		Short: "Stream live output from a running pipeline",
		Long:  "Connect to a running pipeline and stream its output, like docker logs -f.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			fromStart, _ := cmd.Flags().GetBool("from-start")
			showEvents, _ := cmd.Flags().GetBool("events")
			return runAttach(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg.StateDir, args[0], fromStart, showEvents)
		},
	}

	cmd.Flags().Bool("from-start", false, "replay event history before live stream")
	cmd.Flags().Bool("events", false, "show phase-level events (not just output chunks)")

	return cmd
}

func runAttach(stdout, stderr io.Writer, stateDir, ticketKey string, fromStart, showEvents bool) error {
	ticketDir := filepath.Join(stateDir, ticketKey)

	if _, err := os.Stat(ticketDir); os.IsNotExist(err) {
		return fmt.Errorf("attach: no pipeline state for ticket %q (run 'soda run %s' first)", ticketKey, ticketKey)
	}

	lockPath := filepath.Join(ticketDir, "lock")
	info, err := pipeline.ReadLockInfo(lockPath)
	if err != nil || !info.IsAlive {
		return attachNotRunning(stdout, ticketDir, ticketKey)
	}

	sockPath := filepath.Join(ticketDir, "stream.sock")
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		return fmt.Errorf("attach: pipeline is running (PID %d) but broadcast socket not found — binary may be outdated", info.PID)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if fromStart {
		if replayErr := replayHistory(stdout, ticketDir, showEvents); replayErr != nil {
			fmt.Fprintf(stderr, "Warning: could not replay history: %v\n", replayErr)
		}
	}

	fmt.Fprintf(stderr, "Attached to pipeline for %s (PID %d)\n", ticketKey, info.PID)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("attach: connect to socket: %w", err)
	}
	defer conn.Close()

	return streamFromSocket(ctx, stdout, conn, showEvents)
}

func attachNotRunning(stdout io.Writer, ticketDir, ticketKey string) error {
	metaPath := filepath.Join(ticketDir, "meta.json")
	meta, err := pipeline.ReadMeta(metaPath)
	if err != nil {
		return fmt.Errorf("attach: pipeline for %q is not running and no metadata found", ticketKey)
	}

	fmt.Fprintf(stdout, "Pipeline for %s is not running.\n", ticketKey)

	lastPhase := ""
	lastStatus := ""
	for phase, ps := range meta.Phases {
		if ps.Status != "" {
			lastPhase = phase
			lastStatus = string(ps.Status)
		}
	}
	if lastPhase != "" {
		fmt.Fprintf(stdout, "Last phase: %s (%s)\n", lastPhase, lastStatus)
	}
	if meta.TotalCost > 0 {
		fmt.Fprintf(stdout, "Total cost: $%.2f\n", meta.TotalCost)
	}

	return fmt.Errorf("pipeline not running")
}

func replayHistory(stdout io.Writer, ticketDir string, showEvents bool) error {
	events, err := pipeline.ReadEvents(ticketDir)
	if err != nil {
		return err
	}

	for _, ev := range events {
		if showEvents {
			fmt.Fprintln(stdout, pipeline.FormatEvent(ev))
		} else if ev.Kind == pipeline.EventOutputChunk {
			printChunkLine(stdout, ev)
		}
	}

	if len(events) > 0 {
		fmt.Fprintln(stdout, strings.Repeat("─", 40))
	}

	return nil
}

func streamFromSocket(ctx context.Context, stdout io.Writer, conn net.Conn, showEvents bool) error {
	scanner := bufio.NewScanner(conn)

	lineCh := make(chan string)
	errCh := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		}
		close(lineCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("attach: read error: %w", err)
		case line, ok := <-lineCh:
			if !ok {
				fmt.Fprintln(stdout, "\nPipeline finished.")
				return nil
			}

			var msg pipeline.BroadcastMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "chunk":
				var ev pipeline.Event
				if err := json.Unmarshal(msg.Data, &ev); err != nil {
					continue
				}
				printChunkLine(stdout, ev)

			case "event":
				if !showEvents {
					var ev pipeline.Event
					if err := json.Unmarshal(msg.Data, &ev); err != nil {
						continue
					}
					if isPhaseTransition(ev) {
						printPhaseHeader(stdout, ev)
					}
					if isTerminalEvent(ev) {
						printTerminal(stdout, ev)
						return nil
					}
					continue
				}
				var ev pipeline.Event
				if err := json.Unmarshal(msg.Data, &ev); err != nil {
					continue
				}
				fmt.Fprintln(stdout, pipeline.FormatEvent(ev))
				if isTerminalEvent(ev) {
					return nil
				}
			}
		}
	}
}

func printChunkLine(w io.Writer, ev pipeline.Event) {
	if line, ok := ev.Data["line"].(string); ok {
		fmt.Fprint(w, line)
	}
}

func isPhaseTransition(ev pipeline.Event) bool {
	switch ev.Kind {
	case pipeline.EventPhaseStarted, pipeline.EventPhaseCompleted, pipeline.EventPhaseFailed:
		return true
	}
	return false
}

func printPhaseHeader(w io.Writer, ev pipeline.Event) {
	switch ev.Kind {
	case pipeline.EventPhaseStarted:
		fmt.Fprintf(w, "\n── %s ──\n", ev.Phase)
	case pipeline.EventPhaseCompleted:
		fmt.Fprintf(w, "\n✓ %s completed\n", ev.Phase)
	case pipeline.EventPhaseFailed:
		errMsg := ""
		if e, ok := ev.Data["error"].(string); ok {
			errMsg = ": " + e
		}
		fmt.Fprintf(w, "\n✗ %s failed%s\n", ev.Phase, errMsg)
	}
}

func printTerminal(w io.Writer, ev pipeline.Event) {
	switch ev.Kind {
	case pipeline.EventEngineCompleted:
		fmt.Fprintln(w, "\nPipeline completed successfully.")
	case pipeline.EventEngineFailed:
		errMsg := ""
		if e, ok := ev.Data["error"].(string); ok {
			errMsg = ": " + e
		}
		fmt.Fprintf(w, "\nPipeline failed%s\n", errMsg)
	case pipeline.EventPipelineTimeout:
		fmt.Fprintln(w, "\nPipeline timed out.")
	}
}
