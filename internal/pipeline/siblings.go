package pipeline

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
// non-Go files or parse errors (best-effort).
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
	default:
		return "?"
	}
}

// BuildSiblingContext reads the plan result from state, extracts the
// referenced files, and returns a formatted string of function
// signatures grouped by file. Returns an empty string if the plan
// result is unavailable or contains no Go files.
func BuildSiblingContext(workDir string, planResult json.RawMessage) string {
	files, err := ExtractPlanFiles(planResult)
	if err != nil || len(files) == 0 {
		return ""
	}

	var sections []string
	totalBytes := 0
	for _, relPath := range files {
		absPath := filepath.Join(workDir, relPath)
		if _, err := os.Stat(absPath); err != nil {
			continue
		}

		sigs, err := ExtractGoSignatures(absPath)
		if err != nil || len(sigs) == 0 {
			continue
		}

		var b strings.Builder
		b.WriteString("### ")
		b.WriteString(relPath)
		b.WriteByte('\n')
		for _, sig := range sigs {
			b.WriteString("- `")
			b.WriteString(sig)
			b.WriteString("`\n")
		}

		section := b.String()
		if totalBytes+len(section) > maxSiblingContextBytes {
			break
		}
		totalBytes += len(section)
		sections = append(sections, section)
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n")
}
