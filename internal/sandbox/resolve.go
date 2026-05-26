package sandbox

import (
	"os"
	"strings"
)

// systemReadPaths returns the standard OS paths required for process execution.
// Note: /tmp is intentionally excluded — the sandbox has its own tmpDir with
// read+write access. Including /tmp globally would expose other processes' temp files.
func systemReadPaths() []string {
	return []string{
		"/usr",
		"/lib",
		"/lib64",
		"/bin",
		"/sbin",
		"/etc",
		"/dev",
		"/proc",
	}
}

// sanitizePhase replaces slashes with dashes so the phase name is safe
// for use in filesystem paths (e.g. "review/go-specialist" → "review-go-specialist").
func sanitizePhase(phase string) string {
	return strings.ReplaceAll(phase, "/", "-")
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
