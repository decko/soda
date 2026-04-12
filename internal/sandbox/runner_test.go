package sandbox

import (
	"os"
	"testing"
)

func TestExitErrorMessages(t *testing.T) {
	tests := []struct {
		name    string
		err     ExitError
		wantMsg string
	}{
		{
			name:    "oom_kill",
			err:     ExitError{OOMKill: true, Signal: 9},
			wantMsg: "sandbox: process OOM killed (signal 9)",
		},
		{
			name:    "signal_kill",
			err:     ExitError{Signal: 15},
			wantMsg: "sandbox: process killed by signal 15",
		},
		{
			name:    "exit_code",
			err:     ExitError{Code: 1},
			wantMsg: "sandbox: process exited with code 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestParseSignalFromError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want int
	}{
		{"sigkill", "killed by signal 9", 9},
		{"sigterm", "killed by signal 15", 15},
		{"no_match", "process exited normally", 0},
		{"partial", "killed by signal", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &testError{msg: tt.msg}
			if got := parseSignalFromError(err); got != tt.want {
				t.Errorf("parseSignalFromError(%q) = %d, want %d", tt.msg, got, tt.want)
			}
		})
	}
}

func TestParseEnvEntry(t *testing.T) {
	tests := []struct {
		entry   string
		wantKey string
		wantVal string
		wantOK  bool
	}{
		{"FOO=bar", "FOO", "bar", true},
		{"PATH=/a:/b:/c", "PATH", "/a:/b:/c", true},
		{"EMPTY=", "EMPTY", "", true},
		{"NOEQUALS", "NOEQUALS", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			key, val, ok := parseEnvEntry(tt.entry)
			if key != tt.wantKey || val != tt.wantVal || ok != tt.wantOK {
				t.Errorf("parseEnvEntry(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.entry, key, val, ok, tt.wantKey, tt.wantVal, tt.wantOK)
			}
		})
	}
}

func TestSetEnvForLaunch(t *testing.T) {
	original := os.Getenv("PATH")
	testEnv := []string{
		"SODA_TEST_VAR=hello",
		"PATH=/custom/path",
	}

	restore := setEnvForLaunch(testEnv)

	if got := os.Getenv("SODA_TEST_VAR"); got != "hello" {
		t.Errorf("SODA_TEST_VAR = %q, want hello", got)
	}
	if got := os.Getenv("PATH"); got != "/custom/path" {
		t.Errorf("PATH = %q, want /custom/path", got)
	}

	restore()

	if got := os.Getenv("SODA_TEST_VAR"); got != "" {
		t.Errorf("SODA_TEST_VAR = %q after restore, want empty", got)
	}
	if got := os.Getenv("PATH"); got != original {
		t.Errorf("PATH = %q after restore, want %q", got, original)
	}
}

func TestLimitWriter(t *testing.T) {
	tests := []struct {
		name     string
		limit    int64
		writes   []string
		wantData string
	}{
		{
			name:     "within_limit",
			limit:    100,
			writes:   []string{"hello", " world"},
			wantData: "hello world",
		},
		{
			name:     "at_limit",
			limit:    5,
			writes:   []string{"hello"},
			wantData: "hello",
		},
		{
			name:     "over_limit_single_write",
			limit:    5,
			writes:   []string{"hello world"},
			wantData: "hello",
		},
		{
			name:     "over_limit_multi_write",
			limit:    8,
			writes:   []string{"hello", " world"},
			wantData: "hello wo",
		},
		{
			name:     "zero_limit",
			limit:    0,
			writes:   []string{"hello"},
			wantData: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf testBuffer
			lw := &limitWriter{writer: &buf, remaining: tt.limit}

			for _, write := range tt.writes {
				n, err := lw.Write([]byte(write))
				if err != nil {
					t.Fatalf("Write(%q) error: %v", write, err)
				}
				if n != len(write) {
					t.Errorf("Write(%q) = %d, want %d", write, n, len(write))
				}
			}

			if got := buf.String(); got != tt.wantData {
				t.Errorf("buffer = %q, want %q", got, tt.wantData)
			}
		})
	}
}

// testError implements error for testing parseSignalFromError.
type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// testBuffer is a simple byte accumulator for testing limitWriter.
type testBuffer struct {
	data []byte
}

func (b *testBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *testBuffer) String() string { return string(b.data) }
