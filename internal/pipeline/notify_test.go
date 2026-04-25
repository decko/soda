package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/decko/soda/internal/runner"
)

// --- Status derivation ---

func TestDeriveStatus_Success(t *testing.T) {
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{})

	status := engine.deriveStatus(nil)
	if status != NotifyStatusSuccess {
		t.Errorf("deriveStatus(nil) = %q, want %q", status, NotifyStatusSuccess)
	}
}

func TestDeriveStatus_Failure(t *testing.T) {
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{})

	status := engine.deriveStatus(fmt.Errorf("some error"))
	if status != NotifyStatusFailure {
		t.Errorf("deriveStatus(error) = %q, want %q", status, NotifyStatusFailure)
	}
}

func TestDeriveStatus_Timeout(t *testing.T) {
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{})

	pte := &PipelineTimeoutError{
		Limit:   2 * time.Hour,
		Elapsed: 2*time.Hour + 5*time.Minute,
		Phase:   "implement",
	}
	status := engine.deriveStatus(pte)
	if status != NotifyStatusTimeout {
		t.Errorf("deriveStatus(PipelineTimeoutError) = %q, want %q", status, NotifyStatusTimeout)
	}
}

func TestDeriveStatus_TimeoutWrapped(t *testing.T) {
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{})

	pte := &PipelineTimeoutError{
		Limit:   2 * time.Hour,
		Elapsed: 2*time.Hour + 5*time.Minute,
		Phase:   "implement",
	}
	wrapped := fmt.Errorf("engine: %w", pte)
	status := engine.deriveStatus(wrapped)
	if status != NotifyStatusTimeout {
		t.Errorf("deriveStatus(wrapped PipelineTimeoutError) = %q, want %q", status, NotifyStatusTimeout)
	}
}

func TestDeriveStatus_Partial(t *testing.T) {
	engine, state := setupEngine(t, []PhaseConfig{
		{Name: "submit", Prompt: "submit.md"},
		{Name: "monitor", Prompt: "monitor.md"},
	}, &flexMockRunner{})

	// Mark submit as completed.
	state.Meta().Phases["submit"] = &PhaseState{Status: PhaseCompleted}

	status := engine.deriveStatus(fmt.Errorf("monitor failed"))
	if status != NotifyStatusPartial {
		t.Errorf("deriveStatus(submit completed + error) = %q, want %q", status, NotifyStatusPartial)
	}
}

func TestDeriveStatus_SubmitNotCompleted(t *testing.T) {
	engine, state := setupEngine(t, []PhaseConfig{
		{Name: "submit", Prompt: "submit.md"},
	}, &flexMockRunner{})

	// Submit failed — should be failure, not partial.
	state.Meta().Phases["submit"] = &PhaseState{Status: PhaseFailed}

	status := engine.deriveStatus(fmt.Errorf("submit failed"))
	if status != NotifyStatusFailure {
		t.Errorf("deriveStatus(submit failed) = %q, want %q", status, NotifyStatusFailure)
	}
}

// --- Webhook payload ---

func TestBuildWebhookPayload_Success(t *testing.T) {
	engine, state := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{})

	state.Meta().Ticket = "TEST-42"
	state.Meta().Branch = "soda/TEST-42"
	state.Meta().TotalCost = 1.50

	payload := engine.buildWebhookPayload(NotifyStatusSuccess, nil)

	if payload.Ticket != "TEST-42" {
		t.Errorf("Ticket = %q, want %q", payload.Ticket, "TEST-42")
	}
	if payload.Status != NotifyStatusSuccess {
		t.Errorf("Status = %q, want %q", payload.Status, NotifyStatusSuccess)
	}
	if payload.Branch != "soda/TEST-42" {
		t.Errorf("Branch = %q, want %q", payload.Branch, "soda/TEST-42")
	}
	if payload.TotalCost != 1.50 {
		t.Errorf("TotalCost = %f, want 1.50", payload.TotalCost)
	}
	if payload.Error != "" {
		t.Errorf("Error = %q, want empty", payload.Error)
	}
}

