package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/schemas"
)

func TestTruncateLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		contains string
		notLong  bool
	}{
		{"under_limit", "line1\nline2\nline3", 5, "line1", false},
		{"at_limit", "a\nb\nc", 3, "a\nb\nc", false},
		{"over_limit", "1\n2\n3\n4\n5", 3, "... (truncated)", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateLines(tt.input, tt.max)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("truncateLines() = %q, want to contain %q", got, tt.contains)
			}
			if tt.notLong {
				lines := strings.Split(got, "\n")
				// max lines + 1 for the truncation marker
				if len(lines) > tt.max+1 {
					t.Errorf("truncateLines() has %d lines, want <= %d", len(lines), tt.max+1)
				}
			}
		})
	}
}

func TestExtractReviewFeedback(t *testing.T) {
	t.Run("returns_nil_when_no_review_result", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractReviewFeedback(); fb != nil {
			t.Error("expected nil when no review result exists")
		}
	})

	t.Run("returns_nil_when_verdict_is_pass", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		_ = state.WriteResult("review", json.RawMessage(`{"verdict":"pass","findings":[]}`))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractReviewFeedback(); fb != nil {
			t.Error("expected nil when verdict is pass")
		}
	})

	t.Run("returns_nil_when_verdict_is_pass_with_follow_ups", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		_ = state.WriteResult("review", json.RawMessage(`{"verdict":"pass-with-follow-ups","findings":[{"severity":"minor","issue":"style"}]}`))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractReviewFeedback(); fb != nil {
			t.Error("expected nil when verdict is pass-with-follow-ups")
		}
	})

	t.Run("returns_feedback_when_verdict_is_rework", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		reviewResult := `{
			"verdict": "rework",
			"findings": [
				{"source":"go-specialist","severity":"critical","file":"a.go","line":10,"issue":"nil deref","suggestion":"check nil"},
				{"source":"ai-harness","severity":"major","file":"b.go","line":20,"issue":"missing guard","suggestion":"add guard"},
				{"source":"go-specialist","severity":"minor","file":"c.go","line":30,"issue":"naming","suggestion":"rename"}
			]
		}`
		_ = state.WriteResult("review", json.RawMessage(reviewResult))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractReviewFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for rework verdict")
		}

		if fb.Source != "review" {
			t.Errorf("Source = %q, want %q", fb.Source, "review")
		}
		if fb.Verdict != "rework" {
			t.Errorf("Verdict = %q, want %q", fb.Verdict, "rework")
		}

		// Only critical and major findings should be included (not minor).
		if len(fb.ReviewFindings) != 2 {
			t.Fatalf("ReviewFindings count = %d, want 2", len(fb.ReviewFindings))
		}

		if fb.ReviewFindings[0].Source != "go-specialist" || fb.ReviewFindings[0].Severity != "critical" {
			t.Errorf("first finding = %+v, want go-specialist/critical", fb.ReviewFindings[0])
		}
		if fb.ReviewFindings[1].Source != "ai-harness" || fb.ReviewFindings[1].Severity != "major" {
			t.Errorf("second finding = %+v, want ai-harness/major", fb.ReviewFindings[1])
		}
	})

	t.Run("sort_order_critical_before_major", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		// Input order: major, critical, minor — output should be critical, major
		// (minor excluded). This verifies the severity sort.
		reviewResult := `{
			"verdict": "rework",
			"findings": [
				{"source":"a","severity":"major","issue":"second priority"},
				{"source":"b","severity":"critical","issue":"top priority"},
				{"source":"c","severity":"minor","issue":"excluded"}
			]
		}`
		_ = state.WriteResult("review", json.RawMessage(reviewResult))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractReviewFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for rework verdict")
		}

		if len(fb.ReviewFindings) != 2 {
			t.Fatalf("ReviewFindings count = %d, want 2", len(fb.ReviewFindings))
		}
		if fb.ReviewFindings[0].Severity != "critical" {
			t.Errorf("first finding severity = %q, want critical", fb.ReviewFindings[0].Severity)
		}
		if fb.ReviewFindings[1].Severity != "major" {
			t.Errorf("second finding severity = %q, want major", fb.ReviewFindings[1].Severity)
		}
	})

	t.Run("budget_priority_critical_gets_full_file", func(t *testing.T) {
		workDir := t.TempDir()
		stateDir := t.TempDir()

		// Create 3 critical files, each exactly criticalFindingCapBytes (10KB).
		// Combined they consume 3×10KB = 30KB = maxFeedbackContextBytes,
		// fully exhausting the budget so the major finding is forced into
		// snippet fallback.
		criticalNames := []string{"crit1.go", "crit2.go", "crit3.go"}
		criticalContents := make([]string, 3)
		for i, name := range criticalNames {
			content := strings.Repeat(string(rune('a'+i)), criticalFindingCapBytes-1) + "\n"
			criticalContents[i] = content
			if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}

		// Create a large major file (~25KB, 500 lines). After the per-finding
		// cap (5KB) the effective content won't fit in the remaining budget
		// (0 bytes), so the major finding must fall back to a ±10 line snippet
		// around line 250.
		var majorLines []string
		for i := 0; i < 500; i++ {
			majorLines = append(majorLines, strings.Repeat("x", 50))
		}
		majorContent := strings.Join(majorLines, "\n")
		if err := os.WriteFile(filepath.Join(workDir, "major.go"), []byte(majorContent), 0644); err != nil {
			t.Fatal(err)
		}

		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		// Input order: major first, then critical — sort must reorder so
		// critical findings consume budget first.
		reviewResult := `{
			"verdict": "rework",
			"findings": [
				{"source":"a","severity":"major","file":"major.go","line":250,"issue":"needs work"},
				{"source":"b","severity":"critical","file":"crit1.go","line":1,"issue":"nil deref 1"},
				{"source":"c","severity":"critical","file":"crit2.go","line":1,"issue":"nil deref 2"},
				{"source":"d","severity":"critical","file":"crit3.go","line":1,"issue":"nil deref 3"}
			]
		}`
		_ = state.WriteResult("review", json.RawMessage(reviewResult))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{WorkDir: workDir}}
		fb := engine.extractReviewFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for rework verdict")
		}

		if len(fb.ReviewFindings) != 4 {
			t.Fatalf("ReviewFindings count = %d, want 4", len(fb.ReviewFindings))
		}

		// All 3 critical findings (sorted first) should get their full file content.
		for i := 0; i < 3; i++ {
			if fb.ReviewFindings[i].Severity != "critical" {
				t.Fatalf("finding[%d] severity = %q, want critical", i, fb.ReviewFindings[i].Severity)
			}
			if fb.ReviewFindings[i].CodeSnippet != criticalContents[i] {
				t.Errorf("critical finding[%d] should get full file (%d bytes), got %d bytes",
					i, len(criticalContents[i]), len(fb.ReviewFindings[i].CodeSnippet))
			}
		}

		// Major finding (last, after budget exhausted by 3 critical files)
		// should get a ±10 line snippet around line 250, NOT the 5KB capped
		// content from the top of the file.
		if fb.ReviewFindings[3].Severity != "major" {
			t.Fatalf("finding[3] severity = %q, want major", fb.ReviewFindings[3].Severity)
		}
		majorSnippet := fb.ReviewFindings[3].CodeSnippet
		if majorSnippet == "" {
			t.Fatal("major finding should get snippet fallback, not empty")
		}
		// A ±10 line snippet around line 250 of a 500-line file = ~21 lines.
		// If it were capped content (5KB from top of file) it would be ~100 lines.
		snippetLines := strings.Split(majorSnippet, "\n")
		if len(snippetLines) != 21 {
			t.Errorf("major finding snippet should be 21 lines (±10 around line 250), got %d lines", len(snippetLines))
		}
		if majorSnippet == majorContent {
			t.Error("major finding should NOT get full file")
		}
	})
}

