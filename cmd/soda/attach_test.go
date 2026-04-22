package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decko/soda/internal/pipeline"
)

func TestAttach_NonExistentTicket(t *testing.T) {
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := runAttach(&stdout, &stderr, stateDir, "GHOST-999", false, false)
	if err == nil {
		t.Fatal("expected error for non-existent ticket")
	}
	if !strings.Contains(err.Error(), "no pipeline state") {
		t.Errorf("error = %q, want to contain 'no pipeline state'", err.Error())
	}
}

func TestAttach_CompletedPipeline(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-1")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatal(err)
	}

	meta := &pipeline.PipelineMeta{
		Ticket: "TEST-1",
		Phases: map[string]*pipeline.PhaseState{
			"triage": {Status: pipeline.PhaseCompleted},
		},
		TotalCost: 1.25,
	}
	metaData, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(ticketDir, "meta.json"), metaData, 0644)

	// Write a lock file with a dead PID.
	lockInfo := pipeline.LockInfo{PID: 999999, AcquiredAt: time.Now().Add(-time.Hour)}
	lockData, _ := json.Marshal(lockInfo)
	os.WriteFile(filepath.Join(ticketDir, "lock"), lockData, 0644)

	var stdout, stderr bytes.Buffer
	err := runAttach(&stdout, &stderr, stateDir, "TEST-1", false, false)
	if err == nil {
		t.Fatal("expected error for completed pipeline")
	}
	if !strings.Contains(err.Error(), "pipeline not running") {
		t.Errorf("error = %q, want to contain 'pipeline not running'", err.Error())
	}
	if !strings.Contains(stdout.String(), "not running") {
		t.Errorf("stdout = %q, want to contain 'not running'", stdout.String())
	}
}

func TestAttach_FromStartReplay(t *testing.T) {
	stateDir := t.TempDir()
	ticketDir := filepath.Join(stateDir, "TEST-2")
	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write events to replay.
	events := []pipeline.Event{
		{Timestamp: time.Now(), Kind: pipeline.EventEngineStarted},
		{Timestamp: time.Now(), Phase: "triage", Kind: pipeline.EventPhaseStarted},
		{Timestamp: time.Now(), Phase: "triage", Kind: pipeline.EventPhaseCompleted},
	}
	var eventLines bytes.Buffer
	for _, ev := range events {
		data, _ := json.Marshal(ev)
		eventLines.Write(data)
		eventLines.WriteByte('\n')
	}
	os.WriteFile(filepath.Join(ticketDir, "events.jsonl"), eventLines.Bytes(), 0644)

	var stdout bytes.Buffer
	err := replayHistory(&stdout, ticketDir, true)
	if err != nil {
		t.Fatalf("replayHistory: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "engine_started") {
		t.Errorf("output missing engine_started: %q", output)
	}
	if !strings.Contains(output, "phase_started") {
		t.Errorf("output missing phase_started: %q", output)
	}
	if !strings.Contains(output, "───") || !strings.Contains(output, "─") {
		t.Errorf("output missing separator: %q", output)
	}
}

func TestAttach_StreamFromSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	broadcaster, err := pipeline.NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer broadcaster.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Wait for the broadcaster to register the client.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if broadcaster.ClientCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Broadcast events in a goroutine (including a terminal event to end streaming).
	go func() {
		time.Sleep(50 * time.Millisecond)
		broadcaster.Broadcast(pipeline.Event{
			Kind:  pipeline.EventPhaseStarted,
			Phase: "implement",
		})
		broadcaster.Broadcast(pipeline.Event{
			Kind:  pipeline.EventOutputChunk,
			Phase: "implement",
			Data:  map[string]any{"line": "writing code...\n"},
		})
		broadcaster.Broadcast(pipeline.Event{
			Kind: pipeline.EventEngineCompleted,
		})
	}()

	var stdout bytes.Buffer
	ctx, cancel := contextWithTimeout(t, 5*time.Second)
	defer cancel()

	err = streamFromSocket(ctx, &stdout, conn, false)
	if err != nil {
		t.Fatalf("streamFromSocket: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "implement") {
		t.Errorf("output missing phase header: %q", output)
	}
	if !strings.Contains(output, "writing code...") {
		t.Errorf("output missing chunk content: %q", output)
	}
	if !strings.Contains(output, "completed successfully") {
		t.Errorf("output missing terminal message: %q", output)
	}
}

func TestAttach_StreamWithEvents(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	broadcaster, err := pipeline.NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer broadcaster.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if broadcaster.ClientCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		broadcaster.Broadcast(pipeline.Event{
			Kind:  pipeline.EventPhaseStarted,
			Phase: "triage",
		})
		broadcaster.Broadcast(pipeline.Event{
			Kind: pipeline.EventEngineCompleted,
		})
	}()

	var stdout bytes.Buffer
	ctx, cancel := contextWithTimeout(t, 5*time.Second)
	defer cancel()

	err = streamFromSocket(ctx, &stdout, conn, true)
	if err != nil {
		t.Fatalf("streamFromSocket: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "phase_started") {
		t.Errorf("output missing formatted event: %q", output)
	}
	if !strings.Contains(output, "engine_completed") {
		t.Errorf("output missing terminal event: %q", output)
	}
}

func contextWithTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), d)
}
