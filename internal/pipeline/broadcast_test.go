package pipeline

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBroadcasterSingleClient(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer b.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	waitForClients(t, b, 1)

	event := Event{
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Phase:     "triage",
		Kind:      EventPhaseStarted,
	}
	b.Broadcast(event)

	msg := readMessage(t, conn, 2*time.Second)
	if msg.Type != "event" {
		t.Errorf("type = %q, want %q", msg.Type, "event")
	}

	var got Event
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got.Phase != "triage" {
		t.Errorf("phase = %q, want %q", got.Phase, "triage")
	}
	if got.Kind != EventPhaseStarted {
		t.Errorf("kind = %q, want %q", got.Kind, EventPhaseStarted)
	}
}

func TestBroadcasterChunkType(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer b.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	waitForClients(t, b, 1)

	b.Broadcast(Event{Kind: EventOutputChunk, Phase: "implement", Data: map[string]any{"text": "hello"}})

	msg := readMessage(t, conn, 2*time.Second)
	if msg.Type != "chunk" {
		t.Errorf("type = %q, want %q", msg.Type, "chunk")
	}
}

func TestBroadcasterFanOut(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer b.Close()

	const numClients = 4
	conns := make([]net.Conn, numClients)
	for i := range conns {
		conn, dialErr := net.Dial("unix", sockPath)
		if dialErr != nil {
			t.Fatalf("dial client %d: %v", i, dialErr)
		}
		defer conn.Close()
		conns[i] = conn
	}

	waitForClients(t, b, numClients)

	event := Event{Kind: EventPhaseCompleted, Phase: "plan"}
	b.Broadcast(event)

	for i, conn := range conns {
		msg := readMessage(t, conn, 2*time.Second)
		if msg.Type != "event" {
			t.Errorf("client %d: type = %q, want %q", i, msg.Type, "event")
		}
	}
}

func TestBroadcasterSlowClientDoesNotBlock(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer b.Close()

	// The slow client connects but never reads.
	slowConn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial slow: %v", err)
	}
	defer slowConn.Close()

	waitForClients(t, b, 1)

	// Broadcast many events — this must complete quickly even though the
	// slow client never reads. If the broadcaster blocked on writes,
	// this would hang past the deadline.
	done := make(chan struct{})
	go func() {
		for i := 0; i < broadcastClientBufSize*3; i++ {
			b.Broadcast(Event{Kind: EventPhaseStarted, Phase: "fill"})
		}
		close(done)
	}()

	select {
	case <-done:
		// Broadcasts completed without blocking — pass.
	case <-time.After(3 * time.Second):
		t.Fatal("Broadcast blocked on slow client")
	}
}

func TestBroadcasterClientDisconnect(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer b.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	waitForClients(t, b, 1)

	conn.Close()

	// Broadcast after disconnect — should not panic.
	b.Broadcast(Event{Kind: EventPhaseStarted, Phase: "triage"})

	// Give the write loop time to notice the closed connection.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.ClientCount() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("disconnected client was not removed")
}

func TestBroadcasterClose(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	waitForClients(t, b, 1)

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Socket file should be removed.
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file still exists after Close")
	}

	// Client should get EOF.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, readErr := conn.Read(buf)
	if readErr == nil {
		t.Error("expected EOF or error on closed connection, got nil")
	}
}

func TestBroadcasterStaleSockCleanup(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")

	// Create a stale socket file.
	if err := os.WriteFile(sockPath, []byte("stale"), 0644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster should succeed with stale socket: %v", err)
	}
	defer b.Close()

	// Should be able to connect.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
}

func TestBroadcasterMaxClients(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer b.Close()

	conns := make([]net.Conn, MaxBroadcastClients)
	for i := range conns {
		conn, dialErr := net.Dial("unix", sockPath)
		if dialErr != nil {
			t.Fatalf("dial client %d: %v", i, dialErr)
		}
		defer conn.Close()
		conns[i] = conn
	}

	waitForClients(t, b, MaxBroadcastClients)

	// One more connection should be immediately closed by the server.
	extra, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial extra: %v", err)
	}
	defer extra.Close()

	// The extra connection should get closed (EOF on read).
	extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, readErr := extra.Read(buf)
	if readErr == nil {
		t.Error("extra client beyond max was not rejected")
	}
}

func TestBroadcasterConcurrentBroadcast(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stream.sock")
	b, err := NewBroadcaster(sockPath)
	if err != nil {
		t.Fatalf("NewBroadcaster: %v", err)
	}
	defer b.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	waitForClients(t, b, 1)

	const numGoroutines = 10
	const eventsPerGoroutine = 5
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for g := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			for e := range eventsPerGoroutine {
				b.Broadcast(Event{
					Kind:  EventPhaseStarted,
					Phase: "concurrent",
					Data:  map[string]any{"goroutine": id, "event": e},
				})
			}
		}(g)
	}
	wg.Wait()

	// Read all events from the connection.
	received := 0
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		received++
		if received >= numGoroutines*eventsPerGoroutine {
			break
		}
	}
	if received != numGoroutines*eventsPerGoroutine {
		t.Errorf("received %d events, want %d", received, numGoroutines*eventsPerGoroutine)
	}
}

// readMessage reads a single newline-delimited JSON message from conn.
func readMessage(t *testing.T, conn net.Conn, timeout time.Duration) BroadcastMessage {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("read message: %v", scanner.Err())
	}
	var msg BroadcastMessage
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal message: %v (raw: %s)", err, scanner.Text())
	}
	return msg
}

// waitForClients polls until the broadcaster has the expected number of clients.
func waitForClients(t *testing.T, b *Broadcaster, expected int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.ClientCount() >= expected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d clients, have %d", expected, b.ClientCount())
}
