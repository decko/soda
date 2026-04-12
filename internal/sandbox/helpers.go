package sandbox

import (
	"io"
	"os"
	"strconv"
	"strings"
)

// setEnvForLaunch temporarily sets environment variables for a sandboxed
// process launch. arapuca inherits the calling process's env, so we set
// vars temporarily. Pipeline phases run sequentially, so this is safe.
//
// Returns a restore function that reverts all changes.
func setEnvForLaunch(env []string) (restore func()) {
	saved := make(map[string]string)
	for _, entry := range env {
		key, val, _ := parseEnvEntry(entry)
		saved[key] = os.Getenv(key)
		os.Setenv(key, val)
	}
	return func() {
		for key, val := range saved {
			if val == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, val)
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
func parseSignalFromError(err error) int {
	msg := err.Error()
	const prefix = "killed by signal "
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return 0
	}
	numStr := msg[idx+len(prefix):]
	sig, parseErr := strconv.Atoi(numStr)
	if parseErr != nil {
		return 0
	}
	return sig
}

// limitWriter wraps an io.Writer with a byte limit.
// Write always reports success (returning len(p)) so pipe draining is never blocked.
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
	n, err := lw.writer.Write(toWrite)
	lw.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	// Always report full length to avoid blocking pipe draining.
	return len(p), nil
}
