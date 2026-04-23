package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds the proxy server configuration.
type Config struct {
	SocketPath      string        // Unix socket path to listen on (used when ListenAddr is empty)
	ListenAddr      string        // TCP address to listen on (e.g. "127.0.0.1:0"); takes precedence over SocketPath
	UpstreamURL     string        // Real API base URL
	APIKey          string        // API key to inject into requests (Anthropic direct)
	TokenFunc       func() string // returns a Bearer token (Vertex OAuth); takes precedence over APIKey
	MaxInputTokens  int64         // Budget cap; 0 = unlimited
	MaxOutputTokens int64         // Budget cap; 0 = unlimited
	LogDir          string        // Directory for request/response logs; empty = no logging
}

// Stats holds cumulative metering data.
type Stats struct {
	Requests     int64
	InputTokens  int64
	OutputTokens int64
}

// Proxy is an HTTP reverse proxy that listens on a Unix domain socket,
// injects credentials, meters token usage, and enforces budget limits.
type Proxy struct {
	listener net.Listener
	server   *http.Server
	sockPath string
	config   Config

	// metering
	requests     atomic.Int64
	inputTokens  atomic.Int64
	outputTokens atomic.Int64

	// logging
	logMu  sync.Mutex
	logSeq int64
}

// New creates and starts the proxy server. When ListenAddr is set, the
// proxy listens on TCP; otherwise it listens on the Unix socket at SocketPath.
func New(cfg Config) (*Proxy, error) {
	if cfg.ListenAddr == "" && cfg.SocketPath == "" {
		return nil, fmt.Errorf("proxy: either ListenAddr or SocketPath is required")
	}
	if cfg.UpstreamURL == "" {
		return nil, fmt.Errorf("proxy: upstream URL is required")
	}

	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy: parse upstream URL: %w", err)
	}

	var ln net.Listener
	if cfg.ListenAddr != "" {
		ln, err = net.Listen("tcp", cfg.ListenAddr)
		if err != nil {
			return nil, fmt.Errorf("proxy: listen %s: %w", cfg.ListenAddr, err)
		}
	} else {
		os.Remove(cfg.SocketPath)
		ln, err = net.Listen("unix", cfg.SocketPath)
		if err != nil {
			return nil, fmt.Errorf("proxy: listen %s: %w", cfg.SocketPath, err)
		}
	}

	proxy := &Proxy{
		listener: ln,
		sockPath: cfg.SocketPath,
		config:   cfg,
	}

	reverseProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			req.Host = upstream.Host

			// Prepend the upstream's path prefix (e.g. "/v1") to the
			// incoming request path. This is needed for Vertex AI where
			// the base URL is https://REGION-aiplatform.googleapis.com/v1
			// and the client sends /projects/.../rawPredict.
			if upstream.Path != "" && upstream.Path != "/" {
				req.URL.Path = singleJoiningSlash(upstream.Path, req.URL.Path)
			}

			// Inject credentials — the sandbox sees a fake nonce.
			if cfg.TokenFunc != nil {
				if token := cfg.TokenFunc(); token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
			} else if cfg.APIKey != "" {
				req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
				req.Header.Set("x-api-key", cfg.APIKey)
			}
		},
		ModifyResponse: proxy.meterResponse,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if proxy.overBudget() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"type":    "budget_exceeded",
					"message": fmt.Sprintf("budget exceeded: %d input tokens, %d output tokens used", proxy.inputTokens.Load(), proxy.outputTokens.Load()),
				},
			})
			return
		}
		reverseProxy.ServeHTTP(w, r)
	})

	proxy.server = &http.Server{Handler: mux}
	go proxy.server.Serve(ln)

	return proxy, nil
}

// Stats returns the current cumulative metering data.
func (p *Proxy) Stats() Stats {
	return Stats{
		Requests:     p.requests.Load(),
		InputTokens:  p.inputTokens.Load(),
		OutputTokens: p.outputTokens.Load(),
	}
}

// Addr returns the listener's network address. For TCP listeners,
// use this to discover the assigned port (e.g. when using ":0").
func (p *Proxy) Addr() net.Addr {
	return p.listener.Addr()
}

// Close shuts down the proxy server and removes the socket file (if any).
func (p *Proxy) Close() error {
	err := p.server.Close()
	if p.sockPath != "" {
		os.Remove(p.sockPath)
	}
	return err
}

// overBudget returns true if cumulative token usage exceeds configured limits.
// Checked before forwarding to upstream — prevents the call from being made.
func (p *Proxy) overBudget() bool {
	if p.config.MaxInputTokens > 0 && p.inputTokens.Load() >= p.config.MaxInputTokens {
		return true
	}
	if p.config.MaxOutputTokens > 0 && p.outputTokens.Load() >= p.config.MaxOutputTokens {
		return true
	}
	return false
}

// meterResponse is called by the reverse proxy after receiving the upstream
// response. It reads the response body, extracts token usage, updates
// cumulative counts, optionally logs, and replaces the body for the client.
func (p *Proxy) meterResponse(resp *http.Response) error {
	if resp.Body == nil {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("proxy: read response body: %w", err)
	}

	// Replace the body so the client can read it.
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))

	// Meter tokens from response.
	p.requests.Add(1)
	usage := extractUsage(body)
	p.inputTokens.Add(usage.inputTokens)
	p.outputTokens.Add(usage.outputTokens)

	// Log if configured.
	if p.config.LogDir != "" {
		go p.logEntry(resp.Request, body, usage, resp.StatusCode)
	}

	return nil
}

type tokenUsage struct {
	inputTokens  int64
	outputTokens int64
}

// extractUsage parses the Anthropic API response for token usage.
func extractUsage(body []byte) tokenUsage {
	var resp struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return tokenUsage{}
	}
	return tokenUsage{
		inputTokens:  resp.Usage.InputTokens,
		outputTokens: resp.Usage.OutputTokens,
	}
}

func (p *Proxy) logEntry(req *http.Request, responseBody []byte, usage tokenUsage, statusCode int) {
	p.logMu.Lock()
	p.logSeq++
	seq := p.logSeq
	p.logMu.Unlock()

	bodyExcerpt := string(responseBody)
	if len(bodyExcerpt) > 200 {
		bodyExcerpt = bodyExcerpt[:200]
	}

	entry := map[string]any{
		"seq":           seq,
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
		"method":        req.Method,
		"path":          req.URL.Path,
		"status":        statusCode,
		"input_tokens":  usage.inputTokens,
		"output_tokens": usage.outputTokens,
		"body_excerpt":  bodyExcerpt,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')

	os.MkdirAll(p.config.LogDir, 0755)
	logPath := filepath.Join(p.config.LogDir, "proxy.jsonl")
	fd, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer fd.Close()
	fd.Write(data)
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aSlash := strings.HasSuffix(a, "/")
	bSlash := strings.HasPrefix(b, "/")
	switch {
	case aSlash && bSlash:
		return a + b[1:]
	case !aSlash && !bSlash:
		return a + "/" + b
	}
	return a + b
}
