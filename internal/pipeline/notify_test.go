package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewNotifier_NilWhenEmpty(t *testing.T) {
	n := NewNotifier(NotifyConfig{})
	if n != nil {
		t.Fatal("expected nil notifier when no hooks configured")
	}
}

func TestNewNotifier_NonNilWithWebhook(t *testing.T) {
	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{URL: "http://example.com"},
	})
	if n == nil {
		t.Fatal("expected non-nil notifier")
	}
}

func TestNewNotifier_NonNilWithScript(t *testing.T) {
	n := NewNotifier(NotifyConfig{
		Script: &ScriptNotifyConfig{Command: "echo hello"},
	})
	if n == nil {
		t.Fatal("expected non-nil notifier")
	}
}

func TestNotifier_NilReceiverSafe(t *testing.T) {
	var n *Notifier
	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1"})
	if err != nil {
		t.Fatalf("nil notifier should return nil, got: %v", err)
	}
}

func TestNotifier_WebhookSuccess(t *testing.T) {
	var receivedBody []byte
	var receivedHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{
			URL: srv.URL,
			Headers: map[string]string{
				"X-Custom": "test-value",
			},
		},
	})

	result := PipelineResult{
		Ticket:    "TEST-42",
		Summary:   "Test ticket",
		Status:    "success",
		TotalCost: 1.23,
		Duration:  "2m30s",
	}

	err := n.Notify(context.Background(), result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify payload
	var got PipelineResult
	if err := json.Unmarshal(receivedBody, &got); err != nil {
		t.Fatalf("unmarshal received body: %v", err)
	}
	if got.Ticket != "TEST-42" {
		t.Errorf("Ticket = %q, want %q", got.Ticket, "TEST-42")
	}
	if got.Status != "success" {
		t.Errorf("Status = %q, want %q", got.Status, "success")
	}
	if got.TotalCost != 1.23 {
		t.Errorf("TotalCost = %f, want 1.23", got.TotalCost)
	}

	// Verify headers
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want %q", receivedHeaders.Get("Content-Type"), "application/json")
	}
	if receivedHeaders.Get("X-Custom") != "test-value" {
		t.Errorf("X-Custom = %q, want %q", receivedHeaders.Get("X-Custom"), "test-value")
	}
}

func TestNotifier_WebhookServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{URL: srv.URL},
	})

	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1", Status: "success"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestNotifier_WebhookBadURL(t *testing.T) {
	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{URL: "http://127.0.0.1:0/nonexistent"},
	})

	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1", Status: "failed"})
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}
}

func TestNotifier_ScriptSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh not available on windows")
	}

	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "notify.json")

	// Script reads stdin and writes it to a file (tee works without shell)
	n := NewNotifier(NotifyConfig{
		Script: &ScriptNotifyConfig{Command: "tee " + outFile},
	})

	result := PipelineResult{
		Ticket: "SCRIPT-1",
		Status: "success",
	}

	err := n.Notify(context.Background(), result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the script received the payload
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	var got PipelineResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if got.Ticket != "SCRIPT-1" {
		t.Errorf("Ticket = %q, want %q", got.Ticket, "SCRIPT-1")
	}
}

func TestNotifier_ScriptFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh not available on windows")
	}

	n := NewNotifier(NotifyConfig{
		Script: &ScriptNotifyConfig{Command: "false"},
	})

	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1", Status: "failed"})
	if err == nil {
		t.Fatal("expected error for failing script")
	}
	if !strings.Contains(err.Error(), "script") {
		t.Errorf("error should mention script, got: %v", err)
	}
}

func TestNotifier_BothWebhookAndScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh not available on windows")
	}

	var webhookCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "notify.json")

	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{URL: srv.URL},
		Script:  &ScriptNotifyConfig{Command: "tee " + outFile},
	})

	err := n.Notify(context.Background(), PipelineResult{Ticket: "BOTH-1", Status: "success"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !webhookCalled {
		t.Error("webhook was not called")
	}
	if _, err := os.Stat(outFile); err != nil {
		t.Errorf("script did not create output file: %v", err)
	}
}

func TestNotifier_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{URL: srv.URL},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := n.Notify(ctx, PipelineResult{Ticket: "X-1", Status: "failed"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestNotifier_EmptyURLSkipsWebhook(t *testing.T) {
	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{URL: ""},
	})
	// Should not be nil because the struct is non-nil, but the empty URL
	// should be a no-op.
	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1", Status: "success"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNotifier_EmptyCommandSkipsScript(t *testing.T) {
	n := NewNotifier(NotifyConfig{
		Script: &ScriptNotifyConfig{Command: ""},
	})
	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1", Status: "success"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNotifier_WebhookTimeout(t *testing.T) {
	// Create a server that hangs until signalled to stop.
	hangCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hangCh:
		case <-r.Context().Done():
		}
	}))
	// Close hangCh first so handler unblocks, then close the server.
	defer func() {
		close(hangCh)
		srv.Close()
	}()

	n := NewNotifier(NotifyConfig{
		Webhook: &WebhookNotifyConfig{URL: srv.URL},
	})

	// Override timeout to something very short so the test completes quickly.
	n.timeout = 50 * time.Millisecond
	n.httpClient = &http.Client{Timeout: 50 * time.Millisecond}

	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1", Status: "success"})
	if err == nil {
		t.Fatal("expected timeout error for hanging server")
	}
	if !strings.Contains(err.Error(), "webhook") {
		t.Errorf("error should mention webhook, got: %v", err)
	}
}

func TestNotifier_MissingScriptBinary(t *testing.T) {
	n := NewNotifier(NotifyConfig{
		Script: &ScriptNotifyConfig{Command: "/nonexistent/binary/path-that-does-not-exist"},
	})

	err := n.Notify(context.Background(), PipelineResult{Ticket: "X-1", Status: "success"})
	if err == nil {
		t.Fatal("expected error for nonexistent script binary")
	}
	if !strings.Contains(err.Error(), "script") {
		t.Errorf("error should mention script, got: %v", err)
	}
}

func TestPipelineResult_JSONRoundTrip(t *testing.T) {
	result := PipelineResult{
		Ticket:    "RT-1",
		Summary:   "Test ticket",
		Branch:    "soda/RT-1",
		Status:    "failed",
		Error:     "budget exceeded",
		TotalCost: 15.50,
		Duration:  "5m23s",
		Phases: map[string]any{
			"triage":    map[string]any{"status": "completed"},
			"implement": map[string]any{"status": "failed"},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got PipelineResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Ticket != result.Ticket {
		t.Errorf("Ticket = %q, want %q", got.Ticket, result.Ticket)
	}
	if got.Status != result.Status {
		t.Errorf("Status = %q, want %q", got.Status, result.Status)
	}
	if got.Error != result.Error {
		t.Errorf("Error = %q, want %q", got.Error, result.Error)
	}
	if got.TotalCost != result.TotalCost {
		t.Errorf("TotalCost = %f, want %f", got.TotalCost, result.TotalCost)
	}
	if got.Duration != result.Duration {
		t.Errorf("Duration = %q, want %q", got.Duration, result.Duration)
	}
	if len(got.Phases) != 2 {
		t.Errorf("len(Phases) = %d, want 2", len(got.Phases))
	}
}
