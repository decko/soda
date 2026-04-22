package pipeline

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// MaxBroadcastClients is the maximum number of concurrent attach clients.
const MaxBroadcastClients = 8

// broadcastClientBufSize is the per-client event buffer depth.
// Events are dropped if a client falls behind.
const broadcastClientBufSize = 64

// broadcastWriteTimeout is the deadline for writing a single message to a client.
const broadcastWriteTimeout = 2 * time.Second

// BroadcastMessage is the typed JSON envelope sent over the socket.
type BroadcastMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Broadcaster manages a Unix domain socket listener and fans out events
// to all connected clients. Non-blocking: slow clients are dropped.
type Broadcaster struct {
	listener net.Listener
	sockPath string

	mu      sync.Mutex
	clients map[*broadcastClient]struct{}
	closed  bool
	wg      sync.WaitGroup
}

type broadcastClient struct {
	conn net.Conn
	ch   chan []byte
}

// NewBroadcaster creates a Unix domain socket at sockPath and starts
// accepting connections. If a stale socket file exists from a dead
// process, it is removed.
func NewBroadcaster(sockPath string) (*Broadcaster, error) {
	if err := cleanStaleSock(sockPath); err != nil {
		return nil, fmt.Errorf("broadcast: clean stale socket: %w", err)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("broadcast: listen %s: %w", sockPath, err)
	}

	b := &Broadcaster{
		listener: ln,
		sockPath: sockPath,
		clients:  make(map[*broadcastClient]struct{}),
	}

	b.wg.Add(1)
	go b.acceptLoop()

	return b, nil
}

// Broadcast sends an event to all connected clients. Non-blocking:
// if a client's buffer is full, the event is dropped for that client.
func (b *Broadcaster) Broadcast(event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	msgType := "event"
	if event.Kind == EventOutputChunk {
		msgType = "chunk"
	}

	msg := BroadcastMessage{
		Type: msgType,
		Data: json.RawMessage(data),
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return
	}
	line = append(line, '\n')

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}

	for c := range b.clients {
		select {
		case c.ch <- line:
		default:
			// Slow client — drop this event.
		}
	}
}

// ClientCount returns the number of connected clients.
func (b *Broadcaster) ClientCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// Close shuts down the listener, disconnects all clients, and removes
// the socket file.
func (b *Broadcaster) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true

	for c := range b.clients {
		close(c.ch)
		c.conn.Close()
		delete(b.clients, c)
	}
	b.mu.Unlock()

	err := b.listener.Close()
	b.wg.Wait()
	os.Remove(b.sockPath)
	return err
}

func (b *Broadcaster) acceptLoop() {
	defer b.wg.Done()
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}

		b.mu.Lock()
		if b.closed || len(b.clients) >= MaxBroadcastClients {
			b.mu.Unlock()
			conn.Close()
			continue
		}

		c := &broadcastClient{
			conn: conn,
			ch:   make(chan []byte, broadcastClientBufSize),
		}
		b.clients[c] = struct{}{}
		b.mu.Unlock()

		b.wg.Add(1)
		go b.writeLoop(c)
	}
}

func (b *Broadcaster) writeLoop(c *broadcastClient) {
	defer b.wg.Done()
	defer func() {
		b.mu.Lock()
		delete(b.clients, c)
		b.mu.Unlock()
		c.conn.Close()
	}()

	for line := range c.ch {
		c.conn.SetWriteDeadline(time.Now().Add(broadcastWriteTimeout))
		if _, err := c.conn.Write(line); err != nil {
			return
		}
	}
}

// cleanStaleSock removes an existing socket file if it exists and no
// process holds the corresponding lock. This handles the case where a
// previous pipeline crashed and left the socket behind.
func cleanStaleSock(sockPath string) error {
	_, err := os.Stat(sockPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.Remove(sockPath)
}
