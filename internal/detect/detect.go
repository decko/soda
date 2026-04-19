// Package detect provides auto-detection of project stack information
// from a repository directory. It inspects marker files (go.mod,
// package.json, etc.) to determine the language, and parses the git
// remote URL to identify the forge, owner, and repo name. It also
// checks for well-known context files (AGENTS.md, CLAUDE.md).
package detect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProjectInfo holds detected information about a project repository.
type ProjectInfo struct {
	Language     string   // e.g. "go", "python", "typescript", "javascript", "rust", "java", "unknown"
	Forge        string   // e.g. "github", "gitlab", or "" if unrecognised
	Owner        string   // repository owner/org extracted from remote URL
	Repo         string   // repository name extracted from remote URL
	Formatter    string   // suggested formatter command, or "" if unknown
	TestCommand  string   // suggested test command, or "" if unknown
	ContextFiles []string // detected context files (e.g. "AGENTS.md", "CLAUDE.md")

	// markerFile is the file that triggered language detection (internal use).
	markerFile string
}

// languageMarker maps a filename to the language it indicates.
type languageMarker struct {
	file     string
	language string
}

// languageMarkers is checked in priority order; the first match wins.
var languageMarkers = []languageMarker{
	{"go.mod", "go"},
	{"Cargo.toml", "rust"},
	{"pyproject.toml", "python"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
	// package.json is checked last because it can indicate either
	// JavaScript or TypeScript depending on the presence of tsconfig.json.
	{"package.json", "javascript"},
}

// contextFileNames lists well-known context files to look for in the repo root.
var contextFileNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
}

// Detect scans repoDir and returns a ProjectInfo describing the project.
// It is safe to call on directories that are not git repositories; forge
// information will simply be empty in that case. The context is used for
// subprocess cancellation (e.g. git remote lookup).
func Detect(ctx context.Context, repoDir string) (*ProjectInfo, error) {
	if _, err := os.Stat(repoDir); err != nil {
		return nil, fmt.Errorf("detect: stat %s: %w", repoDir, err)
	}

	info := &ProjectInfo{
		Language: "unknown",
	}

	detectLanguage(repoDir, info)
	detectForge(ctx, repoDir, info)
	detectContextFiles(repoDir, info)
	inferTooling(info)

	return info, nil
}

// detectLanguage checks for marker files and sets info.Language.
func detectLanguage(repoDir string, info *ProjectInfo) {
	for _, marker := range languageMarkers {
		path := filepath.Join(repoDir, marker.file)
		if _, err := os.Stat(path); err == nil {
			lang := marker.language
			// TypeScript is a JavaScript project with a tsconfig.json.
			if lang == "javascript" {
				tsconfig := filepath.Join(repoDir, "tsconfig.json")
				if _, tsErr := os.Stat(tsconfig); tsErr == nil {
					lang = "typescript"
				}
			}
			info.Language = lang
			info.markerFile = marker.file
			return
		}
	}
}

// detectForge runs "git remote get-url origin" and parses the result.
// Uses exec.CommandContext so the call respects pipeline-level timeouts
// and cancellation, consistent with other subprocess invocations in the
// codebase.
func detectForge(ctx context.Context, repoDir string, info *ProjectInfo) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return // no remote or not a git repo — leave fields empty
	}
	url := strings.TrimSpace(string(out))
	info.Forge, info.Owner, info.Repo = parseRemoteURL(url)
}

// detectContextFiles looks for well-known context files in the repo root.
func detectContextFiles(repoDir string, info *ProjectInfo) {
	for _, name := range contextFileNames {
		path := filepath.Join(repoDir, name)
		if _, err := os.Stat(path); err == nil {
			info.ContextFiles = append(info.ContextFiles, name)
		}
	}
}

