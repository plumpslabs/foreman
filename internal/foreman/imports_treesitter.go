//go:build ts_symbols

package foreman

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

func init() {
	fileImports = treeSitterImports{}
	importsUseTreeSitter = true
}

// treeSitterImports reads import statements off a real syntax tree instead of
// scanning lines, so statements that wrap across lines (a named-import list
// spread over several lines, a prettier-formatted `export { … } from`) are
// found exactly like single-line ones.
//
// Only the opt-in ts_symbols build uses this; default builds keep the
// dependency-free line scanner. Resolution is unchanged and still fails
// closed, so a specifier this extractor reports that cannot be pinned to one
// workspace file is dropped exactly as before.
type treeSitterImports struct{}

// maxImportSpecifiersPerFile bounds a single pathological file. Real files
// have a few dozen imports at most.
const maxImportSpecifiersPerFile = 512

func (treeSitterImports) Specifiers(rel string, src []byte) ([]string, bool) {
	lang := tsLanguageFor(rel)
	if lang == nil {
		return nil, false
	}
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil || tree == nil {
		return nil, false
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		return nil, false
	}
	// A file with syntax errors may have lost or mangled its import nodes.
	// Hand it to the line scanner instead of silently reporting fewer edges.
	if root.HasError() {
		return nil, false
	}

	var out []string
	add := func(s string) {
		if s == "" || len(out) >= maxImportSpecifiersPerFile {
			return
		}
		out = append(out, s)
	}
	var walk func(n *gotreesitter.Node, depth int)
	walk = func(n *gotreesitter.Node, depth int) {
		if n == nil || depth > 32 || len(out) >= maxImportSpecifiersPerFile {
			return
		}
		switch n.Type(lang) {
		case "import_statement":
			// TypeScript/JavaScript: import … from "x". Python: import a.b.
			// The two share a node type, so the presence of a `source` field
			// decides which grammar produced it.
			if s := n.ChildByFieldName("source", lang); s != nil {
				add(trimQuotes(s.Text(src)))
			} else {
				for i := 0; i < n.NamedChildCount(); i++ {
					if c := n.NamedChild(i); c != nil && c.Type(lang) == "dotted_name" {
						add(strings.TrimSpace(c.Text(src)))
					}
				}
			}
		case "export_statement":
			// export { y } from "x" / export * from "x".
			add(tsSourceField(n, lang, src))
		case "call_expression":
			// require("x") and dynamic import("x") may appear anywhere,
			// including inside function bodies.
			fn := n.ChildByFieldName("function", lang)
			if fn != nil {
				switch fn.Text(src) {
				case "require", "import":
					add(tsFirstStringArgument(n, lang, src))
				}
			}
		case "import_from_statement":
			// python: from <module> import a, b
			if mod := n.ChildByFieldName("module_name", lang); mod != nil {
				add(strings.TrimSpace(mod.Text(src)))
			}
		case "use_declaration":
			// rust: use a::b::c; use a::{b, c}; use crate::x;
			if arg := n.ChildByFieldName("argument", lang); arg != nil {
				for _, s := range rustUsePaths(arg, lang, src) {
					add(s)
				}
			}
		case "mod_item":
			// rust: mod foo; declares a module file next to this one. Emit it
			// as a relative specifier so resolution finds foo.rs or
			// foo/mod.rs. An inline `mod foo { … }` has a body and imports
			// nothing.
			if n.ChildByFieldName("body", lang) == nil {
				if name := n.ChildByFieldName("name", lang); name != nil {
					if v := strings.TrimSpace(name.Text(src)); v != "" {
						add("./" + v)
					}
				}
			}
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), depth+1)
		}
	}
	walk(root, 0)
	return out, true
}

// tsSourceField returns the string literal of an import/export statement.
func tsSourceField(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	s := n.ChildByFieldName("source", lang)
	if s == nil {
		return ""
	}
	return trimQuotes(s.Text(src))
}

// tsFirstStringArgument returns the first string literal inside a call's
// argument list: require("./x") → ./x.
func tsFirstStringArgument(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) string {
	args := n.ChildByFieldName("arguments", lang)
	if args == nil {
		return ""
	}
	for i := 0; i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		if t := c.Type(lang); t == "string" || t == "template_string" {
			return trimQuotes(c.Text(src))
		}
		return "" // first argument is not a literal: nothing to resolve
	}
	return ""
}

// rustUsePaths flattens a use tree into dotted paths, expanding brace lists:
// `a::{b, c}` → a::b, a::c. Glob suffixes (`a::*`) and aliases are dropped by
// resolution, so they are emitted as-is.
func rustUsePaths(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []string {
	if n == nil {
		return nil
	}
	switch n.Type(lang) {
	case "scoped_use_list":
		path := n.ChildByFieldName("path", lang)
		list := n.ChildByFieldName("list", lang)
		if path == nil || list == nil {
			return []string{strings.TrimSpace(n.Text(src))}
		}
		prefix := strings.TrimSpace(path.Text(src))
		var out []string
		for i := 0; i < list.NamedChildCount(); i++ {
			c := list.NamedChild(i)
			if c == nil {
				continue
			}
			for _, tail := range rustUsePaths(c, lang, src) {
				out = append(out, prefix+"::"+tail)
			}
		}
		return out
	case "use_as_clause":
		if p := n.ChildByFieldName("path", lang); p != nil {
			return rustUsePaths(p, lang, src)
		}
	}
	return []string{strings.TrimSpace(n.Text(src))}
}

// trimQuotes strips one layer of surrounding quotes from a string literal.
func trimQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if q := s[0]; (q == '"' || q == '\'' || q == '`') && s[len(s)-1] == q {
			return s[1 : len(s)-1]
		}
	}
	return s
}
