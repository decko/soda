package claude

import (
	"fmt"
	"strconv"
	"strings"
)

// MinCLIVersion is the minimum Claude Code CLI version that supports all
// flags used by SODA. The bottleneck is --bare, introduced in v2.1.81.
//
// Flag introduction timeline:
//
//	--print                            pre-0.2.x
//	--output-format json               ≤ v0.2.66
//	--output-format stream-json        ≤ v0.2.66 (same flag, JSONL variant)
//	--allowed-tools                    pre-1.0.x
//	--system-prompt-file               v1.0.55
//	--permission-mode bypassPermissions ≤ v2.0.x
//	--max-budget-usd                   v2.0.28
//	--json-schema                      ≤ v2.1.21 (fix in v2.1.22)
//	--bare                             v2.1.81 ← bottleneck
const MinCLIVersion = "2.1.81"

// MaxTestedCLIVersion is the highest Claude Code CLI version that SODA has
// been tested against. Versions above this will still work but produce a
// warning in `soda doctor` so operators know the pairing is unverified.
//
// Bump this constant after validating SODA against a newer CLI release.
// See CONTRIBUTING.md § "Release checklist" for the full procedure.
const MaxTestedCLIVersion = "2.2.0"

func init() {
	if compareCLIVersions(MinCLIVersion, MaxTestedCLIVersion) >= 0 {
		panic(fmt.Sprintf(
			"claude: MinCLIVersion (%s) must be less than MaxTestedCLIVersion (%s)",
			MinCLIVersion, MaxTestedCLIVersion,
		))
	}
}

// compareCLIVersions compares two semver strings (X.Y.Z).
// Returns -1 if a < b, 0 if a == b, +1 if a > b.
// This is intentionally duplicated from cmd/soda/doctor.go's compareSemver
// because internal/claude cannot import cmd/soda.
func compareCLIVersions(a, b string) int {
	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)

	for idx := 0; idx < 3; idx++ {
		ai, bi := 0, 0
		if idx < len(aParts) {
			ai, _ = strconv.Atoi(aParts[idx])
		}
		if idx < len(bParts) {
			bi, _ = strconv.Atoi(bParts[idx])
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// BuildArgs constructs the CLI argument list from RunOpts and model.
// When opts.Model is non-empty it takes precedence over the runner-level
// model, enabling per-phase and per-reviewer model overrides.
func BuildArgs(opts RunOpts, model string) []string {
	args := []string{
		"--print",
		"--bare",
		"--verbose",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	}

	if opts.SystemPromptPath != "" {
		args = append(args, "--system-prompt-file", opts.SystemPromptPath)
	}
	if opts.SettingsPath != "" {
		args = append(args, "--settings-path", opts.SettingsPath)
	}
	if opts.OutputSchema != "" {
		args = append(args, "--json-schema", opts.OutputSchema)
	}
	// Prefer per-invocation model over runner-level default.
	effectiveModel := model
	if opts.Model != "" {
		effectiveModel = opts.Model
	}
	if effectiveModel != "" {
		args = append(args, "--model", effectiveModel)
	}
	if opts.MaxBudgetUSD != nil {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(*opts.MaxBudgetUSD, 'f', -1, 64))
	}
	for _, tool := range opts.AllowedTools {
		args = append(args, "--allowed-tools", tool)
	}
	// TODO: validate AllowedTools entries against known tool names
	// (Read, Write, Edit, Glob, Grep, Bash, Bash(git:*), etc.)
	// and log warnings for unknown tools. Not blocking — the CLI
	// rejects unknown tools at runtime.

	return args
}
