package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/decko/soda/schemas"
)

// maxSiblingContextBytes limits the total size of the sibling-function
// context string injected into prompts. This prevents plan files that
// reference many large source files from inflating the prompt beyond
// the model's context window.
const maxSiblingContextBytes = 20000

// maxPackageExemplarBytes limits the total size of the package-exemplar
// context string injected into prompts for new-file creation.
const maxPackageExemplarBytes = 5000

// ExtractPlanFiles parses a plan result JSON and returns a deduplicated,
// sorted list of file paths referenced across all tasks.
func ExtractPlanFiles(planResult json.RawMessage) ([]string, error) {
	if len(planResult) == 0 {
		return nil, nil
	}

	var plan schemas.PlanOutput
	if err := json.Unmarshal(planResult, &plan); err != nil {
		return nil, fmt.Errorf("siblings: parse plan result: %w", err)
	}

	seen := make(map[string]struct{})
	var files []string
	for _, task := range plan.Tasks {
		for _, f := range task.Files {
			clean := filepath.Clean(f)
			if _, ok := seen[clean]; ok {
				continue
			}
			seen[clean] = struct{}{}
			files = append(files, clean)
		}
	}
	sort.Strings(files)
	return files, nil
}

// ExtractGoSignatures parses a Go source file and returns a list of
// function/method signature lines (without bodies). Returns nil for
// non-Go files, test files, or parse errors (best-effort).
func ExtractGoSignatures(filePath string) ([]string, error) {
	if !strings.HasSuffix(filePath, ".go") {
		return nil, nil
	}
	if strings.HasSuffix(filePath, "_test.go") {
		return nil, nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("siblings: parse %s: %w", filePath, err)
	}

	var sigs []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		sigs = append(sigs, formatFuncSignature(fn))
	}
	return sigs, nil
}

// ExtractGoTestPatterns parses a Go test file (_test.go) and returns a
// list of function signature lines. Returns nil for non-test Go files,
// non-Go files, or parse errors (best-effort).
func ExtractGoTestPatterns(filePath string) ([]string, error) {
	if !strings.HasSuffix(filePath, "_test.go") {
		return nil, nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("siblings: parse %s: %w", filePath, err)
	}

	var sigs []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		sigs = append(sigs, formatFuncSignature(fn))
	}
	return sigs, nil
}

// formatFuncSignature renders a function declaration's signature as a
// single-line string (e.g., "func (e *Engine) Run(ctx context.Context) error").
func formatFuncSignature(fn *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString("func ")

	// Receiver.
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		b.WriteByte('(')
		recv := fn.Recv.List[0]
		if len(recv.Names) > 0 {
			b.WriteString(recv.Names[0].Name)
			b.WriteByte(' ')
		}
		b.WriteString(exprString(recv.Type))
		b.WriteString(") ")
	}

	b.WriteString(fn.Name.Name)

	// Type parameters (generics).
	if fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) > 0 {
		b.WriteByte('[')
		b.WriteString(fieldListString(fn.Type.TypeParams))
		b.WriteByte(']')
	}

	// Parameters.
	b.WriteByte('(')
	b.WriteString(fieldListString(fn.Type.Params))
	b.WriteByte(')')

	// Results.
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		results := fieldListString(fn.Type.Results)
		if len(fn.Type.Results.List) == 1 && len(fn.Type.Results.List[0].Names) == 0 {
			b.WriteByte(' ')
			b.WriteString(results)
		} else {
			b.WriteString(" (")
			b.WriteString(results)
			b.WriteByte(')')
		}
	}

	return b.String()
}

// fieldListString renders a field list as comma-separated parameters.
func fieldListString(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, field := range fl.List {
		typeStr := exprString(field.Type)
		if len(field.Names) == 0 {
			parts = append(parts, typeStr)
		} else {
			for _, name := range field.Names {
				parts = append(parts, name.Name+" "+typeStr)
			}
		}
	}
	return strings.Join(parts, ", ")
}

