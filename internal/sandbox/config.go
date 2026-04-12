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
}
