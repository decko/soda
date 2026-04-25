package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	now := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	original := &PipelineMeta{
		Ticket:    "PROJ-123",
		Branch:    "feat/thing",
		Worktree:  "/tmp/wt",
		StartedAt: now,
		TotalCost: 1.23,
		Phases: map[string]*PhaseState{
			"triage": {
				Status:     PhaseCompleted,
				Cost:       0.12,
				DurationMs: 8000,
				Generation: 1,
			},
			"plan": {
				Status:     PhaseRunning,
				Cost:       0.31,
				DurationMs: 0,
				Generation: 2,
			},
			"implement": {
				Status: PhaseFailed,
				Error:  "test failure",
			},
		},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	loaded, err := ReadMeta(path)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	if loaded.Ticket != original.Ticket {
		t.Errorf("Ticket = %q, want %q", loaded.Ticket, original.Ticket)
	}
	if loaded.Branch != original.Branch {
		t.Errorf("Branch = %q, want %q", loaded.Branch, original.Branch)
	}
	if loaded.Worktree != original.Worktree {
		t.Errorf("Worktree = %q, want %q", loaded.Worktree, original.Worktree)
	}
	if !loaded.StartedAt.Equal(original.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", loaded.StartedAt, original.StartedAt)
	}
	if loaded.TotalCost != original.TotalCost {
		t.Errorf("TotalCost = %v, want %v", loaded.TotalCost, original.TotalCost)
	}

	// Verify phase states
	triagePhase := loaded.Phases["triage"]
	if triagePhase == nil {
		t.Fatal("triage phase missing")
	}
	if triagePhase.Status != PhaseCompleted {
		t.Errorf("triage status = %q, want %q", triagePhase.Status, PhaseCompleted)
	}
	if triagePhase.Cost != 0.12 {
		t.Errorf("triage cost = %v, want 0.12", triagePhase.Cost)
	}
	if triagePhase.DurationMs != 8000 {
		t.Errorf("triage duration = %d, want 8000", triagePhase.DurationMs)
	}

	planPhase := loaded.Phases["plan"]
	if planPhase == nil {
		t.Fatal("plan phase missing")
	}
	if planPhase.Generation != 2 {
		t.Errorf("plan generation = %d, want 2", planPhase.Generation)
	}

	implPhase := loaded.Phases["implement"]
	if implPhase == nil {
		t.Fatal("implement phase missing")
	}
	if implPhase.Error != "test failure" {
		t.Errorf("implement error = %q, want %q", implPhase.Error, "test failure")
	}
}