// exprString renders an AST expression as a compact string.
// Covers the common type expression forms found in Go function signatures.
func exprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.ArrayType:
		if e.Len == nil {
			return "[]" + exprString(e.Elt)
		}
		return "[...]" + exprString(e.Elt)
	case *ast.MapType:
		return "map[" + exprString(e.Key) + "]" + exprString(e.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		var b strings.Builder
		b.WriteString("func(")
		b.WriteString(fieldListString(e.Params))
		b.WriteByte(')')
		if e.Results != nil && len(e.Results.List) > 0 {
			results := fieldListString(e.Results)
			if len(e.Results.List) == 1 && len(e.Results.List[0].Names) == 0 {
				b.WriteByte(' ')
				b.WriteString(results)
			} else {
				b.WriteString(" (")
				b.WriteString(results)
				b.WriteByte(')')
			}
		}
		return b.String()
	case *ast.ChanType:
		switch e.Dir {
		case ast.SEND:
			return "chan<- " + exprString(e.Value)
		case ast.RECV:
			return "<-chan " + exprString(e.Value)
		default:
			return "chan " + exprString(e.Value)
		}
	case *ast.Ellipsis:
		return "..." + exprString(e.Elt)
	case *ast.ParenExpr:
		return "(" + exprString(e.X) + ")"
	case *ast.IndexExpr:
		return exprString(e.X) + "[" + exprString(e.Index) + "]"
	case *ast.IndexListExpr:
		parts := make([]string, len(e.Indices))
		for i, idx := range e.Indices {
			parts[i] = exprString(idx)
		}
		return exprString(e.X) + "[" + strings.Join(parts, ", ") + "]"
	default:
		return "?"
	}
}

// FunctionBody holds a Go function/method name and its full source text.
type FunctionBody struct {
	Name string // e.g. "Run" or "(e *Engine) Run"
	Body string // full source text including signature line
}

// ExtractGoFunctionBodies parses a Go source file and returns full
// function/method bodies. Returns nil for non-Go files, test files,
// or parse errors (best-effort).
func ExtractGoFunctionBodies(filePath string) ([]FunctionBody, error) {
	if !strings.HasSuffix(filePath, ".go") {
		return nil, nil
	}
	if strings.HasSuffix(filePath, "_test.go") {
		return nil, nil
	}

	src, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("siblings: read %s: %w", filePath, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, 0)
	if err != nil {
		return nil, fmt.Errorf("siblings: parse %s: %w", filePath, err)
	}

	lines := strings.Split(string(src), "\n")
	var bodies []FunctionBody
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		startLine := fset.Position(fn.Pos()).Line - 1 // 0-based
		endLine := fset.Position(fn.End()).Line       // 1-based, inclusive

		if startLine < 0 {
			startLine = 0
		}
		if endLine > len(lines) {
			endLine = len(lines)
		}

		body := strings.Join(lines[startLine:endLine], "\n")
		bodies = append(bodies, FunctionBody{
			Name: formatFuncSignature(fn),
			Body: body,
		})
	}
	return bodies, nil
}

// BuildSiblingContext reads the plan result from state, extracts the
// referenced files, and returns a formatted context string grouped by file.
//
// For Go source files, full function bodies are injected so the implement
// phase sees the actual code it needs to modify. When a file's bodies would
// exceed the remaining budget, the function falls back to signatures only.
// Test files always get signatures (bodies are less useful for context).
//
// maxBytes limits the total size of the returned string. When maxBytes
// is 0, the default limit (maxSiblingContextBytes) is used.
func BuildSiblingContext(workDir string, planResult json.RawMessage, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = maxSiblingContextBytes
	}

	files, err := ExtractPlanFiles(planResult)
	if err != nil || len(files) == 0 {
		return ""
	}

	var sections []string
	totalBytes := 0
	for _, relPath := range files {
		absPath := filepath.Join(workDir, relPath)
		absResolved, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			continue
		}
		workDirResolved, err := filepath.EvalSymlinks(workDir)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(absResolved, workDirResolved+string(filepath.Separator)) && absResolved != workDirResolved {
			continue
		}
		if _, err = os.Stat(absPath); err != nil {
			continue
		}

		section := buildFileSection(absPath, relPath, maxBytes-totalBytes)
		if section == "" {
			continue
		}

		sepCost := 0
		if len(sections) > 0 {
			sepCost = 1
		}
		if totalBytes+sepCost+len(section) > maxBytes {
			break
		}
		totalBytes += sepCost + len(section)
		sections = append(sections, section)
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n")
}

// buildFileSection returns a formatted context section for a single file.
// For non-test Go files, it tries full function bodies first and falls
// back to signatures if bodies exceed remainingBudget. Test files and
// non-Go files always use signatures.
func buildFileSection(absPath, relPath string, remainingBudget int) string {
	isTest := strings.HasSuffix(relPath, "_test.go")

	// Try full bodies for non-test Go files.
	if !isTest && strings.HasSuffix(relPath, ".go") {
		bodies, err := ExtractGoFunctionBodies(absPath)
		if err == nil && len(bodies) > 0 {
			section := formatBodiesSection(relPath, bodies)
			if len(section) <= remainingBudget {
				return section
			}
			// Bodies too large — fall through to signatures.
		}
	}

	// Signatures as fallback (or primary for test files).
	var sigs []string
	var err error
	if isTest {
		sigs, err = ExtractGoTestPatterns(absPath)
	} else {
		sigs, err = ExtractGoSignatures(absPath)
	}
	if err != nil || len(sigs) == 0 {
		return ""
	}
	return formatSignaturesSection(relPath, sigs)
}

