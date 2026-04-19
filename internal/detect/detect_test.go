package detect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepo initialises a bare git repo in dir with an "origin" remote
// pointing at the given URL. If url is empty the remote is skipped.
func initGitRepo(t *testing.T, dir, url string) {
	t.Helper()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "init")
	if url != "" {
		run(t, dir, "git", "remote", "add", "origin", url)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Isolate from host git config to prevent url.*.insteadOf rewrites
	// that can change HTTPS remotes to SSH on CI.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %s: %v", name, args, out, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name            string
		remoteURL       string
		files           map[string]string // relative path -> content
		wantLanguage    string
		wantForge       string
		wantOwner       string
		wantRepo        string
		wantFormatter   string
		wantTestCommand string
		wantContext     []string
	}{
		{
			name:            "Go project with GitHub SSH remote",
			remoteURL:       "git@github.com:decko/soda.git",
			files:           map[string]string{"go.mod": "module github.com/decko/soda\n\ngo 1.25.0\n"},
			wantLanguage:    "go",
			wantForge:       "github",
			wantOwner:       "decko",
			wantRepo:        "soda",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
		},
		{
			name:            "Go project with HTTPS remote",
			remoteURL:       "https://github.com/decko/soda.git",
			files:           map[string]string{"go.mod": "module github.com/decko/soda\n\ngo 1.25.0\n"},
			wantLanguage:    "go",
			wantForge:       "github",
			wantOwner:       "decko",
			wantRepo:        "soda",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
		},
		{
			name:            "Python project with pyproject.toml",
			remoteURL:       "git@github.com:acme/myapp.git",
			files:           map[string]string{"pyproject.toml": "[project]\nname = \"myapp\"\n"},
			wantLanguage:    "python",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "myapp",
			wantFormatter:   "black .",
			wantTestCommand: "pytest",
		},
		{
			name:            "TypeScript project",
			remoteURL:       "git@github.com:acme/frontend.git",
			files:           map[string]string{"package.json": `{"name":"frontend"}`, "tsconfig.json": "{}"},
			wantLanguage:    "typescript",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "frontend",
			wantFormatter:   "npx prettier --write .",
			wantTestCommand: "npm test",
		},
		{
			name:            "JavaScript project without tsconfig",
			remoteURL:       "git@github.com:acme/scripts.git",
			files:           map[string]string{"package.json": `{"name":"scripts"}`},
			wantLanguage:    "javascript",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "scripts",
			wantFormatter:   "npx prettier --write .",
			wantTestCommand: "npm test",
		},
		{
			name:            "Rust project",
			remoteURL:       "git@github.com:acme/oxide.git",
			files:           map[string]string{"Cargo.toml": "[package]\nname = \"oxide\"\n"},
			wantLanguage:    "rust",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "oxide",
			wantFormatter:   "cargo fmt",
			wantTestCommand: "cargo test",
		},
		{
			name:            "Java project with Maven",
			remoteURL:       "git@github.com:acme/service.git",
			files:           map[string]string{"pom.xml": "<project></project>"},
			wantLanguage:    "java",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "service",
			wantFormatter:   "",
			wantTestCommand: "mvn test",
		},
		{
			name:            "Java project with Gradle",
			remoteURL:       "git@github.com:acme/service.git",
			files:           map[string]string{"build.gradle": "plugins { id 'java' }"},
			wantLanguage:    "java",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "service",
			wantFormatter:   "",
			wantTestCommand: "gradle test",
		},
		{
			name:            "GitLab SSH remote",
			remoteURL:       "git@gitlab.com:team/backend.git",
			files:           map[string]string{"go.mod": "module gitlab.com/team/backend\n\ngo 1.22\n"},
			wantLanguage:    "go",
			wantForge:       "gitlab",
			wantOwner:       "team",
			wantRepo:        "backend",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
		},
		{
			name:            "GitLab HTTPS remote",
			remoteURL:       "https://gitlab.com/team/backend.git",
			files:           map[string]string{"go.mod": "module gitlab.com/team/backend\n\ngo 1.22\n"},
			wantLanguage:    "go",
			wantForge:       "gitlab",
			wantOwner:       "team",
			wantRepo:        "backend",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
		},
		{
			name:            "unknown language",
			remoteURL:       "git@github.com:acme/docs.git",
			files:           map[string]string{"README.md": "# Docs"},
			wantLanguage:    "unknown",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "docs",
			wantFormatter:   "",
			wantTestCommand: "",
		},
		{
			name:            "no remote",
			remoteURL:       "",
			files:           map[string]string{"go.mod": "module local\n\ngo 1.22\n"},
			wantLanguage:    "go",
			wantForge:       "",
			wantOwner:       "",
			wantRepo:        "",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
		},
		{
			name:            "AGENTS.md detected",
			remoteURL:       "git@github.com:acme/proj.git",
			files:           map[string]string{"go.mod": "module m\n\ngo 1.22\n", "AGENTS.md": "# Agents"},
			wantLanguage:    "go",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "proj",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
			wantContext:     []string{"AGENTS.md"},
		},
		{
			name:            "CLAUDE.md detected",
			remoteURL:       "git@github.com:acme/proj.git",
			files:           map[string]string{"go.mod": "module m\n\ngo 1.22\n", "CLAUDE.md": "# Claude"},
			wantLanguage:    "go",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "proj",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
			wantContext:     []string{"CLAUDE.md"},
		},
		{
			name:            "both AGENTS.md and CLAUDE.md detected",
			remoteURL:       "git@github.com:acme/proj.git",
			files:           map[string]string{"go.mod": "module m\n\ngo 1.22\n", "AGENTS.md": "# Agents", "CLAUDE.md": "# Claude"},
			wantLanguage:    "go",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "proj",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
			wantContext:     []string{"AGENTS.md", "CLAUDE.md"},
		},
		{
			name:            "GitHub HTTPS remote without .git suffix",
			remoteURL:       "https://github.com/acme/myrepo",
			files:           map[string]string{"go.mod": "module m\n\ngo 1.22\n"},
			wantLanguage:    "go",
			wantForge:       "github",
			wantOwner:       "acme",
			wantRepo:        "myrepo",
			wantFormatter:   "gofmt -w .",
			wantTestCommand: "go test ./...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir, tt.remoteURL)

			for path, content := range tt.files {
				writeFile(t, filepath.Join(dir, path), content)
			}

			info, err := Detect(context.Background(), dir)
			if err != nil {
				t.Fatalf("Detect() error: %v", err)
			}

			if info.Language != tt.wantLanguage {
				t.Errorf("Language = %q, want %q", info.Language, tt.wantLanguage)
			}
			if info.Forge != tt.wantForge {
				t.Errorf("Forge = %q, want %q", info.Forge, tt.wantForge)
			}
			if info.Owner != tt.wantOwner {
				t.Errorf("Owner = %q, want %q", info.Owner, tt.wantOwner)
			}
			if info.Repo != tt.wantRepo {
				t.Errorf("Repo = %q, want %q", info.Repo, tt.wantRepo)
			}
			if info.Formatter != tt.wantFormatter {
				t.Errorf("Formatter = %q, want %q", info.Formatter, tt.wantFormatter)
			}
			if info.TestCommand != tt.wantTestCommand {
				t.Errorf("TestCommand = %q, want %q", info.TestCommand, tt.wantTestCommand)
			}
			if tt.wantContext != nil {
				if len(info.ContextFiles) != len(tt.wantContext) {
					t.Errorf("ContextFiles = %v, want %v", info.ContextFiles, tt.wantContext)
				} else {
					for idx, want := range tt.wantContext {
						if info.ContextFiles[idx] != want {
							t.Errorf("ContextFiles[%d] = %q, want %q", idx, info.ContextFiles[idx], want)
						}
					}
				}
			}
		})
	}
}

