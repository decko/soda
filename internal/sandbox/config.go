package sandbox

// Config holds sandbox-level configuration shared across all runs.
type Config struct {
	MemoryMB        uint64   // cgroup memory limit
	CPUPercent      uint32   // cgroup CPU percentage (200 = 2 cores)
	MaxPIDs         uint32   // cgroup max processes
	MaxFileSizeMB   uint64   // prevent disk-fill escape
	UseNetNS        bool     // false for v1 — Claude needs network for API calls
	ExtraReadPaths  []string // additional read-only paths
	ExtraWritePaths []string // additional write paths beyond WorkDir
	ClaudeBinary    string   // empty = exec.LookPath("claude")
	Proxy           ProxyConfig
}

// ProxyConfig holds LLM proxy settings.
// When Enabled, the sandbox runner starts an LLM proxy on a Unix socket,
// routes Claude Code API calls through it (via NetworkProxySocket),
// and enables full network isolation (UseNetNS is forced to true).
type ProxyConfig struct {
	Enabled         bool   // start proxy and enable network isolation
	UpstreamURL     string // real API base URL (default: from ANTHROPIC_BASE_URL or https://api.anthropic.com)
	APIKey          string // API key for upstream; empty = read from ANTHROPIC_API_KEY env
	MaxInputTokens  int64  // per-session input token budget; 0 = unlimited
	MaxOutputTokens int64  // per-session output token budget; 0 = unlimited
	LogDir          string // proxy request/response log directory; empty = no logging
}
