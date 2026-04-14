package pipeline

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCODEOWNERS(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantRules int
		wantFirst CODEOWNERSRule
	}{
		{
			name: "basic_rules",
			content: `# Global owners
* @alice @bob
*.go @carol
internal/pipeline/ @dave
`,
			wantRules: 3,
			wantFirst: CODEOWNERSRule{Pattern: "*", Owners: []string{"alice", "bob"}},
		},
		{
			name: "strips_at_signs",
			content: `*.md @docs-team @alice
`,
			wantRules: 1,
			wantFirst: CODEOWNERSRule{Pattern: "*.md", Owners: []string{"docs-team", "alice"}},
		},
		{
			name:      "skips_comments_and_blanks",
			content:   "# comment\n\n# another\n*.go @owner\n",
			wantRules: 1,
			wantFirst: CODEOWNERSRule{Pattern: "*.go", Owners: []string{"owner"}},
		},
		{
			name:      "skips_owner_less_lines",
			content:   "orphan-pattern\n*.go @owner\n",
			wantRules: 1,
			wantFirst: CODEOWNERSRule{Pattern: "*.go", Owners: []string{"owner"}},
		},
		{
			name:      "empty_file",
			content:   "",
			wantRules: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(tt.content))
			rules, err := parseCODEOWNERSReader(scanner)
			if err != nil {
				t.Fatalf("parseCODEOWNERSReader: %v", err)
			}

			if len(rules) != tt.wantRules {
				t.Fatalf("got %d rules, want %d", len(rules), tt.wantRules)
			}

			if tt.wantRules > 0 {
				got := rules[0]
				if got.Pattern != tt.wantFirst.Pattern {
					t.Errorf("first pattern = %q, want %q", got.Pattern, tt.wantFirst.Pattern)
				}
				if len(got.Owners) != len(tt.wantFirst.Owners) {
					t.Fatalf("first owners count = %d, want %d", len(got.Owners), len(tt.wantFirst.Owners))
				}
				for i, owner := range got.Owners {
					if owner != tt.wantFirst.Owners[i] {
						t.Errorf("owner[%d] = %q, want %q", i, owner, tt.wantFirst.Owners[i])
					}
				}
			}
		})
	}
}

