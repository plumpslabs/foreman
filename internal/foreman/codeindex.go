package foreman

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CodeHit is one search result from the code index.
type CodeHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// initCodeIndex creates the FTS5 search tables. If FTS5 is unavailable it
// disables the index (fts5=false) and returns nil, so search_index falls back
// to a plain scan and the rest of the app keeps working.
func (d *DB) initCodeIndex() error {
	if _, err := d.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS code_index USING fts5(path UNINDEXED, body, tokenize='unicode61')`); err != nil {
		d.fts5 = false
		return nil
	}
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS code_files (path TEXT PRIMARY KEY, mtime INTEGER NOT NULL, size INTEGER NOT NULL)`); err != nil {
		return err
	}
	d.fts5 = true
	return nil
}

// initSymbols creates the persisted declaration map. It is plain SQLite (no
// FTS5 requirement), so symbol lookup keeps working even when FTS5 is absent.
func (d *DB) initSymbols() error {
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS symbols (
		path TEXT NOT NULL,
		kind TEXT NOT NULL,
		name TEXT NOT NULL,
		line INTEGER NOT NULL,
		end_line INTEGER NOT NULL,
		signature TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return err
	}
	if _, err := d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name)`); err != nil {
		return err
	}
	if _, err := d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_symbols_path ON symbols(path)`); err != nil {
		return err
	}
	return nil
}

// IndexWorkspace incrementally indexes source files under root: only files
// whose mtime/size changed are re-read, and files deleted from disk are
// dropped from the index. Returns (indexed, removed).
func (d *DB) IndexWorkspace(root string) (int, int, error) {
	if !d.fts5 {
		return 0, 0, nil
	}
	type finfo struct{ mtime, size int64 }
	seen := map[string]finfo{}
	if err := walkProject(root, func(path, rel string, info os.FileInfo) error {
		if !isSourceFile(rel) || info.Size() > 512<<10 {
			return nil
		}
		seen[rel] = finfo{info.ModTime().Unix(), info.Size()}
		return nil
	}); err != nil {
		return 0, 0, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	existing := map[string]finfo{}
	rows, err := d.db.Query(`SELECT path, mtime, size FROM code_files`)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var p string
		var fi finfo
		if err := rows.Scan(&p, &fi.mtime, &fi.size); err != nil {
			rows.Close()
			return 0, 0, err
		}
		existing[p] = fi
	}
	rows.Close()

	var toIndex, toRemove []string
	for rel, fi := range seen {
		if ex, ok := existing[rel]; !ok || ex.mtime != fi.mtime || ex.size != fi.size {
			toIndex = append(toIndex, rel)
		}
	}
	for rel := range existing {
		if _, ok := seen[rel]; !ok {
			toRemove = append(toRemove, rel)
		}
	}

	for _, rel := range toRemove {
		_, _ = d.db.Exec(`DELETE FROM code_index WHERE path = ?`, rel)
		_, _ = d.db.Exec(`DELETE FROM code_files WHERE path = ?`, rel)
		_, _ = d.db.Exec(`DELETE FROM symbols WHERE path = ?`, rel)
	}
	indexed := 0
	for _, rel := range toIndex {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		_, _ = d.db.Exec(`DELETE FROM code_index WHERE path = ?`, rel)
		if _, err := d.db.Exec(`INSERT INTO code_index(path, body) VALUES(?, ?)`, rel, string(data)); err != nil {
			continue
		}
		_, _ = d.db.Exec(`INSERT OR REPLACE INTO code_files(path, mtime, size) VALUES(?, ?, ?)`,
			rel, seen[rel].mtime, seen[rel].size)
		rebuildSymbols(d.db, rel, data)
		indexed++
	}
	return indexed, len(toRemove), nil
}