// formatBodiesSection renders a file section with full function bodies.
func formatBodiesSection(relPath string, bodies []FunctionBody) string {
	var b strings.Builder
	b.WriteString("### ")
	b.WriteString(relPath)
	b.WriteString("\n```go\n")
	for i, body := range bodies {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(body.Body)
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

// formatSignaturesSection renders a file section with function signatures.
func formatSignaturesSection(relPath string, sigs []string) string {
	var b strings.Builder
	b.WriteString("### ")
	b.WriteString(relPath)
	b.WriteByte('\n')
	for _, sig := range sigs {
		b.WriteString("- `")
		b.WriteString(sig)
		b.WriteString("`\n")
	}
	return b.String()
}

// isNewFile returns true if the given file (relative to workDir) does not
// exist on the baseBranch in git. This is used to detect files that the
// plan intends to create rather than modify.
func isNewFile(ctx context.Context, workDir, file, baseBranch string) bool {
	cmd := exec.CommandContext(ctx, "git", "show", baseBranch+":"+file)
	cmd.Dir = workDir
	if err := cmd.Run(); err != nil {
		return true
	}
	return false
}

// isGeneratedGoFile reads the first 512 bytes of a Go file and returns
// true if it contains the standard "// Code generated" marker.
func isGeneratedGoFile(absPath string) bool {
	f, err := os.Open(absPath)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return false
	}
	return strings.Contains(string(buf[:n]), "// Code generated")
}

// findExemplarFiles returns up to maxFiles non-test, non-generated Go source
// files from the given directory, sorted by modification time (newest first).
func findExemplarFiles(absDir string, maxFiles int) []string {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil
	}

	type fileEntry struct {
		path  string
		mtime int64
	}

	var candidates []fileEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		absPath := filepath.Join(absDir, name)
		if isGeneratedGoFile(absPath) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, fileEntry{path: absPath, mtime: info.ModTime().UnixNano()})
	}

	// Sort by modification time descending (newest first).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime > candidates[j].mtime
	})

	if len(candidates) > maxFiles {
		candidates = candidates[:maxFiles]
	}

	result := make([]string, len(candidates))
	for i, c := range candidates {
		result[i] = c.path
	}
	return result
}

// BuildPackageExemplars parses the plan result, identifies new files (not on
// baseBranch), finds existing Go files in the same package directories, and
// extracts function signatures to give the LLM guidance on naming conventions
// and API surface. maxBytes limits the total output size; when 0 the default
// maxPackageExemplarBytes is used. The context is threaded to isNewFile's git
// subprocess so cancellation (e.g. cost cap, timeout) stops spawned processes.
func BuildPackageExemplars(ctx context.Context, workDir string, planResult json.RawMessage, baseBranch string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = maxPackageExemplarBytes
	}

	files, err := ExtractPlanFiles(planResult)
	if err != nil || len(files) == 0 {
		return ""
	}

	// Collect unique package directories for new files only.
	seen := make(map[string]bool)
	var pkgDirs []string
	for _, relPath := range files {
		if !isNewFile(ctx, workDir, relPath, baseBranch) {
			continue
		}
		dir := filepath.Dir(relPath)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		pkgDirs = append(pkgDirs, dir)
	}

	if len(pkgDirs) == 0 {
		return ""
	}

	var sections []string
	totalBytes := 0

	for _, pkgDir := range pkgDirs {
		absDir := filepath.Join(workDir, pkgDir)

		// Validate path stays within workDir using EvalSymlinks.
		absResolved, err := filepath.EvalSymlinks(absDir)
		if err != nil {
			continue
		}
		workDirResolved, err := filepath.EvalSymlinks(workDir)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(absResolved, workDirResolved+string(filepath.Separator)) && absResolved != workDirResolved {
			continue
		}

		exemplars := findExemplarFiles(absDir, 5)
		if len(exemplars) == 0 {
			continue
		}

		for _, absPath := range exemplars {
			sigs, sigErr := ExtractGoSignatures(absPath)
			if sigErr != nil || len(sigs) == 0 {
				continue
			}

			// Build relative path for display.
			relPath, err := filepath.Rel(workDir, absPath)
			if err != nil {
				relPath = absPath
			}

			section := formatSignaturesSection(relPath, sigs)

			sepCost := 0
			if len(sections) > 0 {
				sepCost = 1
			}
			if totalBytes+sepCost+len(section) > maxBytes {
				return strings.Join(sections, "\n")
			}
			totalBytes += sepCost + len(section)
			sections = append(sections, section)
		}
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n")
}