func TestExtractVerifyFeedback(t *testing.T) {
	t.Run("returns_nil_when_no_verify_result", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractVerifyFeedback(); fb != nil {
			t.Error("expected nil when no verify result exists")
		}
	})

	t.Run("returns_nil_when_verdict_is_PASS", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("verify")
		_ = state.WriteResult("verify", json.RawMessage(`{"verdict":"PASS","fixes_required":[]}`))
		_ = state.MarkCompleted("verify")

		engine := &Engine{state: state, config: EngineConfig{}}
		if fb := engine.extractVerifyFeedback(); fb != nil {
			t.Error("expected nil when verdict is PASS")
		}
	})

	t.Run("only_critical_and_major_code_issues_included", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("verify")
		verifyResult := `{
			"verdict": "FAIL",
			"fixes_required": ["fix the bug"],
			"code_issues": [
				{"file":"a.go","line":10,"severity":"critical","issue":"nil deref","suggested_fix":"check nil"},
				{"file":"b.go","line":20,"severity":"major","issue":"unchecked error","suggested_fix":"handle err"},
				{"file":"c.go","line":30,"severity":"minor","issue":"naming","suggested_fix":"rename"},
				{"file":"d.go","line":40,"severity":"info","issue":"style","suggested_fix":"reformat"}
			]
		}`
		_ = state.WriteResult("verify", json.RawMessage(verifyResult))
		_ = state.MarkCompleted("verify")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractVerifyFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for FAIL verdict")
		}

		if len(fb.CodeIssues) != 2 {
			t.Fatalf("CodeIssues count = %d, want 2 (only critical+major)", len(fb.CodeIssues))
		}
		if fb.CodeIssues[0].Severity != "critical" || fb.CodeIssues[0].File != "a.go" {
			t.Errorf("first code issue = %+v, want critical/a.go", fb.CodeIssues[0])
		}
		if fb.CodeIssues[1].Severity != "major" || fb.CodeIssues[1].File != "b.go" {
			t.Errorf("second code issue = %+v, want major/b.go", fb.CodeIssues[1])
		}
	})

	t.Run("only_failed_criteria_included", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("verify")
		verifyResult := `{
			"verdict": "FAIL",
			"fixes_required": ["fix tests"],
			"criteria_results": [
				{"criterion":"must compile","passed":true,"evidence":"builds ok"},
				{"criterion":"must pass tests","passed":false,"evidence":"3 tests failed"},
				{"criterion":"must lint","passed":false,"evidence":"lint errors"}
			]
		}`
		_ = state.WriteResult("verify", json.RawMessage(verifyResult))
		_ = state.MarkCompleted("verify")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractVerifyFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for FAIL verdict")
		}

		if len(fb.FailedCriteria) != 2 {
			t.Fatalf("FailedCriteria count = %d, want 2", len(fb.FailedCriteria))
		}
		if fb.FailedCriteria[0].Criterion != "must pass tests" {
			t.Errorf("first criterion = %q, want %q", fb.FailedCriteria[0].Criterion, "must pass tests")
		}
		if fb.FailedCriteria[1].Criterion != "must lint" {
			t.Errorf("second criterion = %q, want %q", fb.FailedCriteria[1].Criterion, "must lint")
		}
	})

	t.Run("only_failed_commands_included_with_truncated_output", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("verify")

		// Build output with more than 50 lines.
		var longOutput strings.Builder
		for i := 1; i <= 60; i++ {
			longOutput.WriteString("error line " + strings.Repeat("x", 5) + "\n")
		}

		verifyResult := `{
			"verdict": "FAIL",
			"fixes_required": ["fix commands"],
			"command_results": [
				{"command":"go test ./...","exit_code":0,"output":"ok","passed":true},
				{"command":"go vet ./...","exit_code":1,"output":"` + strings.ReplaceAll(longOutput.String(), "\n", `\n`) + `","passed":false},
				{"command":"golint","exit_code":2,"output":"lint failure","passed":false}
			]
		}`
		_ = state.WriteResult("verify", json.RawMessage(verifyResult))
		_ = state.MarkCompleted("verify")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractVerifyFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for FAIL verdict")
		}

		if len(fb.FailedCommands) != 2 {
			t.Fatalf("FailedCommands count = %d, want 2 (only failed)", len(fb.FailedCommands))
		}

		// First failed command should have truncated output.
		if !strings.Contains(fb.FailedCommands[0].Output, "... (truncated)") {
			t.Errorf("expected truncated output for long command output, got: %q", fb.FailedCommands[0].Output)
		}
		if fb.FailedCommands[0].Command != "go vet ./..." {
			t.Errorf("first failed command = %q, want %q", fb.FailedCommands[0].Command, "go vet ./...")
		}

		// Second failed command should have full output (short).
		if fb.FailedCommands[1].Command != "golint" {
			t.Errorf("second failed command = %q, want %q", fb.FailedCommands[1].Command, "golint")
		}
		if fb.FailedCommands[1].ExitCode != 2 {
			t.Errorf("second failed command exit code = %d, want 2", fb.FailedCommands[1].ExitCode)
		}
	})

	t.Run("fixes_required_is_populated", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("verify")
		verifyResult := `{
			"verdict": "FAIL",
			"fixes_required": ["fix the nil pointer", "add error handling"]
		}`
		_ = state.WriteResult("verify", json.RawMessage(verifyResult))
		_ = state.MarkCompleted("verify")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractVerifyFeedback()
		if fb == nil {
			t.Fatal("expected non-nil feedback for FAIL verdict")
		}

		if fb.Source != "verify" {
			t.Errorf("Source = %q, want %q", fb.Source, "verify")
		}
		if fb.Verdict != "FAIL" {
			t.Errorf("Verdict = %q, want %q", fb.Verdict, "FAIL")
		}
		if len(fb.FixesRequired) != 2 {
			t.Fatalf("FixesRequired count = %d, want 2", len(fb.FixesRequired))
		}
		if fb.FixesRequired[0] != "fix the nil pointer" {
			t.Errorf("FixesRequired[0] = %q, want %q", fb.FixesRequired[0], "fix the nil pointer")
		}
		if fb.FixesRequired[1] != "add error handling" {
			t.Errorf("FixesRequired[1] = %q, want %q", fb.FixesRequired[1], "add error handling")
		}
	})
}

