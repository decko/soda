package pipeline

import (
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
