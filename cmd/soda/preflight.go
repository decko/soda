package main

import (
	"fmt"
	"strings"
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

// runPreflight executes a targeted subset of the doctor checks that are
// prerequisites for running a pipeline. It fails fast with actionable
// errors before any expensive work (ticket fetching, worktree setup, etc.)
// begins.
//
// When useMock is true, Claude CLI checks are skipped because the mock
// runner doesn't invoke Claude.
func runPreflight(env *doctorEnv, useMock bool) error {
	checks := []func(*doctorEnv) checkResult{
		checkGit,
		checkGitRepo,
	}

	if !useMock {
		checks = append(checks, checkClaude, checkClaudeVersion)
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