func TestBuildWebhookPayload_WithError(t *testing.T) {
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{})

	payload := engine.buildWebhookPayload(NotifyStatusFailure, fmt.Errorf("phase triage failed"))

	if payload.Error != "phase triage failed" {
		t.Errorf("Error = %q, want %q", payload.Error, "phase triage failed")
	}
}

// --- Webhook POST ---

func TestPostWebhook_Success(t *testing.T) {
	var received webhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
	})

	payload := webhookPayload{
		Ticket: "TEST-1",
		Status: NotifyStatusSuccess,
	}
	err := engine.postWebhook(context.Background(), payload)
	if err != nil {
		t.Fatalf("postWebhook: %v", err)
	}
	if received.Ticket != "TEST-1" {
		t.Errorf("received.Ticket = %q, want %q", received.Ticket, "TEST-1")
	}
}

func TestPostWebhook_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
	})

	err := engine.postWebhook(context.Background(), webhookPayload{})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestPostWebhook_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block longer than context timeout.
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := engine.postWebhook(ctx, webhookPayload{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- Script execution ---

func TestRunScript_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script tests require Unix")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "notify.sh")
	outputFile := filepath.Join(dir, "output.txt")
	script := fmt.Sprintf("#!/bin/sh\necho \"$1 $2\" > %s\n", outputFile)
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	engine, state := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.Script = scriptPath
		cfg.WorkDir = dir
	})

	state.Meta().Ticket = "TEST-99"

	err := engine.runScript(context.Background(), NotifyStatusSuccess, nil)
	if err != nil {
		t.Fatalf("runScript: %v", err)
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(data)
	if got != "success TEST-99\n" {
		t.Errorf("script output = %q, want %q", got, "success TEST-99\n")
	}
}

func TestRunScript_NotFound(t *testing.T) {
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.Script = "/nonexistent/script.sh"
	})

	err := engine.runScript(context.Background(), NotifyStatusFailure, nil)
	if err == nil {
		t.Fatal("expected error for missing script")
	}
}

func TestRunScript_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script tests require Unix")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "slow.sh")
	// Use a trap and wait loop so the process exits promptly when killed.
	script := "#!/bin/sh\ntrap 'exit 1' TERM\nwhile true; do sleep 0.1; done\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.Script = scriptPath
		cfg.WorkDir = dir
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := engine.runScript(ctx, NotifyStatusSuccess, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- notifyOnFinish orchestrator ---

func TestNotifyOnFinish_NoConfig(t *testing.T) {
	var events []Event
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	// No notify config — should be a no-op.
	engine.notifyOnFinish(nil)

	for _, e := range events {
		if e.Kind == EventNotifySuccess || e.Kind == EventNotifyFailed {
			t.Errorf("unexpected notify event: %s", e.Kind)
		}
	}
}

func TestNotifyOnFinish_WebhookSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var events []Event
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	engine.notifyOnFinish(nil)

	hasSuccess := false
	for _, e := range events {
		if e.Kind == EventNotifySuccess {
			if typ, _ := e.Data["type"].(string); typ == "webhook" {
				hasSuccess = true
			}
		}
	}
	if !hasSuccess {
		t.Error("expected notify_success event for webhook")
	}
}

func TestNotifyOnFinish_WebhookFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	var events []Event
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
		cfg.OnEvent = func(e Event) {
			events = append(events, e)
		}
	})

	engine.notifyOnFinish(nil)

	hasFailed := false
	for _, e := range events {
		if e.Kind == EventNotifyFailed {
			if typ, _ := e.Data["type"].(string); typ == "webhook" {
				hasFailed = true
			}
		}
	}
	if !hasFailed {
		t.Error("expected notify_failed event for webhook")
	}
}

// --- Integration with Run/Resume ---

