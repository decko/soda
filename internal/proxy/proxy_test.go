package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxy_ForwardsRequests(t *testing.T) {
	var receivedAuth string
	var receivedBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "hello"}},
			"usage":   map[string]any{"input_tokens": 100, "output_tokens": 50},
		})
	}))
	defer upstream.Close()

	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	proxy, err := New(Config{
		SocketPath:  sockPath,
		UpstreamURL: upstream.URL,
		APIKey:      "sk-test-key-123",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer proxy.Close()

	client := unixClient(sockPath)
	resp, err := client.Post("http://localhost/v1/messages", "application/json", strings.NewReader(`{"model":"claude-3","messages":[]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if receivedAuth != "Bearer sk-test-key-123" {
		t.Errorf("upstream auth = %q, want 'Bearer sk-test-key-123'", receivedAuth)
	}
	if receivedBody != `{"model":"claude-3","messages":[]}` {
		t.Errorf("upstream body = %q", receivedBody)
	}
}

func TestProxy_CredentialIsolation(t *testing.T) {
	var headers http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.WriteHeader(200)
		w.Write([]byte(`{"usage":{"input_tokens":0,"output_tokens":0}}`))
	}))
	defer upstream.Close()

	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	proxy, err := New(Config{
		SocketPath:  sockPath,
		UpstreamURL: upstream.URL,
		APIKey:      "sk-real-key",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer proxy.Close()

	client := unixClient(sockPath)
	req, _ := http.NewRequest("POST", "http://localhost/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer sk-fake-nonce")
	req.Header.Set("x-api-key", "should-be-replaced")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if got := headers.Get("Authorization"); got != "Bearer sk-real-key" {
		t.Errorf("upstream Authorization = %q, want 'Bearer sk-real-key'", got)
	}
	if got := headers.Get("x-api-key"); got != "sk-real-key" {
		t.Errorf("upstream x-api-key = %q, want 'sk-real-key'", got)
	}
}

func TestProxy_TokenMetering(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"usage": map[string]any{
				"input_tokens":  200,
				"output_tokens": 100,
			},
		})
	}))
	defer upstream.Close()

	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	proxy, err := New(Config{
		SocketPath:  sockPath,
		UpstreamURL: upstream.URL,
		APIKey:      "sk-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer proxy.Close()

	client := unixClient(sockPath)
	for range 3 {
		resp, reqErr := client.Post("http://localhost/v1/messages", "application/json", strings.NewReader("{}"))
		if reqErr != nil {
			t.Fatalf("request: %v", reqErr)
		}
		resp.Body.Close()
	}

	stats := proxy.Stats()
	if stats.Requests != 3 {
		t.Errorf("requests = %d, want 3", stats.Requests)
	}
	if stats.InputTokens != 600 {
		t.Errorf("input_tokens = %d, want 600", stats.InputTokens)
	}
	if stats.OutputTokens != 300 {
		t.Errorf("output_tokens = %d, want 300", stats.OutputTokens)
	}
}

func TestProxy_BudgetEnforcement(t *testing.T) {
	callCount := int32(0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"usage": map[string]any{
				"input_tokens":  1000,
				"output_tokens": 500,
			},
		})
	}))
	defer upstream.Close()

	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	proxy, err := New(Config{
		SocketPath:      sockPath,
		UpstreamURL:     upstream.URL,
		APIKey:          "sk-test",
		MaxInputTokens:  500,
		MaxOutputTokens: 400,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer proxy.Close()

	client := unixClient(sockPath)

	// First request: 1000 in, 500 out — within budget.
	resp, err := client.Post("http://localhost/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("req 1: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("req 1 status = %d, want 200", resp.StatusCode)
	}

	// Second request: would exceed budget (2000 in > 1500 max).
	resp2, err := client.Post("http://localhost/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("req 2: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 429 {
		t.Errorf("req 2 status = %d, want 429", resp2.StatusCode)
	}

	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "budget exceeded") {
		t.Errorf("body = %q, want to contain 'budget exceeded'", string(body))
	}

	// Upstream should have been called only once.
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
}

func TestProxy_RequestLogging(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage":   map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer upstream.Close()

	logDir := t.TempDir()
	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	proxy, err := New(Config{
		SocketPath:  sockPath,
		UpstreamURL: upstream.URL,
		APIKey:      "sk-test",
		LogDir:      logDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer proxy.Close()

	client := unixClient(sockPath)
	resp, err := client.Post("http://localhost/v1/messages", "application/json", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	// Give the async log writer time to flush.
	time.Sleep(100 * time.Millisecond)

	entries, readErr := os.ReadDir(logDir)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	if len(entries) == 0 {
		t.Error("no log files written")
	}
}

func TestProxy_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"internal server error"}}`))
	}))
	defer upstream.Close()

	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	proxy, err := New(Config{
		SocketPath:  sockPath,
		UpstreamURL: upstream.URL,
		APIKey:      "sk-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer proxy.Close()

	client := unixClient(sockPath)
	resp, err := client.Post("http://localhost/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestProxy_Close(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	proxy, err := New(Config{
		SocketPath:  sockPath,
		UpstreamURL: "http://localhost:0",
		APIKey:      "sk-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := proxy.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file not cleaned up")
	}
}

// unixClient creates an HTTP client that dials a Unix socket.
func unixClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}