// rebuildSymbols replaces the symbol rows of one indexed file. Extraction is
// deterministic and cheap (stdlib AST for Go, line heuristics otherwise);
// failures are tolerated — a broken file just yields no symbols.
func rebuildSymbols(db *sql.DB, rel string, data []byte) {
	syms := extractSymbols(rel, data)
	if len(syms) == 0 {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO symbols(path, kind, name, line, end_line, signature) VALUES(?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return
	}
	defer stmt.Close()
	for _, s := range syms {
		if _, err := stmt.Exec(rel, s.Kind, s.Name, s.Line, s.EndLine, s.Signature); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// SymbolsForFile returns the indexed declarations of one workspace file.
func (d *DB) SymbolsForFile(path string) ([]Symbol, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT path, kind, name, line, end_line, signature FROM symbols WHERE path = ? ORDER BY line`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var syms []Symbol
	for rows.Next() {
		var s Symbol
		if err := rows.Scan(&s.Path, &s.Kind, &s.Name, &s.Line, &s.EndLine, &s.Signature); err != nil {
			return nil, err
		}
		syms = append(syms, s)
	}
	return syms, rows.Err()
}

// SearchSymbols finds declarations whose name contains query (case-insensitive
// substring), ordered by path then line, capped at limit.
func (d *DB) SearchSymbols(query string, limit int) ([]Symbol, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	pattern := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT path, kind, name, line, end_line, signature FROM symbols
		WHERE lower(name) LIKE ? ORDER BY path, line LIMIT ?`, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var syms []Symbol
	for rows.Next() {
		var s Symbol
		if err := rows.Scan(&s.Path, &s.Kind, &s.Name, &s.Line, &s.EndLine, &s.Signature); err != nil {
			return nil, err
		}
		syms = append(syms, s)
	}
	return syms, rows.Err()
}

// ftsQuery builds an FTS5 MATCH expression: each term quoted and AND-ed.
func ftsQuery(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " ")
}

// SearchWorkspace finds up to limit lines matching the query. It uses the FTS5
// index to shortlist candidate files, then scans those files for exact lines
// (so hits carry real line numbers). Returns (hits, usedIndex).
func (d *DB) SearchWorkspace(root, query string, limit int) ([]CodeHit, bool, error) {
	if limit <= 0 {
		limit = 30
	}
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return nil, d.fts5, nil
	}
	lower := make([]string, 0, len(terms))
	for _, t := range terms {
		lower = append(lower, strings.ToLower(t))
	}

	var candidates []string
	usedIndex := d.fts5
	if d.fts5 {
		d.mu.RLock()
		rows, err := d.db.Query(`SELECT path FROM code_index WHERE code_index MATCH ? LIMIT 200`, ftsQuery(terms))
		if err == nil {
			for rows.Next() {
				var p string
				if rows.Scan(&p) == nil {
					candidates = append(candidates, p)
				}
			}
			rows.Close()
		}
		d.mu.RUnlock()
	}
	if !d.fts5 {
		_ = walkProject(root, func(path, rel string, info os.FileInfo) error {
			if isSourceFile(rel) && info.Size() <= 512<<10 {
				candidates = append(candidates, rel)
			}
			return nil
		})
	}

	var hits []CodeHit
	for _, rel := range candidates {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			low := strings.ToLower(line)
			matched := true
			for _, t := range lower {
				if !strings.Contains(low, t) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 240 {
				trimmed = trimmed[:240] + "…"
			}
			hits = append(hits, CodeHit{Path: rel, Line: i + 1, Text: trimmed})
			if len(hits) >= limit {
				return hits, usedIndex, nil
			}
		}
	}
	return hits, usedIndex, nil
}

// Store wrappers -----------------------------------------------------------------

// IndexWorkspace delegates to the underlying database. It also opportunistically
// decays stale signal edges so the learned co-usage graph keeps tracking what
// matters NOW — no background job required.
func (s *Store) IndexWorkspace(root string) (int, int, error) {
	if s.db == nil {
		return 0, 0, nil
	}
	if err := s.db.SignalsDecay(); err != nil {
		// Decay is best-effort; indexing must not fail because of it.
		slog.Warn("signals decay failed", "err", err)
	}
	// Compact the journal so the active table does not grow without bound: old
	// entries for the same room+file collapse to their newest row. Best-effort.
	if _, err := s.db.JournalTTLPrune(90 * 24 * time.Hour); err != nil {
		slog.Warn("journal prune failed", "err", err)
	}
	indexed, removed, err := s.db.IndexWorkspace(root)
	// Structural import edges rebuild opportunistically with the index; they
	// give the graph a warm start without waiting for agent runs.
	s.refreshSignalImports(root)
	return indexed, removed, err
}

// SearchWorkspace delegates to the underlying database.
func (s *Store) SearchWorkspace(root, query string, limit int) ([]CodeHit, bool, error) {
	if s.db == nil {
		return nil, false, nil
	}
	return s.db.SearchWorkspace(root, query, limit)
}

// SymbolsForFile delegates to the underlying database.
func (s *Store) SymbolsForFile(path string) ([]Symbol, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.SymbolsForFile(path)
}

// SearchSymbols delegates to the underlying database.
func (s *Store) SearchSymbols(query string, limit int) ([]Symbol, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.SearchSymbols(query, limit)
}
