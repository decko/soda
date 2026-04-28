//go:build cgo && integration

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decko/soda/internal/proxy"
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

// TestIntegration_ProxyRoundTrip starts a mock HTTP upstream (simulating the
// Anthropic API), a soda proxy in front of it, then launches sandboxed curl
// to hit the proxy. It verifies:
//   - the response body round-trips from upstream → proxy → sandbox process
//   - the proxy injects the API key (x-api-key header) into upstream requests
//   - token metering counts are updated from the response usage field
func TestIntegration_ProxyRoundTrip(t *testing.T) {
	// Verify curl is available.
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("skipping: curl not found in PATH")
	}

	// Mock upstream: echoes back a response with usage for token metering.
	var receivedAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":    "msg_test123",
			"type":  "message",
			"model": "claude-sonnet-4-20250514",
			"content": []map[string]string{
				{"type": "text", "text": "Hello from mock upstream"},
			},
			"usage": map[string]int{
				"input_tokens":  42,
				"output_tokens": 17,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	// Start the soda proxy pointing at the mock upstream.
	llmProxy, err := proxy.New(proxy.Config{
		ListenAddr:      "127.0.0.1:0",
		UpstreamURL:     upstream.URL,
		APIKey:          "sk-test-integration-key",
		MaxInputTokens:  100_000,
		MaxOutputTokens: 50_000,
	})
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}
	defer llmProxy.Close()

	proxyAddr := llmProxy.Addr().String()
	proxyURL := fmt.Sprintf("http://%s/v1/messages", proxyAddr)

	// Create sandbox and launch curl inside it.
	sb, err := arapuca.New()
	if err != nil {
		t.Fatalf("arapuca.New: %v", err)
	}
	defer sb.Close()

	tmpDir, err := arapuca.MakeTmpDir("proxy-test")
	if err != nil {
		t.Fatalf("MakeTmpDir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	workDir := t.TempDir()

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

	// Curl needs read access to its own binary directory and CA certs.
	readPaths := systemReadPaths()
	readPaths = append(readPaths, workDir, tmpDir)

	cfg := arapuca.Config{
		Profile: arapuca.Profile{
			ReadPaths:  readPaths,
			WritePaths: []string{workDir, tmpDir},
		},
		TaskID:  "proxy-test",
		Phase:   "test",
		WorkDir: workDir,
		Stdout:  stdoutW,
		Stderr:  stderrW,
		Env: map[string]string{
			"HOME":   tmpDir,
			"TMPDIR": tmpDir,
			"PATH":   "/usr/local/bin:/usr/bin:/bin",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// POST a minimal request body to the proxy endpoint.
	requestBody := `{"model":"claude-sonnet-4-20250514","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
	proc, err := sb.Launch(ctx, cfg, curlPath, []string{
		"-s",         // silent mode
		"-X", "POST", // HTTP method
		"-H", "Content-Type: application/json",
		"-H", "x-api-key: sk-proxy-nonce", // fake key (proxy replaces it)
		"-d", requestBody,
		proxyURL,
	}, nil)
	if err != nil {
		t.Fatalf("Launch curl: %v", err)
	}

	stdoutW.Close()
	stderrW.Close()

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
		t.Fatalf("curl exit code = %d, want 0 (stderr: %s)", exitCode, stderr.String())
	}

	// Verify response body round-tripped from mock upstream.
	body := stdout.String()
	if !strings.Contains(body, "Hello from mock upstream") {
		t.Errorf("response body missing expected text; got:\n%s", body)
	}
	if !strings.Contains(body, "msg_test123") {
		t.Errorf("response body missing message ID; got:\n%s", body)
	}

	// Verify proxy injected the real API key into the upstream request.
	if receivedAPIKey != "sk-test-integration-key" {
		t.Errorf("upstream received x-api-key = %q, want %q", receivedAPIKey, "sk-test-integration-key")
	}

	// Verify token metering was updated.
	stats := llmProxy.Stats()
	if stats.Requests != 1 {
		t.Errorf("proxy requests = %d, want 1", stats.Requests)
	}
	if stats.InputTokens != 42 {
		t.Errorf("proxy input tokens = %d, want 42", stats.InputTokens)
	}
	if stats.OutputTokens != 17 {
		t.Errorf("proxy output tokens = %d, want 17", stats.OutputTokens)
	}
}