func TestRun_NotifiesOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var mu sync.Mutex
	var events []Event
	engine, _ := setupEngine(t, []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}, &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"automatable":true}`),
					RawText: "Triage complete",
				},
			}},
		},
	}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
		cfg.OnEvent = func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	})

	err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	hasNotifySuccess := false
	for _, e := range events {
		if e.Kind == EventNotifySuccess {
			hasNotifySuccess = true
		}
	}
	if !hasNotifySuccess {
		t.Error("expected notify_success event after successful Run")
	}
}

func TestRun_NotifiesOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload webhookPayload
		json.NewDecoder(r.Body).Decode(&payload)
		if payload.Status != NotifyStatusFailure {
			t.Errorf("webhook status = %q, want %q", payload.Status, NotifyStatusFailure)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var mu sync.Mutex
	var events []Event
	engine, _ := setupEngine(t, []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{},
		},
	}, &flexMockRunner{
		responses: map[string][]flexResponse{
			"triage": {{
				err: fmt.Errorf("runner failed"),
			}},
		},
	}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
		cfg.OnEvent = func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	})

	err := engine.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from Run")
	}

	mu.Lock()
	defer mu.Unlock()

	hasNotifySuccess := false
	for _, e := range events {
		if e.Kind == EventNotifySuccess {
			hasNotifySuccess = true
		}
	}
	if !hasNotifySuccess {
		t.Error("expected notify_success event (webhook accepted the failure payload)")
	}
}

func TestResume_NotifiesOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var mu sync.Mutex
	var events []Event
	engine, state := setupEngine(t, []PhaseConfig{
		{
			Name:   "triage",
			Prompt: "triage.md",
			Retry:  RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
		{
			Name:      "plan",
			Prompt:    "plan.md",
			DependsOn: []string{"triage"},
			Retry:     RetryConfig{Transient: 1, Parse: 1, Semantic: 1},
		},
	}, &flexMockRunner{
		responses: map[string][]flexResponse{
			"plan": {{
				result: &runner.RunResult{
					Output:  json.RawMessage(`{"tasks":["t1"]}`),
					RawText: "Plan done",
				},
			}},
		},
	}, func(cfg *EngineConfig) {
		cfg.Notify.WebhookURL = server.URL
		cfg.OnEvent = func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	})

	// Mark triage as completed so resume from plan works.
	state.Meta().Phases["triage"] = &PhaseState{Status: PhaseCompleted}

	err := engine.Resume(context.Background(), "plan")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	hasNotifySuccess := false
	for _, e := range events {
		if e.Kind == EventNotifySuccess {
			hasNotifySuccess = true
		}
	}
	if !hasNotifySuccess {
		t.Error("expected notify_success event after successful Resume")
	}
}

// --- NotifyConfig timeout default ---

func TestNotifyConfig_DefaultTimeout(t *testing.T) {
	cfg := NotifyConfig{}
	if got := cfg.notifyTimeout(); got != defaultNotifyTimeout {
		t.Errorf("notifyTimeout() = %v, want %v", got, defaultNotifyTimeout)
	}
}

func TestNotifyConfig_CustomTimeout(t *testing.T) {
	cfg := NotifyConfig{Timeout: 10 * time.Second}
	if got := cfg.notifyTimeout(); got != 10*time.Second {
		t.Errorf("notifyTimeout() = %v, want 10s", got)
	}
}

// Verify errors.As works for PipelineTimeoutError through wrapping.
func TestDeriveStatus_ErrorsAsIntegration(t *testing.T) {
	engine, _ := setupEngine(t, []PhaseConfig{
		{Name: "triage", Prompt: "triage.md"},
	}, &flexMockRunner{})

	// Deeply wrapped PipelineTimeoutError.
	inner := &PipelineTimeoutError{Limit: time.Hour, Elapsed: time.Hour, Phase: "plan"}
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", inner))

	status := engine.deriveStatus(wrapped)
	if status != NotifyStatusTimeout {
		t.Errorf("deriveStatus(deeply wrapped PipelineTimeoutError) = %q, want %q", status, NotifyStatusTimeout)
	}

	// Verify errors.As actually works.
	var pte *PipelineTimeoutError
	if !errors.As(wrapped, &pte) {
		t.Fatal("errors.As should find PipelineTimeoutError")
	}
}
