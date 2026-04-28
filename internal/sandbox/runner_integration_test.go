//go:build cgo && integration

package sandbox

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	arapuca "github.com/sergio-correia/go-arapuca"
)

// TestIntegration_FullRunMockProcess spawns /bin/sh inside a real arapuca
// sandbox and verifies that:
//   - environment variables are passed through to the subprocess
//   - stdout is captured correctly via pipes
//   - the process exits cleanly with code 0
func TestIntegration_FullRunMockProcess(t *testing.T) {
	sb, err := arapuca.New()
	if err != nil {
		t.Fatalf("arapuca.New: %v", err)
	}
	defer sb.Close()

	tmpDir, err := arapuca.MakeTmpDir("integration-test")
	if err != nil {
		t.Fatalf("MakeTmpDir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	workDir := t.TempDir()

	// Set up stdout pipe to capture subprocess output.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer stdoutR.Close()
	defer stdoutW.Close()

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	defer stderrR.Close()
	defer stderrW.Close()

	cfg := arapuca.Config{
		Profile: arapuca.Profile{
			ReadPaths:  systemReadPaths(),
			WritePaths: []string{workDir, tmpDir},
		},
		TaskID:  "integration-test",
		Phase:   "test",
		WorkDir: workDir,
		Stdout:  stdoutW,
		Stderr:  stderrW,
		Env: map[string]string{
			"HOME":             tmpDir,
			"TMPDIR":           tmpDir,
			"PATH":             "/usr/local/bin:/usr/bin:/bin",
			"SODA_TEST_MARKER": "hello-from-sandbox",
			"LANG":             "en_US.UTF-8",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run a shell command that echoes the test env var and a fixed string.
	proc, err := sb.Launch(ctx, cfg, "/bin/sh", []string{
		"-c",
		`echo "marker=$SODA_TEST_MARKER" && echo "done"`,
	}, nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Close write ends so readers get EOF after process exits.
	stdoutW.Close()
	stderrW.Close()

	// Drain stdout and stderr concurrently.
	var stdout, stderr bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(&stdout, stdoutR)
	}()
	go func() {
		defer wg.Done()
		io.Copy(&stderr, stderrR)
	}()

	exitCode, waitErr := proc.Wait()
	wg.Wait()
	proc.Cleanup()

	if waitErr != nil {
		t.Fatalf("Wait error: %v (stderr: %s)", waitErr, stderr.String())
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", exitCode, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "marker=hello-from-sandbox") {
		t.Errorf("stdout missing env var marker; got:\n%s", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("stdout missing 'done'; got:\n%s", out)
	}
}