func TestExtractFeedbackFrom(t *testing.T) {
	t.Run("review_source", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("review")
		_ = state.WriteResult("review", json.RawMessage(`{"verdict":"rework","findings":[{"severity":"critical","issue":"nil deref"}]}`))
		_ = state.MarkCompleted("review")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractFeedbackFrom("review")
		if fb == nil {
			t.Fatal("expected non-nil feedback from review source")
		}
		if fb.Source != "review" {
			t.Errorf("Source = %q, want %q", fb.Source, "review")
		}
	})

	t.Run("verify_source", func(t *testing.T) {
		stateDir := t.TempDir()
		state, _ := LoadOrCreate(stateDir, "TEST-1")
		_ = state.MarkRunning("verify")
		_ = state.WriteResult("verify", json.RawMessage(`{"verdict":"FAIL","fixes_required":["fix it"]}`))
		_ = state.MarkCompleted("verify")

		engine := &Engine{state: state, config: EngineConfig{}}
		fb := engine.extractFeedbackFrom("verify")
		if fb == nil {
			t.Fatal("expected non-nil feedback from verify source")
		}
		if fb.Source != "verify" {
			t.Errorf("Source = %q, want %q", fb.Source, "verify")
		}
	})

	t.Run("unknown_source_returns_nil", func(t *testing.T) {
		engine := &Engine{config: EngineConfig{}}
		fb := engine.extractFeedbackFrom("unknown")
		if fb != nil {
			t.Errorf("expected nil for unknown source, got %+v", fb)
		}
	})
}

