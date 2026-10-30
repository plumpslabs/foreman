//go:build ts_symbols

package foreman

import (
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter"
	langc "github.com/odvcencio/gotreesitter/grammars/c"
	langgo "github.com/odvcencio/gotreesitter/grammars/go"
	langjava "github.com/odvcencio/gotreesitter/grammars/java"
	langjs "github.com/odvcencio/gotreesitter/grammars/javascript"
	langpy "github.com/odvcencio/gotreesitter/grammars/python"
	langrb "github.com/odvcencio/gotreesitter/grammars/ruby"
	langrs "github.com/odvcencio/gotreesitter/grammars/rust"
	langtsx "github.com/odvcencio/gotreesitter/grammars/tsx"
	langts "github.com/odvcencio/gotreesitter/grammars/typescript"
)

func init() { repoSymbols = treeSitterSymbols{} }

// treeSitterSymbols extracts top-level declarations from a syntax tree using a
// pure-Go tree-sitter grammar. Returns nil when the file extension has no
// grammar, so callers fall back to the heuristic extractor.
type treeSitterSymbols struct{}

func (treeSitterSymbols) Symbols(rel string, src []byte, max int) []string {
	lang := tsLanguageFor(rel)
	if lang == nil {
		return nil
	}
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Release()

	var out []string
	seen := map[string]bool{}
	var walk func(n *gotreesitter.Node, depth int)
	walk = func(n *gotreesitter.Node, depth int) {
		if n == nil || depth > 6 || len(out) >= max {
			return
		}
		typ := n.Type(lang)
		if isDefinitionType(typ) {
			if nameNode := n.ChildByFieldName("name", lang); nameNode != nil {
				name := strings.TrimSpace(nameNode.Text(src))
				if key := typ + ":" + name; name != "" && !seen[key] {
					seen[key] = true
					out = append(out, formatSymbol(typ, name))
				}
			}
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			child := n.NamedChild(i)
			if child == nil {
				continue
			}
			// Do not descend into bodies or parameter/argument lists: they
			// contain local declarations that are not repo-map symbols.
			if isSkipType(child.Type(lang)) {
				continue
			}
			walk(child, depth+1)
		}
	}
	walk(tree.RootNode(), 0)
	return out
}

func tsLanguageFor(rel string) *gotreesitter.Language {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		return langgo.Language()
	case ".py":
		return langpy.Language()
	case ".js", ".jsx", ".mjs", ".cjs":
		return langjs.Language()
	case ".ts":
		return langts.Language()
	case ".tsx":
		return langtsx.Language()
	case ".rs":
		return langrs.Language()
	case ".java":
		return langjava.Language()
	case ".c", ".h":
		return langc.Language()
	case ".rb":
		return langrb.Language()
	}
	return nil
}

func isDefinitionType(typ string) bool {
	for _, k := range []string{
		"declaration", "definition", "_spec", "_item", "class", "interface",
		"trait", "struct", "enum", "impl", "type_alias", "function", "method",
		"module", "namespace",
	} {
		if strings.Contains(typ, k) {
			return true
		}
	}
	return false
}

func isSkipType(typ string) bool {
	for _, k := range []string{"block", "body", "parameter", "argument", "expression", "field"} {
		if strings.Contains(typ, k) {
			return true
		}
	}
	return false
}

func formatSymbol(typ, name string) string {
	switch {
	case strings.Contains(typ, "function"), strings.Contains(typ, "method"):
		return "func " + name
	case strings.Contains(typ, "class"):
		return "class " + name
	case strings.Contains(typ, "interface"):
		return "interface " + name
	case strings.Contains(typ, "struct"):
		return "struct " + name
	case strings.Contains(typ, "enum"):
		return "enum " + name
	case strings.Contains(typ, "trait"):
		return "trait " + name
	case strings.Contains(typ, "impl"):
		return "impl " + name
	case strings.Contains(typ, "type"):
		return "type " + name
	}
	return name
}