func TestParseCODEOWNERS_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CODEOWNERS")
	content := "* @alice\n*.go @bob\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rules, err := ParseCODEOWNERS(path)
	if err != nil {
		t.Fatalf("ParseCODEOWNERS: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
}

func TestParseCODEOWNERS_MissingFile(t *testing.T) {
	_, err := ParseCODEOWNERS("/nonexistent/CODEOWNERS")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCODEOWNERSAuthority_IsAuthoritative(t *testing.T) {
	rules := []CODEOWNERSRule{
		{Pattern: "*", Owners: []string{"alice", "bob"}},
		{Pattern: "*.go", Owners: []string{"carol"}},
		{Pattern: "internal/pipeline/", Owners: []string{"dave"}},
		{Pattern: "docs/*.md", Owners: []string{"eve"}},
		{Pattern: "Makefile", Owners: []string{"frank"}},
	}
	auth := NewCODEOWNERSAuthority(rules)

	tests := []struct {
		name     string
		author   string
		filePath string
		want     bool
	}{
		// Last-match-wins: *.go matches, carol owns.
		{
			name:     "go_file_owner",
			author:   "carol",
			filePath: "main.go",
			want:     true,
		},
		{
			name:     "go_file_non_owner",
			author:   "alice",
			filePath: "main.go",
			want:     false,
		},
		// internal/pipeline/ directory match — dave owns.
		{
			name:     "pipeline_dir_owner",
			author:   "dave",
			filePath: "internal/pipeline/engine.go",
			want:     true,
		},
		{
			name:     "pipeline_dir_non_owner",
			author:   "carol",
			filePath: "internal/pipeline/engine.go",
			want:     false,
		},
		// Exact file match.
		{
			name:     "makefile_owner",
			author:   "frank",
			filePath: "Makefile",
			want:     true,
		},
		{
			name:     "makefile_non_owner",
			author:   "alice",
			filePath: "Makefile",
			want:     false,
		},
		// docs/*.md match.
		{
			name:     "docs_md_owner",
			author:   "eve",
			filePath: "docs/README.md",
			want:     true,
		},
		// Unmatched file (no rule) — authoritative by default.
		{
			name:     "unmatched_file_anyone_authoritative",
			author:   "random",
			filePath: "random.txt",
			want:     false, // catch-all * matches, owned by alice/bob
		},
		{
			name:     "catchall_owner",
			author:   "alice",
			filePath: "random.txt",
			want:     true, // catch-all matches, alice is owner
		},
		// General comment (empty path) — check any rule.
		{
			name:     "general_comment_known_owner",
			author:   "carol",
			filePath: "",
			want:     true,
		},
		{
			name:     "general_comment_unknown_author",
			author:   "stranger",
			filePath: "",
			want:     false,
		},
		// Case-insensitive author matching.
		{
			name:     "case_insensitive_author",
			author:   "Carol",
			filePath: "main.go",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := auth.IsAuthoritative(tt.author, tt.filePath)
			if got != tt.want {
				t.Errorf("IsAuthoritative(%q, %q) = %v, want %v", tt.author, tt.filePath, got, tt.want)
			}
		})
	}
}

func TestCODEOWNERSAuthority_EmptyRules(t *testing.T) {
	// With no rules, everyone is authoritative (backward-compatible default).
	auth := NewCODEOWNERSAuthority(nil)

	if !auth.IsAuthoritative("anyone", "any/file.go") {
		t.Error("empty rules: expected all authors to be authoritative")
	}
	if !auth.IsAuthoritative("anyone", "") {
		t.Error("empty rules: expected all authors to be authoritative for general comments")
	}
}

func TestFindCODEOWNERS(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(root string)
		wantEnd string // expected suffix of the found path, or "" for not found
	}{
		{
			name: "github_directory",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, ".github"), 0755)
				os.WriteFile(filepath.Join(root, ".github", "CODEOWNERS"), []byte("* @a\n"), 0644)
			},
			wantEnd: ".github/CODEOWNERS",
		},
		{
			name: "root_directory",
			setup: func(root string) {
				os.WriteFile(filepath.Join(root, "CODEOWNERS"), []byte("* @a\n"), 0644)
			},
			wantEnd: "CODEOWNERS",
		},
		{
			name: "docs_directory",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "docs"), 0755)
				os.WriteFile(filepath.Join(root, "docs", "CODEOWNERS"), []byte("* @a\n"), 0644)
			},
			wantEnd: "docs/CODEOWNERS",
		},
		{
			name: "prefers_github_over_root",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, ".github"), 0755)
				os.WriteFile(filepath.Join(root, ".github", "CODEOWNERS"), []byte("* @a\n"), 0644)
				os.WriteFile(filepath.Join(root, "CODEOWNERS"), []byte("* @b\n"), 0644)
			},
			wantEnd: ".github/CODEOWNERS",
		},
		{
			name:    "not_found",
			setup:   func(root string) {},
			wantEnd: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(root)

			got := FindCODEOWNERS(root)
			if tt.wantEnd == "" {
				if got != "" {
					t.Errorf("FindCODEOWNERS = %q, want empty", got)
				}
				return
			}
			if !strings.HasSuffix(got, tt.wantEnd) {
				t.Errorf("FindCODEOWNERS = %q, want suffix %q", got, tt.wantEnd)
			}
		})
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern  string
		filePath string
		want     bool
	}{
		{"*", "anything.txt", true},
		{"*.go", "main.go", true},
		{"*.go", "internal/pipeline/engine.go", true},
		{"*.go", "README.md", false},
		{"internal/pipeline/", "internal/pipeline/engine.go", true},
		{"internal/pipeline/", "internal/other/file.go", false},
		{"docs/*.md", "docs/README.md", true},
		{"docs/*.md", "docs/sub/file.md", false}, // filepath.Match doesn't cross /
		{"Makefile", "Makefile", true},
		{"Makefile", "other/Makefile", false},
		{"/src/", "src/main.go", true},
		{"/*.go", "main.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.filePath, func(t *testing.T) {
			got := matchPattern(tt.pattern, tt.filePath)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.filePath, got, tt.want)
			}
		})
	}
}
