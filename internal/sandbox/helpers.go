package sandbox

import (
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// launchMu serializes env mutation + subprocess launch to prevent
// races if multiple Runners are ever used concurrently.
var launchMu sync.Mutex

// savedEnvEntry records the previous state of an environment variable.
type savedEnvEntry struct {
	value string
	found bool
}

// setEnvForLaunch temporarily sets environment variables for a sandboxed
// process launch. arapuca inherits the calling process's env, so we set
// vars temporarily.
//
// Uses os.LookupEnv to correctly distinguish "unset" from "set to empty".
// Returns a restore function that reverts all changes.
func setEnvForLaunch(env []string) (restore func()) {
	saved := make(map[string]savedEnvEntry)
	for _, entry := range env {
		key, val, _ := parseEnvEntry(entry)
		oldVal, found := os.LookupEnv(key)
		saved[key] = savedEnvEntry{value: oldVal, found: found}
		os.Setenv(key, val)
	}
	return func() {
		for key, entry := range saved {
			if !entry.found {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, entry.value)
			}
		}
	}
}

func parseEnvEntry(entry string) (key, val string, ok bool) {
	idx := strings.IndexByte(entry, '=')
	if idx < 0 {
		return entry, "", false
	}
	return entry[:idx], entry[idx+1:], true
}

// parseSignalFromError extracts the signal number from an arapuca wait error.
// arapuca returns "killed by signal N" — extract N.
// Scans only leading digits after the prefix to be robust against wrapped errors.
func parseSignalFromError(err error) int {
	msg := err.Error()
	const prefix = "killed by signal "
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return 0
	}
	numStr := msg[idx+len(prefix):]
	// Scan only leading digits — error message may have trailing content
	// if wrapped (e.g., "arapuca: killed by signal 9: context canceled").
	end := 0
	for end < len(numStr) && numStr[end] >= '0' && numStr[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	sig, parseErr := strconv.Atoi(numStr[:end])
	if parseErr != nil {
		return 0
	}
	return sig
}

// limitWriter wraps an io.Writer with a byte limit.
// Write always reports len(p), nil so pipe draining is never blocked,
// even when the underlying write is truncated or fails.
type limitWriter struct {
	writer    io.Writer
	remaining int64
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		return len(p), nil // silently discard
	}
	toWrite := p
	if int64(len(toWrite)) > lw.remaining {
		toWrite = toWrite[:lw.remaining]
	}
	n, _ := lw.writer.Write(toWrite)
	lw.remaining -= int64(n)
	// Always report full length to avoid blocking pipe draining.
	return len(p), nil
}
