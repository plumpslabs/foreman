//go:build ts_symbols

package foreman

import "testing"

// TestTreeSitterImportsCoverBlindSpots is the payoff test for the opt-in
// extractor: every fixture, including the wrapped statements and Rust module
// declarations the line scanner cannot see, must resolve.
func TestTreeSitterImportsCoverBlindSpots(t *testing.T) {
	runImportCases(t, treeSitterImports{}, false)
}

// TestTreeSitterImportsDeclinesUnknownExtension keeps the fallback contract:
// an extension with no grammar must report "not understood" so extractImports
// falls back to the line scanner instead of dropping the file's edges.
func TestTreeSitterImportsDeclinesUnknownExtension(t *testing.T) {
	if _, ok := (treeSitterImports{}).Specifiers("notes.txt", []byte("import x from \"./y\";")); ok {
		t.Fatal("tree-sitter claimed to understand a .txt file")
	}
}

// TestTreeSitterImportsDeclinesUnparseable keeps the other half: a file that
// does not parse must not be reported as understood with a partial list.
func TestTreeSitterImportsDeclinesUnparseable(t *testing.T) {
	body := []byte("import { a } from \"./dep\";\nfunction ((( {\n")
	if _, ok := (treeSitterImports{}).Specifiers("src/broken.ts", body); ok {
		t.Fatal("tree-sitter claimed to understand a file with syntax errors")
	}
}

// TestTreeSitterImportsRustUseLists covers brace expansion, which the line
// scanner reports as a single unusable "a::{b" token.
func TestTreeSitterImportsRustUseLists(t *testing.T) {
	byRel := map[string]bool{
		"src/lib.rs":         true,
		"src/tickets.rs":     true,
		"src/agents.rs":      true,
		"src/tickets/mod.rs": true,
	}
	body := []byte("use crate::{tickets, agents};\nmod tickets;\n")
	specs, ok := (treeSitterImports{}).Specifiers("src/lib.rs", body)
	if !ok {
		t.Fatal("tree-sitter declined a valid rust file")
	}
	got := map[string]bool{}
	for _, spec := range specs {
		if dst := resolveImportSpecifier("src/lib.rs", "src", spec, "", byRel); dst != "" {
			got[dst] = true
		}
	}
	for _, want := range []string{"src/tickets.rs", "src/agents.rs"} {
		if !got[want] {
			t.Errorf("missing %q from use list (specifiers=%v)", want, specs)
		}
	}
}

// TestTreeSitterImportsInlineModuleIsNotAnEdge: `mod foo { … }` defines a
// module inline, so it imports no file.
func TestTreeSitterImportsInlineModuleIsNotAnEdge(t *testing.T) {
	specs, ok := (treeSitterImports{}).Specifiers("src/lib.rs", []byte("mod inline { fn f() {} }\n"))
	if !ok {
		t.Fatal("tree-sitter declined a valid rust file")
	}
	if len(specs) != 0 {
		t.Fatalf("inline module produced specifiers %v", specs)
	}
}
