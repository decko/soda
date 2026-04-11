package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// Ensure mock script is executable
	os.Chmod("testdata/mock_claude.sh", 0755)
	os.Exit(m.Run())
}

func mockBinaryPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/mock_claude.sh")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestNewRunner(t *testing.T) {
	t.Run("valid_binary", func(t *testing.T) {
		runner, err := NewRunner(mockBinaryPath(t), "test-model", t.TempDir())
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		if runner.version != "claude-code 1.0.0-test" {
			t.Errorf("version = %q, want %q", runner.version, "claude-code 1.0.0-test")
		}
	})

	t.Run("missing_binary", func(t *testing.T) {
		_, err := NewRunner("/nonexistent/path/claude", "model", t.TempDir())
		if err == nil {
			t.Fatal("expected error for missing binary")
		}
	})

	t.Run("relative_workdir_rejected", func(t *testing.T) {
		_, err := NewRunner(mockBinaryPath(t), "model", "relative/path")
		if err == nil {
			t.Fatal("expected error for relative workDir")
		}
	})
}

func TestLimitedBuffer(t *testing.T) {
	t.Run("within_limit", func(t *testing.T) {
		lb := &limitedBuffer{max: 100}
		lb.Write([]byte("hello"))
		lb.Write([]byte(" world"))
		if lb.Len() != 11 {
			t.Errorf("Len = %d, want 11", lb.Len())
		}
		if lb.overflow {
			t.Error("should not overflow")
		}
	})

	t.Run("exceeds_limit", func(t *testing.T) {
		lb := &limitedBuffer{max: 10}
		lb.Write([]byte("12345"))
		lb.Write([]byte("67890abc")) // would exceed 10
		if !lb.overflow {
			t.Error("should overflow")
		}
		// Buffer contains data up to the limit
		if lb.Len() > 10 {
			t.Errorf("Len = %d, should be <= 10", lb.Len())
		}
	})

	t.Run("write_returns_full_length", func(t *testing.T) {
		lb := &limitedBuffer{max: 5}
		n, err := lb.Write([]byte("1234567890"))
		if err != nil {
			t.Errorf("Write error: %v", err)
		}
		if n != 10 {
			t.Errorf("Write returned %d, want 10", n)
		}
	})
}
