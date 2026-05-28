package main

import (
	"fmt"
	"strings"

	"github.com/decko/soda/internal/runner"
)

// PreflightError is returned when one or more prerequisite checks fail
// before the pipeline starts. It aggregates all failures into a single
// error with actionable fix suggestions.
type PreflightError struct {
	Failures []checkResult
}

func (e *PreflightError) Error() string {
	var b strings.Builder
	b.WriteString("preflight check failed:\n")
	for _, f := range e.Failures {
		b.WriteString(fmt.Sprintf("  ✗ %s: %s\n", f.name, f.detail))
		if f.fix != "" {
			b.WriteString(fmt.Sprintf("    fix: %s\n", f.fix))
		}
	}
	b.WriteString("\nRun 'soda doctor' for full diagnostics")
	return b.String()
}

// checkRunnerBinary verifies that the binary for a non-claude runner is
// available in PATH. On failure it builds a fix string from the agent
// registry install hint.
func checkRunnerBinary(env *doctorEnv, runnerName, binaryName string) checkResult {
	if binaryName == "" {
		info := runner.AgentByName(runnerName)
		if info != nil {
			binaryName = info.Binary
		} else {
			binaryName = runnerName
		}
	}

	path, err := env.LookPath(binaryName)
	if err != nil {
		var fixParts []string
		info := runner.AgentByName(runnerName)
		hint := runner.InstallHint(info)
		if hint != "" {
			fixParts = append(fixParts, hint)
		}
		fixParts = append(fixParts, "or change runner in soda.yaml")
		fixParts = append(fixParts, "Run 'soda doctor' for full diagnostics")
		return checkResult{
			name:     runnerName,
			passed:   false,
			required: true,
			detail:   fmt.Sprintf("%s not found in PATH", binaryName),
			fix:      strings.Join(fixParts, "; "),
		}
	}
	return checkResult{
		name:     runnerName,
		passed:   true,
		required: true,
		detail:   path,
	}
}

// runPreflight executes a targeted subset of the doctor checks that are
// prerequisites for running a pipeline. It fails fast with actionable
// errors before any expensive work (ticket fetching, worktree setup, etc.)
// begins.
//
// When useMock is true, Claude CLI checks are skipped because the mock
// runner doesn't invoke Claude. When runnerName is "pi" or "opencode",
// Claude CLI checks are also skipped because those runners don't invoke
// the Claude Code CLI; instead the runner-specific binary is checked.
func runPreflight(env *doctorEnv, useMock bool, runnerName string) error {
	return runPreflightFull(env, useMock, runnerName, "", false)
}

// runPreflightFull is the extended version of runPreflight that accepts
// a binaryOverride for the runner binary (e.g. from cfg.Pi.Binary) and
// sandboxEnabled to gate the arapuca wrapper check.
func runPreflightFull(env *doctorEnv, useMock bool, runnerName string, binaryOverride string, sandboxEnabled bool) error {
	checks := []func(*doctorEnv) checkResult{
		checkGit,
		checkGitRepo,
	}

	if !useMock {
		switch runnerName {
		case "pi", "opencode":
			// Check the runner-specific binary instead of claude.
			binaryName := binaryOverride
			rName := runnerName
			checks = append(checks, func(env *doctorEnv) checkResult {
				return checkRunnerBinary(env, rName, binaryName)
			})
		default:
			checks = append(checks, checkClaude, checkClaudeVersion)
		}
	}

	if sandboxEnabled && !useMock {
		checks = append(checks, func(env *doctorEnv) checkResult {
			return arapucaWrapperCheck(env)
		})
	}

	// Config checks (checkConfig, checkConfigValid) are intentionally
	// omitted here. loadConfig already validated the configuration
	// (respecting --config) before runPipeline was called, so
	// re-checking from default paths would incorrectly reject valid
	// --config overrides.

	var failures []checkResult
	for _, check := range checks {
		result := check(env)
		if result.skipped {
			continue
		}
		if !result.passed && result.required {
			failures = append(failures, result)
		}
	}

	if len(failures) > 0 {
		return &PreflightError{Failures: failures}
	}
	return nil
}