func TestSummarizeReviewFindings(t *testing.T) {
	t.Run("includes_critical_and_major", func(t *testing.T) {
		findings := []schemas.ReviewFinding{
			{Severity: "critical", File: "handler.go", Line: 42, Issue: "nil deref"},
			{Severity: "major", File: "util.go", Issue: "unchecked error"},
			{Severity: "minor", File: "style.go", Issue: "unused import"},
		}
		summary := summarizeReviewFindings(findings)
		if !strings.Contains(summary, "nil deref") {
			t.Errorf("summary should contain critical finding, got: %s", summary)
		}
		if !strings.Contains(summary, "unchecked error") {
			t.Errorf("summary should contain major finding, got: %s", summary)
		}
		if strings.Contains(summary, "unused import") {
			t.Errorf("summary should NOT contain minor finding, got: %s", summary)
		}
	})

	t.Run("empty_when_no_critical_or_major", func(t *testing.T) {
		findings := []schemas.ReviewFinding{
			{Severity: "minor", File: "style.go", Issue: "unused import"},
		}
		if summary := summarizeReviewFindings(findings); summary != "" {
			t.Errorf("summary should be empty for minor-only findings, got: %s", summary)
		}
	})

	t.Run("empty_when_no_findings", func(t *testing.T) {
		if summary := summarizeReviewFindings(nil); summary != "" {
			t.Errorf("summary should be empty for nil findings, got: %s", summary)
		}
	})

	t.Run("includes_line_numbers", func(t *testing.T) {
		findings := []schemas.ReviewFinding{
			{Severity: "critical", File: "x.go", Line: 10, Issue: "bad"},
		}
		summary := summarizeReviewFindings(findings)
		if !strings.Contains(summary, "x.go:10") {
			t.Errorf("summary should contain line number, got: %s", summary)
		}
	})
}