// inferTooling sets Formatter and TestCommand based on the detected language.
func inferTooling(info *ProjectInfo) {
	switch info.Language {
	case "go":
		info.Formatter = "gofmt -w ."
		info.TestCommand = "go test ./..."
	case "python":
		info.Formatter = "black ."
		info.TestCommand = "pytest"
	case "typescript", "javascript":
		info.Formatter = "npx prettier --write ."
		info.TestCommand = "npm test"
	case "rust":
		info.Formatter = "cargo fmt"
		info.TestCommand = "cargo test"
	case "java":
		info.Formatter = ""
		switch info.markerFile {
		case "build.gradle":
			info.TestCommand = "gradle test"
		default:
			info.TestCommand = "mvn test"
		}
	}
}

// parseRemoteURL extracts forge, owner, and repo from a git remote URL.
// Supports SSH (git@github.com:owner/repo.git), SSH URL
// (ssh://git@github.com/owner/repo.git), and HTTPS
// (https://github.com/owner/repo.git) formats for GitHub and GitLab.
// Returns empty strings for unrecognised URLs.
func parseRemoteURL(url string) (forge, owner, repo string) {
	if url == "" {
		return "", "", ""
	}

	// SSH URL format: ssh://git@github.com/owner/repo.git
	// Git may rewrite HTTPS URLs to this format via url.<base>.insteadOf.
	if strings.HasPrefix(url, "ssh://") {
		return parseSSHURLRemote(url)
	}

	// SCP-style SSH format: git@github.com:owner/repo.git
	if strings.HasPrefix(url, "git@") {
		return parseSSHRemote(url)
	}

	// HTTPS format: https://github.com/owner/repo.git
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return parseHTTPSRemote(url)
	}

	return "", "", ""
}

// parseSSHURLRemote handles ssh://git@<host>/<owner>/<repo>.git URLs.
// Git may produce these when url.<base>.insteadOf rewrites are active.
func parseSSHURLRemote(url string) (forge, owner, repo string) {
	// Strip "ssh://git@" prefix
	stripped := strings.TrimPrefix(url, "ssh://git@")

	// Split: host / owner / repo
	parts := strings.SplitN(stripped, "/", 3)
	if len(parts) < 3 {
		return "", "", ""
	}

	forge = forgeFromHost(parts[0])
	if forge == "" {
		return "", "", ""
	}

	owner = parts[1]
	repo = strings.TrimSuffix(parts[2], ".git")
	repo = strings.TrimRight(repo, "/")
	return forge, owner, repo
}

// parseSSHRemote handles git@<host>:<owner>/<repo>.git URLs.
func parseSSHRemote(url string) (forge, owner, repo string) {
	// Split on ":"  →  "git@github.com" and "owner/repo.git"
	parts := strings.SplitN(url, ":", 2)
	if len(parts) != 2 {
		return "", "", ""
	}

	host := strings.TrimPrefix(parts[0], "git@")
	forge = forgeFromHost(host)
	if forge == "" {
		return "", "", ""
	}

	pathParts := strings.SplitN(parts[1], "/", 2)
	if len(pathParts) != 2 {
		return "", "", ""
	}

	owner = pathParts[0]
	repo = strings.TrimSuffix(pathParts[1], ".git")
	return forge, owner, repo
}

// parseHTTPSRemote handles https://<host>/<owner>/<repo>.git URLs.
func parseHTTPSRemote(url string) (forge, owner, repo string) {
	// Strip scheme
	stripped := url
	stripped = strings.TrimPrefix(stripped, "https://")
	stripped = strings.TrimPrefix(stripped, "http://")

	// Split: host / owner / repo
	parts := strings.SplitN(stripped, "/", 3)
	if len(parts) < 3 {
		return "", "", ""
	}

	forge = forgeFromHost(parts[0])
	if forge == "" {
		return "", "", ""
	}

	owner = parts[1]
	repo = strings.TrimSuffix(parts[2], ".git")
	// Remove trailing slashes if any.
	repo = strings.TrimRight(repo, "/")
	return forge, owner, repo
}

// forgeFromHost maps a hostname to a forge name.
func forgeFromHost(host string) string {
	switch host {
	case "github.com":
		return "github"
	case "gitlab.com":
		return "gitlab"
	default:
		return ""
	}
}
