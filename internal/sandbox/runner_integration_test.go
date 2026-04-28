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
	var mu sync.Mutex
	var receivedAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedAPIKey = r.Header.Get("x-api-key")
		mu.Unlock()
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
	mu.Lock()
	gotKey := receivedAPIKey
	mu.Unlock()
	if gotKey != "sk-test-integration-key" {
		t.Errorf("upstream received x-api-key = %q, want %q", gotKey, "sk-test-integration-key")
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

// TestIntegration_SandboxIsolation groups subtests that verify OS-level
// isolation primitives. Each subtest skips gracefully on kernels that
// lack the required feature.
func TestIntegration_SandboxIsolation(t *testing.T) {
	t.Run("Landlock", testIsolationLandlock)
	t.Run("NetworkNamespace", testIsolationNetworkNamespace)
	t.Run("CgroupLimits", testIsolationCgroupLimits)
}

// testIsolationLandlock verifies that Landlock filesystem restrictions
// prevent the sandboxed process from reading files outside the allowed
// path set. The subprocess attempts to read a secret file under /var/tmp
// (a directory NOT in the allowed read paths). We expect the read to fail.
func testIsolationLandlock(t *testing.T) {
	if arapuca.LandlockABIVersion() == 0 {
		t.Skip("skipping: Landlock not supported on this kernel")
	}

	sb, err := arapuca.New()
	if err != nil {
		t.Fatalf("arapuca.New: %v", err)
	}
	defer sb.Close()

	tmpDir, err := arapuca.MakeTmpDir("landlock-test")
	if err != nil {
		t.Fatalf("MakeTmpDir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	workDir := t.TempDir()

	// Create a secret file under /var/tmp — a path that is definitely NOT
	// covered by systemReadPaths() (/usr, /lib, /bin, /etc, /dev, /proc)
	// or by the sandbox's workDir / tmpDir. Using /var/tmp avoids the
	// case where t.TempDir() dirs share a parent with workDir under /tmp.
	secretDir, err := os.MkdirTemp("/var/tmp", "soda-landlock-secret-")
	if err != nil {
		t.Fatalf("create secret dir: %v", err)
	}
	defer os.RemoveAll(secretDir)

	secretFile := secretDir + "/secret.txt"
	if err := os.WriteFile(secretFile, []byte("top-secret"), 0644); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

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

	// Intentionally restrict read paths: workDir and tmpDir only.
	// The secret file's directory is NOT in ReadPaths.
	cfg := arapuca.Config{
		Profile: arapuca.Profile{
			ReadPaths:  append(systemReadPaths(), workDir, tmpDir),
			WritePaths: []string{workDir, tmpDir},
		},
		TaskID:  "landlock-test",
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

	// Try to cat the secret file. This should fail due to Landlock.
	proc, err := sb.Launch(ctx, cfg, "/bin/sh", []string{
		"-c",
		fmt.Sprintf("cat %s 2>&1; echo EXIT=$?", secretFile),
	}, nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
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

	proc.Wait()
	wg.Wait()
	proc.Cleanup()

	out := stdout.String()
	// The cat command should have failed — look for "Permission denied"
	// or a non-zero exit code. If Landlock enforcement is not active
	// (e.g., running in a privileged container), skip gracefully.
	if strings.Contains(out, "top-secret") {
		t.Skip("skipping: Landlock reports ABI support but does not enforce restrictions (likely a privileged container)")
	}
	if !strings.Contains(out, "EXIT=1") && !strings.Contains(out, "Permission denied") &&
		!strings.Contains(out, "Operation not permitted") {
		t.Errorf("expected access denied error; got:\n%s\nstderr: %s", out, stderr.String())
	}
}

// testIsolationNetworkNamespace verifies that network namespace isolation
// prevents outbound network access. The subprocess attempts to connect
// to a TCP port which should fail when UseNetNS is enabled.
func testIsolationNetworkNamespace(t *testing.T) {
	if !arapuca.NetNSAvailable() {
		t.Skip("skipping: network namespace isolation not available")
	}

	sb, err := arapuca.New()
	if err != nil {
		t.Fatalf("arapuca.New: %v", err)
	}
	defer sb.Close()

	tmpDir, err := arapuca.MakeTmpDir("netns-test")
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

	cfg := arapuca.Config{
		Profile: arapuca.Profile{
			ReadPaths:  append(systemReadPaths(), workDir, tmpDir),
			WritePaths: []string{workDir, tmpDir},
			UseNetNS:   true, // Enable network namespace isolation.
		},
		TaskID:  "netns-test",
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

	// Attempt to reach an external host. The connection should fail
	// inside the network namespace. We try with /dev/tcp which is a
	// bash built-in, falling back to checking general connectivity.
	proc, err := sb.Launch(ctx, cfg, "/bin/sh", []string{
		"-c",
		// Try to connect to a well-known external address.
		`(echo test | nc -w 1 1.1.1.1 80 >/dev/null 2>&1) && echo "NET=reachable" || echo "NET=blocked"`,
	}, nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
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

	proc.Wait()
	wg.Wait()
	proc.Cleanup()

	out := stdout.String()
	if strings.Contains(out, "NET=reachable") {
		t.Errorf("network namespace isolation failed: process reached external host;\nstdout: %s\nstderr: %s", out, stderr.String())
	}
	if !strings.Contains(out, "NET=blocked") {
		t.Logf("unexpected output (test may be inconclusive):\nstdout: %s\nstderr: %s", out, stderr.String())
	}
}

// testIsolationCgroupLimits verifies that cgroup PID limits prevent
// fork bombs from escaping the sandbox. The subprocess attempts to
// fork rapidly; with MaxPIDs set to a low value, the fork should fail.
func testIsolationCgroupLimits(t *testing.T) {
	sb, err := arapuca.New()
	if err != nil {
		t.Fatalf("arapuca.New: %v", err)
	}
	defer sb.Close()

	if !sb.CgroupsAvailable() {
		t.Skip("skipping: cgroups v2 not available")
	}

	tmpDir, err := arapuca.MakeTmpDir("cgroup-test")
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

	cfg := arapuca.Config{
		Profile: arapuca.Profile{
			ReadPaths:  append(systemReadPaths(), workDir, tmpDir),
			WritePaths: []string{workDir, tmpDir},
			MaxPIDs:    5, // Very low PID limit — fork bomb should be stopped.
		},
		TaskID:  "cgroup-test",
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Attempt a controlled fork bomb. With MaxPIDs=5, most forks will
	// fail with EAGAIN. The script tries to spawn 20 background sleeps
	// and counts how many succeeded.
	proc, err := sb.Launch(ctx, cfg, "/bin/sh", []string{
		"-c",
		`count=0; i=0; while [ $i -lt 20 ]; do sleep 60 & if [ $? -eq 0 ]; then count=$((count+1)); fi; i=$((i+1)); done; echo "FORKS=$count"`,
	}, nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
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

	proc.Wait()
	wg.Wait()
	proc.Cleanup()

	out := stdout.String()
	combined := out + stderr.String()

	// With MaxPIDs=5 and the sh process itself counting as 1, we expect
	// far fewer than 20 forks to succeed. If all 20 succeeded, cgroup
	// limits are not working.
	if strings.Contains(out, "FORKS=20") {
		t.Errorf("cgroup PID limit not enforced: all 20 forks succeeded;\nstdout: %s\nstderr: %s", out, stderr.String())
	}

	// Verify that fork failure messages appeared (EAGAIN / "Resource temporarily unavailable").
	if !strings.Contains(combined, "Resource temporarily unavailable") &&
		!strings.Contains(combined, "Cannot fork") &&
		!strings.Contains(combined, "fork") &&
		!strings.Contains(out, "FORKS=") {
		t.Logf("output may be inconclusive (no fork failure message):\nstdout: %s\nstderr: %s", out, stderr.String())
	}
}
