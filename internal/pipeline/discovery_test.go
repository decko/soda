package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePipelineName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"", false},
		{"default", false},
		{"fast", false},
		{"ci-lite", false},
		{"foo/bar", true},
		{`foo\bar`, true},
		{"foo/../bar", true},
		{"../etc/passwd", true},
		{"..", true},
		{"foo/../../bar", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePipelineName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePipelineName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestPipelineFilename(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"", "phases.yaml"},
		{"default", "phases.yaml"},
		{"fast", "phases-fast.yaml"},
		{"ci-lite", "phases-ci-lite.yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PipelineFilename(tt.name)
			if got != tt.want {
				t.Errorf("PipelineFilename(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestPipelineNameFromFile(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"phases.yaml", "default"},
		{"phases-fast.yaml", "fast"},
		{"phases-ci-lite.yaml", "ci-lite"},
		{"phases-.yaml", ""},
		{"other.yaml", ""},
		{"phases.yml", ""},
		{"phases-fast.yml", ""},
		{"dir/phases-nested.yaml", "nested"},
		{"phases.yaml.bak", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := PipelineNameFromFile(tt.filename)
			if got != tt.want {
				t.Errorf("PipelineNameFromFile(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestDiscoverPipelines(t *testing.T) {
	t.Run("discovers_default_and_named", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "phases.yaml"), minimalPhasesYAML)
		writeFile(t, filepath.Join(dir, "phases-fast.yaml"), minimalPhasesYAML)
		writeFile(t, filepath.Join(dir, "phases-ci.yaml"), minimalPhasesYAML)
		writeFile(t, filepath.Join(dir, "unrelated.yaml"), "key: value")

		pipelines := DiscoverPipelines([]string{dir}, []string{"local"})

		if len(pipelines) != 3 {
			t.Fatalf("got %d pipelines, want 3", len(pipelines))
		}

		// default should be first
		if pipelines[0].Name != "default" {
			t.Errorf("first pipeline = %q, want %q", pipelines[0].Name, "default")
		}
		if pipelines[0].Source != "local" {
			t.Errorf("first pipeline source = %q, want %q", pipelines[0].Source, "local")
		}

		// ci and fast should follow alphabetically
		if pipelines[1].Name != "ci" {
			t.Errorf("second pipeline = %q, want %q", pipelines[1].Name, "ci")
		}
		if pipelines[2].Name != "fast" {
			t.Errorf("third pipeline = %q, want %q", pipelines[2].Name, "fast")
		}
	})

	t.Run("higher_priority_source_wins", func(t *testing.T) {
		localDir := t.TempDir()
		userDir := t.TempDir()

		writeFile(t, filepath.Join(localDir, "phases.yaml"), minimalPhasesYAML)
		writeFile(t, filepath.Join(localDir, "phases-fast.yaml"), minimalPhasesYAML)
		// user dir also has default and an extra pipeline
		writeFile(t, filepath.Join(userDir, "phases.yaml"), minimalPhasesYAML)
		writeFile(t, filepath.Join(userDir, "phases-full.yaml"), minimalPhasesYAML)

		pipelines := DiscoverPipelines(
			[]string{localDir, userDir},
			[]string{"local", "user"},
		)

		if len(pipelines) != 3 {
			t.Fatalf("got %d pipelines, want 3", len(pipelines))
		}

		// default should come from local (first dir)
		if pipelines[0].Name != "default" || pipelines[0].Source != "local" {
			t.Errorf("default pipeline source = %q, want %q", pipelines[0].Source, "local")
		}

		// fast should come from local
		found := false
		for _, p := range pipelines {
			if p.Name == "fast" {
				found = true
				if p.Source != "local" {
					t.Errorf("fast pipeline source = %q, want %q", p.Source, "local")
				}
			}
		}
		if !found {
			t.Error("expected fast pipeline in results")
		}

		// full should come from user (only source)
		found = false
		for _, p := range pipelines {
			if p.Name == "full" {
				found = true
				if p.Source != "user" {
					t.Errorf("full pipeline source = %q, want %q", p.Source, "user")
				}
			}
		}
		if !found {
			t.Error("expected full pipeline in results")
		}
	})

	t.Run("empty_directory", func(t *testing.T) {
		dir := t.TempDir()
		pipelines := DiscoverPipelines([]string{dir}, []string{"local"})
		if len(pipelines) != 0 {
			t.Errorf("got %d pipelines, want 0", len(pipelines))
		}
	})

	t.Run("nonexistent_directory", func(t *testing.T) {
		pipelines := DiscoverPipelines([]string{"/nonexistent"}, []string{"local"})
		if len(pipelines) != 0 {
			t.Errorf("got %d pipelines, want 0", len(pipelines))
		}
	})

	t.Run("mismatched_dirs_sources_returns_nil", func(t *testing.T) {
		pipelines := DiscoverPipelines([]string{"/a", "/b"}, []string{"local"})
		if pipelines != nil {
			t.Errorf("expected nil for mismatched dirs/sources, got %v", pipelines)
		}
	})
}

const minimalPhasesYAML = `phases:
  - name: triage
    prompt: prompts/triage.md
    timeout: 1m
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
