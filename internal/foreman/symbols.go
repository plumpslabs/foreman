package foreman

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Symbol is one indexed declaration with its exact (or best-effort) line range.
// The persisted symbol map lets an agent locate a function/type without
// grepping the whole workspace, then read precisely with view_file
// start_line/end_line. Go files get exact ranges from the stdlib AST parser;
// other languages use the heuristic extractor (name + start line, end line
// estimated from braces/indentation).
type Symbol struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Line      int    `json:"line"`
	EndLine   int    `json:"end_line"`
	Signature string `json:"signature"`
}

// symbolSource extracts up to max declaration symbols from one source file.
// Implementations must never panic on malformed input.
type symbolSource interface {
	Symbols(rel string, src []byte, max int) []string
}

// heuristicSymbols is the original line-based extractor. It works for any
// language and is the fallback when no tree-sitter grammar is available.
type heuristicSymbols struct{}

func (heuristicSymbols) Symbols(_ string, src []byte, max int) []string {
	var syms []string
	for _, line := range strings.Split(string(src), "\n") {
		if s := symbolizeLine(line); s != "" {
			syms = append(syms, s)
			if len(syms) >= max {
				break
			}
		}
	}
	return syms
}

// repoSymbols is the configured extractor. Default builds use the heuristic
// extractor (no extra dependencies); builds with `-tags ts_symbols` swap in the
// pure-Go tree-sitter extractor (see symbols_treesitter.go).
var repoSymbols symbolSource = heuristicSymbols{}

// fileSymbols returns declaration symbols for one file, preferring the
// configured source and falling back to the heuristic.
func fileSymbols(rel string, src []byte, max int) []string {
	if syms := repoSymbols.Symbols(rel, src, max); len(syms) > 0 {
		return syms
	}
	return heuristicSymbols{}.Symbols(rel, src, max)
}

// extractSymbols returns structured declarations with line ranges for one
// source file. Go files are parsed with the stdlib AST (exact positions);
// every other language falls back to the heuristic extractor.
func extractSymbols(rel string, src []byte) []Symbol {
	if strings.EqualFold(extOf(rel), ".go") {
		if syms := goASTSymbols(src); len(syms) > 0 {
			return syms
		}
	}
	return heuristicSymbolsDetailed(src)
}

func extOf(rel string) string {
	if i := strings.LastIndexByte(rel, '.'); i >= 0 {
		return rel[i:]
	}
	return ""
}

// goASTSymbols extracts functions, methods, structs, and interfaces from Go
// source using the standard library parser. Positions are exact. It never
// panics on malformed input — a parse error yields nil.
func goASTSymbols(src []byte) []Symbol {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(src), "\n")
	var syms []Symbol
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			kind := "func"
			name := decl.Name.Name
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				kind = "method"
				name = recvTypeName(decl.Recv.List[0].Type) + "." + name
			}
			syms = append(syms, Symbol{
				Kind: kind, Name: name,
				Line:      fset.Position(decl.Pos()).Line,
				EndLine:   fset.Position(decl.End()).Line,
				Signature: sigLine(lines, fset.Position(decl.Pos()).Line),
			})
		case *ast.GenDecl:
			if decl.Tok != token.TYPE {
				continue
			}
			for _, spec := range decl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				kind := "type"
				switch ts.Type.(type) {
				case *ast.StructType:
					kind = "struct"
				case *ast.InterfaceType:
					kind = "interface"
				}
				syms = append(syms, Symbol{
					Kind: kind, Name: ts.Name.Name,
					Line:      fset.Position(ts.Pos()).Line,
					EndLine:   fset.Position(ts.End()).Line,
					Signature: sigLine(lines, fset.Position(ts.Pos()).Line),
				})
			}
		}
	}
	return syms
}

// recvTypeName renders a receiver type as its plain name ("T" for "*T").
func recvTypeName(t ast.Expr) string {
	switch v := t.(type) {
	case *ast.StarExpr:
		return recvTypeName(v.X)
	case *ast.Ident:
		return v.Name
	case *ast.IndexExpr:
		return recvTypeName(v.X)
	case *ast.IndexListExpr:
		return recvTypeName(v.X)
	default:
		return ""
	}
}

// sigLine returns the trimmed first source line of a declaration, capped.
func sigLine(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	s := strings.TrimSpace(lines[line-1])
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

// heuristicSymbolsDetailed is the line-based extractor with line ranges. The
// start line is exact (the declaration); the end line is estimated from brace
// balance (brace languages) or indentation (Python/Ruby-style), and degrades
// to the declaration line when nothing reliable is found.
func heuristicSymbolsDetailed(src []byte) []Symbol {
	lines := strings.Split(string(src), "\n")
	var syms []Symbol
	for i, line := range lines {
		name, kind := declNameKind(line)
		if name == "" {
			continue
		}
		end := estimateEnd(lines, i)
		if end < i+1 {
			end = i + 1
		}
		s := strings.TrimSpace(line)
		if len(s) > 160 {
			s = s[:160] + "..."
		}
		syms = append(syms, Symbol{
			Kind: kind, Name: name,
			Line: i + 1, EndLine: end, Signature: s,
		})
	}
	return syms
}

// declNameKind matches a declaration line, returning the symbol name and kind.
// It reuses the same modifier list as reDefLine but also captures the keyword
// so `export class Greeter` is classified as a class, not a generic decl.
func declNameKind(line string) (string, string) {
	if m := reGoMethod.FindStringSubmatch(line); m != nil {
		return m[1], "method"
	}
	m := reDefLineWithKw.FindStringSubmatch(line)
	if m == nil {
		return "", ""
	}
	kw, name := m[1], m[2]
	switch kw {
	case "func", "function", "fn", "fun", "def":
		return name, "func"
	case "class":
		return name, "class"
	case "struct":
		return name, "struct"
	case "interface":
		return name, "interface"
	case "enum":
		return name, "enum"
	case "trait":
		return name, "trait"
	case "type":
		return name, "type"
	case "const", "let", "var":
		return name, "var"
	}
	return name, "decl"
}

// estimateEnd guesses the last line of a declaration body starting at startIdx.
// Brace-based when the declaration opens a block; indent-based otherwise.
func estimateEnd(lines []string, startIdx int) int {
	start := lines[startIdx]
	if strings.Contains(start, "{") {
		depth := 0
		for i := startIdx; i < len(lines) && i < startIdx+500; i++ {
			depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			if depth <= 0 && i > startIdx {
				return i + 1
			}
		}
	}
	// Indent fallback (Python, Ruby, ...): body lines are more indented than
	// the declaration; the next declaration at equal/lower indent ends it.
	indent := leadingSpaces(start)
	for i := startIdx + 1; i < len(lines) && i < startIdx+500; i++ {
		ln := lines[i]
		if strings.TrimSpace(ln) == "" || strings.HasPrefix(strings.TrimSpace(ln), "#") || strings.HasPrefix(strings.TrimSpace(ln), "//") {
			continue
		}
		if leadingSpaces(ln) <= indent {
			return i
		}
	}
	if len(lines) > startIdx {
		return len(lines)
	}
	return startIdx + 1
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
}
