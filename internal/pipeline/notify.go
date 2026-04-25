package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// NotifyConfig configures notification hooks fired on pipeline completion.
// Both Webhook and Script may be set; they fire independently.
// Mirrors config.NotifyConfig — kept separate to avoid cross-package imports.
type NotifyConfig struct {
	Webhook   *WebhookNotifyConfig // fires on any completion
	Script    *ScriptNotifyConfig  // fires on any completion
	OnFinish  *NotifyHookConfig    // fires on any completion
	OnFailure *NotifyHookConfig    // fires only on failed or timeout
}

// NotifyHookConfig groups a webhook and script for a single trigger condition.
type NotifyHookConfig struct {
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
const defaultNotifyTimeout = 10 * time.Second

// Notifier sends pipeline completion notifications via webhook and/or script.
type Notifier struct {
	config     NotifyConfig
	httpClient *http.Client
	timeout    time.Duration
}

// NewNotifier creates a Notifier. Returns nil if no notifications are configured.
func NewNotifier(cfg NotifyConfig) *Notifier {
	if cfg.Webhook == nil && cfg.Script == nil && cfg.OnFinish == nil && cfg.OnFailure == nil {
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

	// OnFinish fires on any completion.
	if n.config.OnFinish != nil {
		if n.config.OnFinish.Webhook != nil && n.config.OnFinish.Webhook.URL != "" {
			if err := n.sendWebhookTo(ctx, n.config.OnFinish.Webhook, payload); err != nil {
				errs = append(errs, fmt.Errorf("on_finish webhook: %w", err))
			}
		}
		if n.config.OnFinish.Script != nil && n.config.OnFinish.Script.Command != "" {
			if err := n.runScriptCmd(ctx, n.config.OnFinish.Script.Command, payload); err != nil {
				errs = append(errs, fmt.Errorf("on_finish script: %w", err))
			}
		}
	}

	// OnFailure fires only on failed or timeout status.
	if n.config.OnFailure != nil && (result.Status == "failed" || result.Status == "timeout") {
		if n.config.OnFailure.Webhook != nil && n.config.OnFailure.Webhook.URL != "" {
			if err := n.sendWebhookTo(ctx, n.config.OnFailure.Webhook, payload); err != nil {
				errs = append(errs, fmt.Errorf("on_failure webhook: %w", err))
			}
		}
		if n.config.OnFailure.Script != nil && n.config.OnFailure.Script.Command != "" {
			if err := n.runScriptCmd(ctx, n.config.OnFailure.Script.Command, payload); err != nil {
				errs = append(errs, fmt.Errorf("on_failure script: %w", err))
			}
		}
	}

	if len(errs) == 1 {
		return fmt.Errorf("notify: %w", errs[0])
	}
	if len(errs) > 1 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return fmt.Errorf("notify: multiple errors: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// sendWebhook sends an HTTP POST with the JSON payload to the configured URL.
func (n *Notifier) sendWebhook(ctx context.Context, payload []byte) error {
	return n.sendWebhookTo(ctx, n.config.Webhook, payload)
}

// sendWebhookTo sends an HTTP POST with the JSON payload to the given webhook config.
func (n *Notifier) sendWebhookTo(ctx context.Context, wh *WebhookNotifyConfig, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	for k, v := range wh.Headers {
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
// The command is split into binary + args and executed directly without a shell.
func (n *Notifier) runScript(ctx context.Context, payload []byte) error {
	return n.runScriptCmd(ctx, n.config.Script.Command, payload)
}

// runScriptCmd executes the given command string with the JSON payload on stdin.
func (n *Notifier) runScriptCmd(ctx context.Context, command string, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	parts := strings.Fields(command)
	if len(parts) == 0 {
		return fmt.Errorf("script command is empty")
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Stdin = bytes.NewReader(payload)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("execute %q: %w (stderr: %s)", command, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}
