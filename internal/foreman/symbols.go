package foreman

import "strings"

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