func TestSummarizeVerifyFailures(t *testing.T) {
	t.Run("joins_fixes", func(t *testing.T) {
		fixes := []string{"fix A", "fix B"}
		summary := summarizeVerifyFailures(fixes)
		if summary != "fix A; fix B" {
			t.Errorf("summary = %q, want %q", summary, "fix A; fix B")
		}
	})

	t.Run("empty_when_no_fixes", func(t *testing.T) {
		if summary := summarizeVerifyFailures(nil); summary != "" {
			t.Errorf("summary should be empty for nil fixes, got: %s", summary)
		}
	})
}

func TestReadSnippet(t *testing.T) {
	// Helper: create a file with numbered lines.
	writeFile := func(t *testing.T, dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Build a 20-line file for most tests.
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, "line"+strings.Repeat(" ", 0)+string(rune('A'-1+i)))
	}
	content20 := strings.Join(lines, "\n")

	t.Run("line_near_start", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "f.go", content20)
		got := readSnippet(dir, "f.go", 1, 5)
		if got == "" {
			t.Fatal("expected non-empty snippet for line=1")
		}
		// Should start from line 1 (start clamped to 0).
		if !strings.HasPrefix(got, "line") {
			t.Errorf("snippet should start with first line, got: %q", got)
		}
		// Should include lines 1..6 (line-context-1=max(0,-5)=0, line+context=6).
		snippetLines := strings.Split(got, "\n")
		if len(snippetLines) != 6 {
			t.Errorf("snippet lines = %d, want 6", len(snippetLines))
		}
	})

	t.Run("line_near_end", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "f.go", content20)
		got := readSnippet(dir, "f.go", 20, 5)
		if got == "" {
			t.Fatal("expected non-empty snippet for line=20")
		}
		// Should include lines 15..20 (start=14, end=min(25,20)=20).
		snippetLines := strings.Split(got, "\n")
		if len(snippetLines) != 6 {
			t.Errorf("snippet lines = %d, want 6", len(snippetLines))
		}
	})

	t.Run("line_in_middle", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "f.go", content20)
		got := readSnippet(dir, "f.go", 10, 3)
		if got == "" {
			t.Fatal("expected non-empty snippet for line=10")
		}
		// Should include lines 7..13 (start=6, end=13).
		snippetLines := strings.Split(got, "\n")
		if len(snippetLines) != 7 {
			t.Errorf("snippet lines = %d, want 7", len(snippetLines))
		}
	})

	t.Run("file_shorter_than_context_window", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "tiny.go", "only\nthree\nlines")
		got := readSnippet(dir, "tiny.go", 2, 10)
		if got == "" {
			t.Fatal("expected non-empty snippet")
		}
		snippetLines := strings.Split(got, "\n")
		if len(snippetLines) != 3 {
			t.Errorf("snippet lines = %d, want 3 (entire file)", len(snippetLines))
		}
	})

	t.Run("nonexistent_file_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		got := readSnippet(dir, "nope.go", 1, 5)
		if got != "" {
			t.Errorf("expected empty string for nonexistent file, got: %q", got)
		}
	})

	t.Run("line_zero_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "f.go", content20)
		// line=0, context=0 → start = 0-0-1 = -1 → clamped to 0; end = 0+0 = 0; start >= end → ""
		got := readSnippet(dir, "f.go", 0, 0)
		if got != "" {
			t.Errorf("expected empty string for line=0 context=0, got: %q", got)
		}
	})

	t.Run("empty_file_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "empty.go", "")
		got := readSnippet(dir, "empty.go", 1, 5)
		// An empty file split by \n gives [""], which is 1 element.
		// start=max(0,-5)=0, end=min(6,1)=1 → returns "" (the single empty line).
		// This is acceptable — the snippet is the empty content.
		if strings.Contains(got, "\n") {
			t.Errorf("expected at most one line for empty file, got: %q", got)
		}
	})

	t.Run("path_traversal_blocked", func(t *testing.T) {
		dir := t.TempDir()
		// Create a file outside workDir.
		parentFile := filepath.Join(filepath.Dir(dir), "secret.txt")
		if err := os.WriteFile(parentFile, []byte("secret data"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(parentFile)

		got := readSnippet(dir, "../secret.txt", 1, 5)
		if got != "" {
			t.Errorf("expected empty string for path traversal, got: %q", got)
		}
	})
}

func TestReadFileForFinding(t *testing.T) {
	writeFile := func(t *testing.T, dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("full_file_within_budget", func(t *testing.T) {
		dir := t.TempDir()
		content := "line1\nline2\nline3\nline4\nline5\n"
		writeFile(t, dir, "small.go", content)

		budget := 1000
		cache := make(map[string]string)
		got := readFileForFinding(dir, "small.go", 3, "major", &budget, cache)

		if got != content {
			t.Errorf("got %q, want full file content", got)
		}
		if budget != 1000-len(content) {
			t.Errorf("budget = %d, want %d", budget, 1000-len(content))
		}
	})

	t.Run("falls_back_to_snippet_when_over_budget", func(t *testing.T) {
		dir := t.TempDir()
		// 20-line file — snippet at line 10 with ±5 context (minor) = 11 lines, not all 20.
		// Using "minor" severity to keep the ±5 fallback window so the snippet
		// is clearly shorter than the full 20-line file.
		var longLines []string
		for i := 1; i <= 20; i++ {
			longLines = append(longLines, strings.Repeat("x", 10))
		}
		content := strings.Join(longLines, "\n")
		writeFile(t, dir, "big.go", content)

		budget := 50 // too small for full file (~220 bytes)
		cache := make(map[string]string)
		got := readFileForFinding(dir, "big.go", 10, "minor", &budget, cache)

		if got == content {
			t.Error("should NOT return full file when over budget")
		}
		if got == "" {
			t.Error("should return snippet fallback, not empty")
		}
		// Snippet should be a subset of lines.
		snippetLines := strings.Split(got, "\n")
		if len(snippetLines) >= 20 {
			t.Errorf("snippet has %d lines, should be fewer than 20", len(snippetLines))
		}
	})

	t.Run("deduplicates_same_file", func(t *testing.T) {
		dir := t.TempDir()
		content := "func A() {}\nfunc B() {}\n"
		writeFile(t, dir, "shared.go", content)

		budget := 1000
		cache := make(map[string]string)

		got1 := readFileForFinding(dir, "shared.go", 1, "major", &budget, cache)
		budgetAfterFirst := budget
		got2 := readFileForFinding(dir, "shared.go", 2, "major", &budget, cache)

		if got1 == "" || got2 == "" {
			t.Errorf("both calls should return content: got1=%q, got2=%q", got1, got2)
		}
		if budget != budgetAfterFirst {
			t.Errorf("second read should not consume budget: %d vs %d", budget, budgetAfterFirst)
		}
	})

	t.Run("budget_exhaustion_across_files", func(t *testing.T) {
		dir := t.TempDir()
		file1Content := strings.Repeat("a", 100) + "\n"
		// 20-line file so snippet (±5 around line 15) is shorter than full file.
		var file2Lines []string
		for i := 1; i <= 20; i++ {
			file2Lines = append(file2Lines, strings.Repeat("b", 10))
		}
		file2Content := strings.Join(file2Lines, "\n")
		writeFile(t, dir, "first.go", file1Content)
		writeFile(t, dir, "second.go", file2Content)

		budget := 120 // enough for first file (101 bytes) but not second (~220 bytes)
		cache := make(map[string]string)

		got1 := readFileForFinding(dir, "first.go", 1, "major", &budget, cache)
		if got1 != file1Content {
			t.Error("first file should get full content")
		}

		got2 := readFileForFinding(dir, "second.go", 15, "major", &budget, cache)
		if got2 == file2Content {
			t.Error("second file should NOT get full content (over budget)")
		}
		if got2 == "" {
			t.Error("second file should get snippet fallback")
		}
	})

	t.Run("no_cache_on_budget_exhaustion", func(t *testing.T) {
		// Regression: when budget is exhausted on a cache miss, the file must
		// NOT be cached. A second finding for the same file should also fall
		// back to a snippet, not return full capped content for free.
		dir := t.TempDir()
		var fileLines []string
		for i := 1; i <= 100; i++ {
			fileLines = append(fileLines, strings.Repeat("z", 50))
		}
		content := strings.Join(fileLines, "\n")
		writeFile(t, dir, "expensive.go", content)

		budget := 0 // budget already exhausted
		cache := make(map[string]string)

		// First call: cache miss, budget exhausted → snippet fallback, NOT cached.
		got1 := readFileForFinding(dir, "expensive.go", 50, "major", &budget, cache)
		if got1 == "" {
			t.Fatal("first call should return snippet fallback")
		}
		if len(got1) > majorFindingCapBytes {
			t.Errorf("first call returned %d bytes, want ≤ %d (snippet)", len(got1), majorFindingCapBytes)
		}

		// Second call: should also be a cache miss (file was not cached),
		// also falls back to snippet — no budget bypass.
		got2 := readFileForFinding(dir, "expensive.go", 50, "major", &budget, cache)
		if got2 == "" {
			t.Fatal("second call should return snippet fallback")
		}
		// Both calls should return the same snippet.
		if got1 != got2 {
			t.Errorf("both calls should return same snippet, got %d vs %d bytes", len(got1), len(got2))
		}
		if budget != 0 {
			t.Errorf("budget should remain 0, got %d", budget)
		}

		// Verify the file was NOT cached.
		if _, inCache := cache["expensive.go"]; inCache {
			t.Error("file should NOT be in cache after budget-exhausted fallback")
		}
	})

	t.Run("nonexistent_file_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		budget := 1000
		cache := make(map[string]string)
		got := readFileForFinding(dir, "nope.go", 1, "major", &budget, cache)
		if got != "" {
			t.Errorf("expected empty for nonexistent file, got: %q", got)
		}
	})

	t.Run("path_traversal_blocked", func(t *testing.T) {
		dir := t.TempDir()
		parentFile := filepath.Join(filepath.Dir(dir), "secret.txt")
		os.WriteFile(parentFile, []byte("secret"), 0644)
		defer os.Remove(parentFile)

		budget := 1000
		cache := make(map[string]string)
		got := readFileForFinding(dir, "../secret.txt", 1, "major", &budget, cache)
		if got != "" {
			t.Errorf("expected empty for path traversal, got: %q", got)
		}
	})

	t.Run("cross_severity_cache_same_file_different_content", func(t *testing.T) {
		dir := t.TempDir()
		// 7KB file — larger than majorCap (5KB) but smaller than criticalCap (10KB).
		content := strings.Repeat("x", 7*1024) + "\n"
		if err := os.WriteFile(filepath.Join(dir, "mid.go"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		budget := 50000
		cache := make(map[string]string)

		// First call with "critical" — file is under criticalCap (10KB), returns full content.
		got1 := readFileForFinding(dir, "mid.go", 1, "critical", &budget, cache)
		budgetAfterFirst := budget

		// Second call (same file, "major", cache hit) — returns ≤5KB without charging budget.
		got2 := readFileForFinding(dir, "mid.go", 1, "major", &budget, cache)

		if len(got1) <= len(got2) {
			t.Errorf("critical content (%d bytes) should be larger than major content (%d bytes)", len(got1), len(got2))
		}
		if len(got2) > majorFindingCapBytes {
			t.Errorf("major cache hit should be ≤ %d bytes, got %d", majorFindingCapBytes, len(got2))
		}
		if budget != budgetAfterFirst {
			t.Errorf("cache hit should not charge budget: got %d, want %d", budget, budgetAfterFirst)
		}
	})

	t.Run("cache_hit_line_beyond_cap_boundary", func(t *testing.T) {
		dir := t.TempDir()
		// 300-line file, ~80 chars/line → ~24KB. Larger than both caps.
		var fileLines []string
		for i := 1; i <= 300; i++ {
			fileLines = append(fileLines, fmt.Sprintf("line-%03d %s", i, strings.Repeat("z", 70)))
		}
		content := strings.Join(fileLines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		budget := 50000
		cache := make(map[string]string)

		// First call: critical finding at line 1 — caches the full file,
		// returns up to criticalCap bytes from head.
		got1 := readFileForFinding(dir, "handler.go", 1, "critical", &budget, cache)
		if !strings.Contains(got1, "line-001") {
			t.Error("first call should contain line-001")
		}

		// Second call: major finding at line 200 (cache hit).
		// The major cap (5KB, ~63 lines) doesn't cover line 200.
		// The fix should center the snippet on line 200, not return head.
		got2 := readFileForFinding(dir, "handler.go", 200, "major", &budget, cache)
		if !strings.Contains(got2, "line-200") {
			t.Errorf("cache hit at line 200 should contain line-200, got snippet starting with: %q", got2[:min(80, len(got2))])
		}
	})

	t.Run("cache_miss_line_beyond_cap_centers_on_line", func(t *testing.T) {
		dir := t.TempDir()
		// 300-line file, ~80 chars/line → ~24KB. Larger than both caps.
		var fileLines []string
		for i := 1; i <= 300; i++ {
			fileLines = append(fileLines, fmt.Sprintf("line-%03d %s", i, strings.Repeat("z", 70)))
		}
		content := strings.Join(fileLines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		budget := 50000
		cache := make(map[string]string)

		// Cache miss, major finding at line 200. The file exceeds majorCap (5KB),
		// so the returned content should be centered on line 200, not the head.
		got := readFileForFinding(dir, "service.go", 200, "major", &budget, cache)
		if !strings.Contains(got, "line-200") {
			t.Errorf("cache miss at line 200 should contain line-200, got snippet starting with: %q", got[:min(80, len(got))])
		}
		if strings.Contains(got, "line-001") {
			t.Error("cache miss at line 200 should NOT contain line-001 (should not return head)")
		}
	})

	t.Run("cache_hit_sub_cap_file_returns_full_content", func(t *testing.T) {
		dir := t.TempDir()
		// 80-line file, ~32 chars/line → ~2.5KB, smaller than majorCap (5KB).
		var fileLines []string
		for i := 1; i <= 80; i++ {
			fileLines = append(fileLines, fmt.Sprintf("line-%03d %s", i, strings.Repeat("w", 20)))
		}
		content := strings.Join(fileLines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "small.go"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		budget := 50000
		cache := make(map[string]string)

		// Cache miss at line 10 — returns full file (sub-cap).
		got1 := readFileForFinding(dir, "small.go", 10, "major", &budget, cache)
		// Cache hit at line 70 — should also return full file, not a snippet.
		got2 := readFileForFinding(dir, "small.go", 70, "major", &budget, cache)

		if len(got1) != len(got2) {
			t.Errorf("sub-cap file: cache miss (%d bytes) and cache hit (%d bytes) should return same size", len(got1), len(got2))
		}
		if !strings.Contains(got2, "line-001") || !strings.Contains(got2, "line-080") {
			t.Error("cache hit on sub-cap file should return full content including first and last lines")
		}
	})

	t.Run("fallback_context_window_by_severity", func(t *testing.T) {
		dir := t.TempDir()
		// 100-line file.
		var fileLines []string
		for i := 1; i <= 100; i++ {
			fileLines = append(fileLines, strings.Repeat("y", 20))
		}
		content := strings.Join(fileLines, "\n")
		if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		// Budget=0 forces the snippet fallback path on cache miss.
		budgetZero := 0

		// Critical: ±15 lines around line 50 → lines 35..65 = 31 lines.
		cache1 := make(map[string]string)
		gotCritical := readFileForFinding(dir, "big.go", 50, "critical", &budgetZero, cache1)
		criticalLines := strings.Split(gotCritical, "\n")

		// Major: ±10 lines around line 50 → lines 40..60 = 21 lines.
		cache2 := make(map[string]string)
		gotMajor := readFileForFinding(dir, "big.go", 50, "major", &budgetZero, cache2)
		majorLines := strings.Split(gotMajor, "\n")

		// Minor: ±5 lines around line 50 → lines 45..55 = 11 lines.
		cache3 := make(map[string]string)
		gotMinor := readFileForFinding(dir, "big.go", 50, "minor", &budgetZero, cache3)
		minorLines := strings.Split(gotMinor, "\n")

		if len(criticalLines) != 31 {
			t.Errorf("critical snippet: got %d lines, want 31 (±15)", len(criticalLines))
		}
		if len(majorLines) != 21 {
			t.Errorf("major snippet: got %d lines, want 21 (±10)", len(majorLines))
		}
		if len(minorLines) != 11 {
			t.Errorf("minor snippet: got %d lines, want 11 (±5)", len(minorLines))
		}
	})
}

func TestExtractSnippet(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"

	got := extractSnippet(content, 5, 2)
	if !strings.Contains(got, "line3") || !strings.Contains(got, "line7") {
		t.Errorf("snippet should contain lines 3-7, got: %q", got)
	}

	got2 := extractSnippet(content, 1, 2)
	if !strings.Contains(got2, "line1") {
		t.Errorf("snippet near start should contain line1, got: %q", got2)
	}
}
