package sandbox

import "github.com/decko/soda/internal/runner"

// AgentAdapter abstracts agent-specific concerns (argument building, output
// parsing, environment construction) so the sandbox runner stays agnostic.
type AgentAdapter interface {
	Binary() string
	BuildArgs(opts runner.RunOpts, tmpDir string) ([]string, error)
	BuildEnv(opts runner.RunOpts, tmpDir string, proxyURL string) []string
	ParseOutput(stdout []byte, opts runner.RunOpts) (*runner.RunResult, error)
	ExtraPaths(opts runner.RunOpts) (read []string, write []string)
	MCPExtraPaths(servers map[string]runner.MCPServerConfig) (read []string, write []string)
}
