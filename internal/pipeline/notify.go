package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// NotifyConfig configures notification hooks fired on pipeline completion.
// Both Webhook and Script may be set; they fire independently.
// Mirrors config.NotifyConfig — kept separate to avoid cross-package imports.
type NotifyConfig struct {
	Webhook *WebhookNotifyConfig
	Script  *ScriptNotifyConfig
}

// WebhookNotifyConfig configures an HTTP POST webhook notification.
type WebhookNotifyConfig struct {
	URL     string
	Headers map[string]string
}

// ScriptNotifyConfig configures a script callback notification.
type ScriptNotifyConfig struct {
	Command string
}

// PipelineResult is the JSON payload sent to notification hooks.
type PipelineResult struct {
	Ticket    string         `json:"ticket"`
	Summary   string         `json:"summary,omitempty"`
	Branch    string         `json:"branch,omitempty"`
	Status    string         `json:"status"` // "success" or "failed"
	Error     string         `json:"error,omitempty"`
	TotalCost float64        `json:"total_cost"`
	Duration  string         `json:"duration"`
	Phases    map[string]any `json:"phases,omitempty"`
}

// defaultNotifyTimeout is the maximum time allowed for a single webhook or
// script notification. Notifications are best-effort and must not block the
// pipeline for extended periods.
const defaultNotifyTimeout = 30 * time.Second

// Notifier sends pipeline completion notifications via webhook and/or script.
type Notifier struct {
	config     NotifyConfig
	httpClient *http.Client
	timeout    time.Duration
}

// NewNotifier creates a Notifier. Returns nil if no notifications are configured.
func NewNotifier(cfg NotifyConfig) *Notifier {
	if cfg.Webhook == nil && cfg.Script == nil {
		return nil
	}
	return &Notifier{
		config: cfg,
		httpClient: &http.Client{
			Timeout: defaultNotifyTimeout,
		},
		timeout: defaultNotifyTimeout,
	}
}

// Notify sends the pipeline result to all configured notification hooks.
// Errors from individual hooks are collected and returned as a combined error.
// Notifications are best-effort: the caller should log but not fail on errors.
func (n *Notifier) Notify(ctx context.Context, result PipelineResult) error {
	if n == nil {
		return nil
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("notify: marshal payload: %w", err)
	}

	var errs []error

	if n.config.Webhook != nil && n.config.Webhook.URL != "" {
		if err := n.sendWebhook(ctx, payload); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		}
	}

	if n.config.Script != nil && n.config.Script.Command != "" {
		if err := n.runScript(ctx, payload); err != nil {
			errs = append(errs, fmt.Errorf("script: %w", err))
		}
	}

	if len(errs) == 1 {
		return fmt.Errorf("notify: %w", errs[0])
	}
	if len(errs) > 1 {
		return fmt.Errorf("notify: multiple errors: %v, %v", errs[0], errs[1])
	}
	return nil
}

// sendWebhook sends an HTTP POST with the JSON payload to the configured URL.
func (n *Notifier) sendWebhook(ctx context.Context, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.config.Webhook.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	for k, v := range n.config.Webhook.Headers {
		req.Header.Set(k, v)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	// Drain body to enable connection reuse.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}

// runScript executes the configured command with the JSON payload on stdin.
func (n *Notifier) runScript(ctx context.Context, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", n.config.Script.Command)
	cmd.Stdin = bytes.NewReader(payload)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("execute %q: %w (output: %s)", n.config.Script.Command, err, bytes.TrimSpace(output))
	}
	return nil
}