func TestReadMeta_InitializesNilPhasesMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	// Write JSON without a phases key
	if err := atomicWrite(path, []byte(`{"ticket":"T-1","started_at":"2026-04-11T10:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	meta, err := ReadMeta(path)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.Phases == nil {
		t.Error("Phases should be initialized to non-nil map")
	}
}

func TestReadMeta_FileNotExist(t *testing.T) {
	_, err := ReadMeta("/nonexistent/path/meta.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadMeta(t *testing.T) {
	dir := t.TempDir()
	metaPath := filepath.Join(dir, "meta.json")

	meta := &PipelineMeta{
		Ticket:    "TEST-1",
		Branch:    "feat/test",
		StartedAt: time.Now().Truncate(time.Second),
		TotalCost: 1.23,
		Phases:    map[string]*PhaseState{},
	}
	if err := WriteMeta(metaPath, meta); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	got, err := ReadMeta(metaPath)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if got.Ticket != "TEST-1" {
		t.Errorf("Ticket = %q, want TEST-1", got.Ticket)
	}
	if got.TotalCost != 1.23 {
		t.Errorf("TotalCost = %f, want 1.23", got.TotalCost)
	}

	_, err = ReadMeta(filepath.Join(dir, "nonexistent.json"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCumulativeCost_Empty(t *testing.T) {
	dir := t.TempDir()
	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cost != 0 {
		t.Errorf("CumulativeCost = %f, want 0", cost)
	}
}

func TestCumulativeCost_NonexistentDir(t *testing.T) {
	cost, err := CumulativeCost("/tmp/nonexistent-soda-cumulative-cost-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cost != 0 {
		t.Errorf("CumulativeCost = %f, want 0", cost)
	}
}

func TestCumulativeCost_MultipleSessions(t *testing.T) {
	dir := t.TempDir()

	writeTestMeta(t, filepath.Join(dir, "TICKET-1"), &PipelineMeta{
		Ticket:    "TICKET-1",
		TotalCost: 1.50,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})
	writeTestMeta(t, filepath.Join(dir, "TICKET-2"), &PipelineMeta{
		Ticket:    "TICKET-2",
		TotalCost: 3.25,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})
	writeTestMeta(t, filepath.Join(dir, "TICKET-3"), &PipelineMeta{
		Ticket:    "TICKET-3",
		TotalCost: 0.75,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})

	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := 5.50
	if cost != want {
		t.Errorf("CumulativeCost = %f, want %f", cost, want)
	}
}

func TestCumulativeCost_SkipsNonDirectories(t *testing.T) {
	dir := t.TempDir()

	writeTestMeta(t, filepath.Join(dir, "TICKET-1"), &PipelineMeta{
		Ticket:    "TICKET-1",
		TotalCost: 2.00,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})

	// Create a regular file (not a directory) — should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "not-a-dir"), []byte("junk"), 0644); err != nil {
		t.Fatal(err)
	}

	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cost != 2.00 {
		t.Errorf("CumulativeCost = %f, want 2.00", cost)
	}
}

func TestCumulativeCost_SkipsBrokenMeta(t *testing.T) {
	dir := t.TempDir()

	writeTestMeta(t, filepath.Join(dir, "TICKET-1"), &PipelineMeta{
		Ticket:    "TICKET-1",
		TotalCost: 4.00,
		StartedAt: time.Now(),
		Phases:    map[string]*PhaseState{},
	})

	// Create a directory with a corrupt meta.json — should be skipped.
	brokenDir := filepath.Join(dir, "BROKEN")
	if err := os.MkdirAll(brokenDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "meta.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	cost, err := CumulativeCost(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cost != 4.00 {
		t.Errorf("CumulativeCost = %f, want 4.00", cost)
	}
}

// writeTestMeta writes a PipelineMeta to dir/meta.json for testing.
func writeTestMeta(t *testing.T, dir string, meta *PipelineMeta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestMetaBinaryVersionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:        "PROJ-258",
		BinaryVersion: "v1.2.3-abc123def456",
		StartedAt:     time.Now().Truncate(time.Second),
		Phases:        map[string]*PhaseState{},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	loaded, err := ReadMeta(path)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	if loaded.BinaryVersion != original.BinaryVersion {
		t.Errorf("BinaryVersion = %q, want %q", loaded.BinaryVersion, original.BinaryVersion)
	}
}

func TestMetaBinaryVersionOmitEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:    "PROJ-258",
		StartedAt: time.Now().Truncate(time.Second),
		Phases:    map[string]*PhaseState{},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// When BinaryVersion is empty, it should be omitted from JSON.
	if strings.Contains(string(data), "binary_version") {
		t.Errorf("meta.json should omit binary_version when empty, got:\n%s", data)
	}
}

func TestMetaTokenCountsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:    "PROJ-281",
		StartedAt: time.Now().Truncate(time.Second),
		Phases: map[string]*PhaseState{
			"triage": {
				Status:        PhaseCompleted,
				Cost:          0.12,
				DurationMs:    8000,
				TokensIn:      15000,
				TokensOut:     2500,
				CacheTokensIn: 5000,
				Generation:    1,
			},
		},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	loaded, err := ReadMeta(path)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	ps := loaded.Phases["triage"]
	if ps == nil {
		t.Fatal("triage phase missing")
	}
	if ps.TokensIn != 15000 {
		t.Errorf("TokensIn = %d, want 15000", ps.TokensIn)
	}
	if ps.TokensOut != 2500 {
		t.Errorf("TokensOut = %d, want 2500", ps.TokensOut)
	}
	if ps.CacheTokensIn != 5000 {
		t.Errorf("CacheTokensIn = %d, want 5000", ps.CacheTokensIn)
	}
}

func TestMetaTokenCountsOmitEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:    "PROJ-281",
		StartedAt: time.Now().Truncate(time.Second),
		Phases: map[string]*PhaseState{
			"triage": {
				Status:     PhaseCompleted,
				Cost:       0.12,
				DurationMs: 8000,
				Generation: 1,
			},
		},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// When token counts are zero, they should be omitted from JSON.
	if strings.Contains(string(data), "tokens_in") {
		t.Errorf("meta.json should omit tokens_in when zero, got:\n%s", data)
	}
	if strings.Contains(string(data), "tokens_out") {
		t.Errorf("meta.json should omit tokens_out when zero, got:\n%s", data)
	}
	if strings.Contains(string(data), "cache_tokens_in") {
		t.Errorf("meta.json should omit cache_tokens_in when zero, got:\n%s", data)
	}
}

func TestMetaPipelineNameRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:    "PROJ-300",
		Pipeline:  "fast",
		StartedAt: time.Now().Truncate(time.Second),
		Phases:    map[string]*PhaseState{},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	loaded, err := ReadMeta(path)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	if loaded.Pipeline != "fast" {
		t.Errorf("Pipeline = %q, want %q", loaded.Pipeline, "fast")
	}
}

func TestMetaPipelineNameOmitEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:    "PROJ-300",
		StartedAt: time.Now().Truncate(time.Second),
		Phases:    map[string]*PhaseState{},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if strings.Contains(string(data), "pipeline") {
		t.Errorf("meta.json should omit pipeline when empty, got:\n%s", data)
	}
}

func TestMetaPromptHashRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:    "PROJ-383",
		StartedAt: time.Now().Truncate(time.Second),
		Phases: map[string]*PhaseState{
			"triage": {
				Status:     PhaseCompleted,
				Cost:       0.10,
				DurationMs: 5000,
				Generation: 1,
				PromptHash: "abc123def456789012345678901234567890123456789012345678901234abcd",
			},
		},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	loaded, err := ReadMeta(path)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	ps := loaded.Phases["triage"]
	if ps == nil {
		t.Fatal("triage phase missing")
	}
	if ps.PromptHash != original.Phases["triage"].PromptHash {
		t.Errorf("PromptHash = %q, want %q", ps.PromptHash, original.Phases["triage"].PromptHash)
	}
}

func TestMetaPromptHashOmitEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	original := &PipelineMeta{
		Ticket:    "PROJ-383",
		StartedAt: time.Now().Truncate(time.Second),
		Phases: map[string]*PhaseState{
			"triage": {
				Status:     PhaseCompleted,
				Cost:       0.10,
				DurationMs: 5000,
				Generation: 1,
			},
		},
	}

	if err := WriteMeta(path, original); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// When PromptHash is empty, it should be omitted from JSON.
	if strings.Contains(string(data), "prompt_hash") {
		t.Errorf("meta.json should omit prompt_hash when empty, got:\n%s", data)
	}
}

func TestPhaseStatusConstants(t *testing.T) {
	// Verify JSON serialization matches expected strings
	tests := []struct {
		status PhaseStatus
		want   string
	}{
		{PhasePending, "pending"},
		{PhaseRunning, "running"},
		{PhaseCompleted, "completed"},
		{PhaseFailed, "failed"},
		{PhaseRetrying, "retrying"},
		{PhasePaused, "paused"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("PhaseStatus %v = %q, want %q", tt.status, tt.status, tt.want)
		}
	}
}