func TestDetect_InvalidDir(t *testing.T) {
	_, err := Detect(context.Background(), "/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
}

func TestDetect_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	// No git init -- just a plain directory with a go.mod.
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.22\n")

	info, err := Detect(context.Background(), dir)
	if err != nil {
		t.Fatalf("Detect() error: %v", err)
	}

	// Language detection should still work even without a git repo.
	if info.Language != "go" {
		t.Errorf("Language = %q, want %q", info.Language, "go")
	}
	// Forge info should be empty since there is no git remote.
	if info.Forge != "" {
		t.Errorf("Forge = %q, want empty", info.Forge)
	}
}

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantForge string
		wantOwner string
		wantRepo  string
	}{
		{
			name:      "GitHub SSH",
			url:       "git@github.com:decko/soda.git",
			wantForge: "github",
			wantOwner: "decko",
			wantRepo:  "soda",
		},
		{
			name:      "GitHub HTTPS",
			url:       "https://github.com/decko/soda.git",
			wantForge: "github",
			wantOwner: "decko",
			wantRepo:  "soda",
		},
		{
			name:      "GitHub HTTPS no .git",
			url:       "https://github.com/decko/soda",
			wantForge: "github",
			wantOwner: "decko",
			wantRepo:  "soda",
		},
		{
			name:      "GitLab SSH",
			url:       "git@gitlab.com:team/project.git",
			wantForge: "gitlab",
			wantOwner: "team",
			wantRepo:  "project",
		},
		{
			name:      "GitLab HTTPS",
			url:       "https://gitlab.com/team/project.git",
			wantForge: "gitlab",
			wantOwner: "team",
			wantRepo:  "project",
		},
		{
			name:      "GitHub SSH URL",
			url:       "ssh://git@github.com/decko/soda.git",
			wantForge: "github",
			wantOwner: "decko",
			wantRepo:  "soda",
		},
		{
			name:      "GitLab SSH URL",
			url:       "ssh://git@gitlab.com/team/project.git",
			wantForge: "gitlab",
			wantOwner: "team",
			wantRepo:  "project",
		},
		{
			name:      "unknown forge",
			url:       "git@bitbucket.org:team/project.git",
			wantForge: "",
			wantOwner: "",
			wantRepo:  "",
		},
		{
			name:      "empty URL",
			url:       "",
			wantForge: "",
			wantOwner: "",
			wantRepo:  "",
		},
		{
			name:      "malformed URL",
			url:       "not-a-url",
			wantForge: "",
			wantOwner: "",
			wantRepo:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			forge, owner, repo := parseRemoteURL(tt.url)
			if forge != tt.wantForge {
				t.Errorf("forge = %q, want %q", forge, tt.wantForge)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}
