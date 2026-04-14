package pipeline

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AuthorityResolver determines whether a comment author has authority
// over the files being discussed. Implementations can use CODEOWNERS,
// team membership, or other heuristics.
type AuthorityResolver interface {
	// IsAuthoritative returns true if the given author has authority
	// over the specified file path. If filePath is empty (general PR
	// comment), implementations should return true for known owners.
	IsAuthoritative(author, filePath string) bool
}

// CODEOWNERSRule is a single rule from a CODEOWNERS file.
type CODEOWNERSRule struct {
	Pattern string   // glob pattern (e.g., "*.go", "internal/pipeline/")
	Owners  []string // GitHub usernames or team slugs (without @)
}

// CODEOWNERSAuthority implements AuthorityResolver using a parsed CODEOWNERS file.
// When the rule set is empty, all authors are considered authoritative
// (backward-compatible default).
type CODEOWNERSAuthority struct {
	rules []CODEOWNERSRule
}

// NewCODEOWNERSAuthority creates an AuthorityResolver from a slice of rules.
// If rules is nil or empty, all authors are treated as authoritative.
func NewCODEOWNERSAuthority(rules []CODEOWNERSRule) *CODEOWNERSAuthority {
	return &CODEOWNERSAuthority{rules: rules}
}

// ParseCODEOWNERS reads a CODEOWNERS file and returns the parsed rules.
// Lines starting with # are comments. Each non-empty line is:
//
//	<pattern> <owner1> [<owner2> ...]
//
// Owners may have a leading @ which is stripped.
func ParseCODEOWNERS(path string) ([]CODEOWNERSRule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("pipeline: parse CODEOWNERS %s: %w", path, err)
	}
	defer f.Close()

	return parseCODEOWNERSReader(bufio.NewScanner(f))
}

// parseCODEOWNERSReader parses CODEOWNERS rules from a scanner.
// Extracted for testability.
func parseCODEOWNERSReader(scanner *bufio.Scanner) ([]CODEOWNERSRule, error) {
	var rules []CODEOWNERSRule

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			// A CODEOWNERS line with only a pattern and no owners is unusual
			// but valid — skip it.
			continue
		}

		pattern := fields[0]
		owners := make([]string, 0, len(fields)-1)
		for _, owner := range fields[1:] {
			// Strip leading @ from owner references.
			owners = append(owners, strings.TrimPrefix(owner, "@"))
		}

		rules = append(rules, CODEOWNERSRule{
			Pattern: pattern,
			Owners:  owners,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("pipeline: scan CODEOWNERS: %w", err)
	}

	return rules, nil
}

// IsAuthoritative returns true if the author owns the given file path
// according to the CODEOWNERS rules. CODEOWNERS uses last-match-wins
// semantics (like .gitignore). If no rules are configured, all authors
// are considered authoritative. If filePath is empty (general PR comment),
// the author is considered authoritative if they appear in any rule.
func (c *CODEOWNERSAuthority) IsAuthoritative(author, filePath string) bool {
	if len(c.rules) == 0 {
		return true
	}

	normalAuthor := strings.ToLower(author)

	// General comment (no file path): check if author appears in any rule.
	if filePath == "" {
		for _, rule := range c.rules {
			for _, owner := range rule.Owners {
				if strings.ToLower(owner) == normalAuthor {
					return true
				}
			}
		}
		return false
	}

	// File-specific comment: use last-match-wins to find the owning rule.
	var matchedRule *CODEOWNERSRule
	for i := range c.rules {
		if matchPattern(c.rules[i].Pattern, filePath) {
			matchedRule = &c.rules[i]
		}
	}

	if matchedRule == nil {
		// No rule matches this file — treat as authoritative (unowned file).
		return true
	}

	for _, owner := range matchedRule.Owners {
		if strings.ToLower(owner) == normalAuthor {
			return true
		}
	}
	return false
}

// FindCODEOWNERS locates the CODEOWNERS file in a repository.
// Searches in order: .github/CODEOWNERS, CODEOWNERS, docs/CODEOWNERS.
func FindCODEOWNERS(repoRoot string) string {
	candidates := []string{
		filepath.Join(repoRoot, ".github", "CODEOWNERS"),
		filepath.Join(repoRoot, "CODEOWNERS"),
		filepath.Join(repoRoot, "docs", "CODEOWNERS"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// matchPattern matches a CODEOWNERS pattern against a file path.
// Supports:
//   - Exact file names ("Makefile")
//   - Directory prefixes ("internal/pipeline/")
//   - Extension globs ("*.go")
//   - Path globs ("docs/*.md")
//   - Catch-all ("*")
func matchPattern(pattern, filePath string) bool {
	// Catch-all.
	if pattern == "*" {
		return true
	}

	// Directory pattern (trailing /).
	if strings.HasSuffix(pattern, "/") {
		dir := strings.TrimSuffix(pattern, "/")
		// Strip leading / from pattern.
		dir = strings.TrimPrefix(dir, "/")
		return strings.HasPrefix(filePath, dir+"/") || filePath == dir
	}

	// Strip leading / from pattern for matching.
	cleanPattern := strings.TrimPrefix(pattern, "/")

	// Try filepath.Match first for glob patterns.
	if matched, err := filepath.Match(cleanPattern, filePath); err == nil && matched {
		return true
	}

	// For patterns like "*.go", also match against just the filename.
	if strings.Contains(cleanPattern, "*") && !strings.Contains(cleanPattern, "/") {
		base := filepath.Base(filePath)
		if matched, err := filepath.Match(cleanPattern, base); err == nil && matched {
			return true
		}
	}

	// Exact match.
	return cleanPattern == filePath
}
