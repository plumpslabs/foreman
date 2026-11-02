package foreman

import (
	"os"
	"path/filepath"
	"testing"
)

const goSample = `package svc

import "errors"

// Options configures the service.
type Options struct {
	Timeout int
}

type Client interface {
	Do() error
}

func (c *Client) Do() error {
	return errors.New("todo")
}

func NewClient(opts Options) (*Client, error) {
	return nil, nil
}

var ErrClosed = errors.New("closed")
`

func TestGoSymbolExtraction(t *testing.T) {
	syms := extractSymbols("svc.go", []byte(goSample))
	byName := map[string]Symbol{}
	for _, s := range syms {
		byName[s.Name] = s
	}
	want := map[string]Symbol{
		"Options":   {Kind: "struct", Line: 6, EndLine: 8},
		"Client":    {Kind: "interface", Line: 10, EndLine: 12},
		"Client.Do": {Kind: "method", Line: 14, EndLine: 16},
		"NewClient": {Kind: "func", Line: 18, EndLine: 20},
	}
	if len(syms) != len(want) {
		t.Fatalf("extractSymbols = %d symbols, want %d (%+v)", len(syms), len(want), syms)
	}
	for name, exp := range want {
		got, ok := byName[name]
		if !ok {
			t.Fatalf("missing symbol %q in %+v", name, syms)
		}
		if got.Kind != exp.Kind || got.Line != exp.Line || got.EndLine != exp.EndLine {
			t.Fatalf("symbol %s = %+v, want kind=%s line=%d end=%d", name, got, exp.Kind, exp.Line, exp.EndLine)
		}
	}
	// const/var declarations are deliberately not part of the map.
	if _, ok := byName["ErrClosed"]; ok {
		t.Fatalf("expected ErrClosed (var) to be excluded, got %+v", byName["ErrClosed"])
	}
}

func TestGoSymbolExtractionMalformed(t *testing.T) {
	// Broken Go must never panic. The AST parser yields nothing, so extraction
	// degrades to the heuristic line extractor — best-effort is fine here.
	_ = extractSymbols("broken.go", []byte("package p\nfunc nope( {"))
}

const jsSample = `export function greet(name) {
  return "hi " + name;
}

export class Greeter {
  constructor() {
    this.name = "g";
  }
  say() {
    return "hi";
  }
}
`

const pySample = `def authenticate(user, password):
    token = make_token(user)
    return token

class AuthService:
    def login(self):
        return authenticate(self.user, self.password)
`

func TestHeuristicSymbolExtraction(t *testing.T) {
	js := extractSymbols("app.js", []byte(jsSample))
	if len(js) != 2 {
		t.Fatalf("js extractSymbols = %+v, want 2 symbols (greet + Greeter)", js)
	}
	jsByName := map[string]Symbol{}
	for _, s := range js {
		jsByName[s.Name] = s
	}
	if g := jsByName["greet"]; g.Kind != "func" || g.Line != 1 || g.EndLine != 3 {
		t.Fatalf("greet = %+v, want func line 1..3", g)
	}
	if c := jsByName["Greeter"]; c.Kind != "class" || c.Line != 5 || c.EndLine != 12 {
		t.Fatalf("Greeter = %+v, want class line 5..12", c)
	}

	py := extractSymbols("auth.py", []byte(pySample))
	// Heuristic extracts nested methods too (login) — consistent with Go
	// method extraction, and useful for locating definitions.
	pyByName := map[string]Symbol{}
	for _, s := range py {
		pyByName[s.Name] = s
	}
	if len(py) != 3 {
		t.Fatalf("py extractSymbols = %+v, want 3 symbols (authenticate + AuthService + login)", py)
	}
	if a := pyByName["authenticate"]; a.Kind != "func" || a.Line != 1 || a.EndLine != 4 {
		t.Fatalf("authenticate = %+v, want func line 1..4", a)
	}
	if c := pyByName["AuthService"]; c.Kind != "class" || c.Line != 5 || c.EndLine != 8 {
		t.Fatalf("AuthService = %+v, want class line 5..8", c)
	}
	if l := pyByName["login"]; l.Kind != "func" || l.Line != 6 {
		t.Fatalf("login = %+v, want func line 6", l)
	}
}

func TestSymbolsIndexLifecycle(t *testing.T) {
	dir := t.TempDir()
	isolateGlobal(t)
	writeFile(t, dir, "svc.go", goSample)
	writeFile(t, dir, "app.js", jsSample)

	st, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()
	if st.db == nil || !st.db.fts5 {
		t.Skip("FTS5 unavailable in this build")
	}

	if _, _, err := st.IndexWorkspace(dir); err != nil {
		t.Fatalf("IndexWorkspace: %v", err)
	}

	// Per-file lookup.
	syms, err := st.SymbolsForFile("svc.go")
	if err != nil {
		t.Fatalf("SymbolsForFile: %v", err)
	}
	if len(syms) != 4 {
		t.Fatalf("SymbolsForFile(svc.go) = %d symbols, want 4 (%+v)", len(syms), syms)
	}

	// Name search.
	opts, err := st.SearchSymbols("Options", 10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(opts) != 1 || opts[0].Name != "Options" || opts[0].Path != "svc.go" || opts[0].Line != 6 {
		t.Fatalf("SearchSymbols(Options) = %+v", opts)
	}

	// Case-insensitive substring: "client" also matches NewClient, so assert
	// the two real Client symbols are present (plus the case-insensitivity).
	clients, err := st.SearchSymbols("client", 10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	names := map[string]bool{}
	for _, c := range clients {
		names[c.Name] = true
	}
	if !names["Client"] || !names["Client.Do"] {
		t.Fatalf("SearchSymbols(client) = %+v, want both Client and Client.Do", clients)
	}
	if got, _ := st.SearchSymbols("CLIENT", 10); len(got) != len(clients) {
		t.Fatalf("expected case-insensitive search to match, got %d vs %d", len(got), len(clients))
	}

	// Modify a file: the map must pick up the new declaration.
	updated := goSample + "\nfunc Purge() error {\n\treturn nil\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "svc.go"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.IndexWorkspace(dir); err != nil {
		t.Fatalf("IndexWorkspace after edit: %v", err)
	}
	purge, err := st.SearchSymbols("Purge", 10)
	if err != nil {
		t.Fatalf("SearchSymbols(Purge): %v", err)
	}
	if len(purge) != 1 || purge[0].Line != 24 {
		t.Fatalf("SearchSymbols(Purge) = %+v, want line 24", purge)
	}

	// Delete a file: its symbols must vanish.
	if err := os.Remove(filepath.Join(dir, "app.js")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.IndexWorkspace(dir); err != nil {
		t.Fatalf("IndexWorkspace after delete: %v", err)
	}
	js, err := st.SymbolsForFile("app.js")
	if err != nil {
		t.Fatalf("SymbolsForFile: %v", err)
	}
	if len(js) != 0 {
		t.Fatalf("expected app.js symbols to be removed, got %+v", js)
	}
}
