package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"time"
)

// NotifyStatus represents the outcome of a pipeline run for notifications.
type NotifyStatus string

const (
	// NotifyStatusSuccess indicates the pipeline completed all phases.
	NotifyStatusSuccess NotifyStatus = "success"
	// NotifyStatusFailure indicates the pipeline failed.
	NotifyStatusFailure NotifyStatus = "failure"
	// NotifyStatusTimeout indicates the pipeline exceeded its time limit.
	NotifyStatusTimeout NotifyStatus = "timeout"
	// NotifyStatusPartial indicates submit succeeded but monitor failed.
	NotifyStatusPartial NotifyStatus = "partial"
)

// NotifyConfig holds notification settings for the engine.
// Both WebhookURL and Script are optional; when both are set, both
// are invoked best-effort.
type NotifyConfig struct {
	WebhookURL string        // HTTP(S) URL to POST a JSON payload on completion
	Script     string        // path to executable script invoked on completion
	Timeout    time.Duration // max duration for webhook/script; 0 means default (30s)
}

// defaultNotifyTimeout is the default timeout for notification webhook and
// script callbacks when no explicit timeout is configured.
const defaultNotifyTimeout = 30 * time.Second

// notifyTimeout returns the configured timeout, falling back to the default.
func (c *NotifyConfig) notifyTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultNotifyTimeout
}

// EventNotifyFailed is emitted when a notification callback fails.
// Notification errors are best-effort and never propagated to the caller.
const EventNotifyFailed = "notify_failed"

// EventNotifySuccess is emitted when a notification callback succeeds.
const EventNotifySuccess = "notify_success"

// webhookPayload is the JSON body sent to the configured webhook URL.
type webhookPayload struct {
	Ticket    string       `json:"ticket"`
	Status    NotifyStatus `json:"status"`
	Branch    string       `json:"branch,omitempty"`
	PRURL     string       `json:"pr_url,omitempty"`
	TotalCost float64      `json:"total_cost"`
	Error     string       `json:"error,omitempty"`
}

// deriveStatus determines the notification status from the pipeline run error
// and meta state. It uses errors.As to classify the error, matching existing
// patterns in wrapTimeoutError and formatNextSteps.
func (e *Engine) deriveStatus(runErr error) NotifyStatus {
	if runErr == nil {
		return NotifyStatusSuccess
	}

	// Check for pipeline timeout.
	var pte *PipelineTimeoutError
	if errors.As(runErr, &pte) {
		return NotifyStatusTimeout
	}

	// Check for partial: submit completed but a later phase (e.g., monitor) failed.
	meta := e.state.Meta()
	if ps := meta.Phases["submit"]; ps != nil && ps.Status == PhaseCompleted {
		// If submit completed and we still got an error, it's partial.
		return NotifyStatusPartial
	}

	return NotifyStatusFailure
}

// buildWebhookPayload constructs the JSON payload for the webhook notification.
func (e *Engine) buildWebhookPayload(status NotifyStatus, runErr error) webhookPayload {
	meta := e.state.Meta()
	p := webhookPayload{
		Ticket:    meta.Ticket,
		Status:    status,
		Branch:    meta.Branch,
		TotalCost: meta.TotalCost,
	}
	if runErr != nil {
		p.Error = runErr.Error()
	}
	// Extract PR URL from submit result.
	p.PRURL = e.extractPRURL()
	return p
}

// runScript executes the notification script with status information passed
// as arguments. The script is invoked directly (no shell) via
// exec.CommandContext.
func (e *Engine) runScript(ctx context.Context, status NotifyStatus, runErr error) error {
	script := e.config.Notify.Script
	meta := e.state.Meta()

	args := []string{
		string(status),
		meta.Ticket,
	}
	if meta.Branch != "" {
		args = append(args, meta.Branch)
	}

	cmd := exec.CommandContext(ctx, script, args...)
	cmd.Dir = e.config.WorkDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("notify script %s: %w (output: %s)", script, err, string(output))
	}
	return nil
}

// postWebhook sends a POST request with the JSON payload to the configured
// webhook URL.
func (e *Engine) postWebhook(ctx context.Context, payload webhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notify webhook marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.config.Notify.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("notify webhook POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("notify webhook POST: status %d", resp.StatusCode)
	}
	return nil
}

// notifyOnFinish is the best-effort notification orchestrator. It derives the
// pipeline status, runs the configured script and/or webhook, and emits events
// on success or failure. Errors are captured via EventNotifyFailed and never
// propagated to the caller.
func (e *Engine) notifyOnFinish(runErr error) {
	cfg := e.config.Notify
	if cfg.WebhookURL == "" && cfg.Script == "" {
		return
	}

	status := e.deriveStatus(runErr)
	timeout := cfg.notifyTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if cfg.Script != "" {
		if err := e.runScript(ctx, status, runErr); err != nil {
			e.emit(Event{
				Kind: EventNotifyFailed,
				Data: map[string]any{"type": "script", "error": err.Error()},
			})
		} else {
			e.emit(Event{
				Kind: EventNotifySuccess,
				Data: map[string]any{"type": "script"},
			})
		}
	}

	if cfg.WebhookURL != "" {
		payload := e.buildWebhookPayload(status, runErr)
		if err := e.postWebhook(ctx, payload); err != nil {
			e.emit(Event{
				Kind: EventNotifyFailed,
				Data: map[string]any{"type": "webhook", "error": err.Error()},
			})
		} else {
			e.emit(Event{
				Kind: EventNotifySuccess,
				Data: map[string]any{"type": "webhook"},
			})
		}
	}
}
