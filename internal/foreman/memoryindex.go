package foreman

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MemoryFact is one free-text note an agent retained for later runs: a "fact"
// (durable state of the workspace) or a "lesson" (what worked or failed on a
// finished task). Unlike the key-value `memory` table, facts are recalled by
// meaning — FTS5 full-text search — so the next run can pull back what is
// relevant without knowing the exact key. This is the retain/recall pattern
// used by agent-memory engines (Hindsight, mnemopi), built on the same FTS5
// index that already backs the code search (no new dependency, no CGO).
type MemoryFact struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	Kind      string    `json:"kind"` // fact | lesson
	Body      string    `json:"body"`
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"created_at"`
	UsedAt    time.Time `json:"used_at"`
	UsedCount int       `json:"used_count"`
}

const maxMemoryFactChars = 1000

// initMemoryIndex creates the facts table plus its FTS5 mirror. Facts stay
// readable even without FTS5 — recall falls back to a plain scan.
func (d *DB) initMemoryIndex() error {
	if _, err := d.db.Exec(`
	CREATE TABLE IF NOT EXISTS memory_facts (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT 'fact',
		body TEXT NOT NULL,
		agent TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		used_at DATETIME,
		used_count INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	if _, err := d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_facts_room ON memory_facts(room_id, created_at)`); err != nil {
		return err
	}
	if d.fts5 {
		if _, err := d.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(id UNINDEXED, body, tokenize='unicode61')`); err != nil {
			// The mirror is an optimization only; plain-scan recall still works.
			d.fts5 = false
		}
	}
	return nil
}

func normalizeMemoryKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "lesson") {
		return "lesson"
	}
	return "fact"
}

// clipForMemory trims a summary/lesson text to a prompt-friendly length.
func clipForMemory(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const n = 400
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// SaveMemoryFact retains one note. An identical body in the same room is
// idempotent (returns the existing fact), so repeated auto-lessons or model
// retries never spam the bank. Returns the stored fact.
func (d *DB) SaveMemoryFact(roomID, agent, kind, body string) (MemoryFact, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return MemoryFact{}, fmt.Errorf("memory body is required")
	}
	if len(body) > maxMemoryFactChars {
		body = body[:maxMemoryFactChars]
	}
	kind = normalizeMemoryKind(kind)

	d.mu.Lock()
	defer d.mu.Unlock()
	var existing MemoryFact
	var usedAt sql.NullTime
	err := d.db.QueryRow(`SELECT id, room_id, kind, body, agent, created_at, used_at, used_count
		FROM memory_facts WHERE room_id = ? AND body = ?`, roomID, body).
		Scan(&existing.ID, &existing.RoomID, &existing.Kind, &existing.Body, &existing.Agent, &existing.CreatedAt, &usedAt, &existing.UsedCount)
	if err == nil {
		if usedAt.Valid {
			existing.UsedAt = usedAt.Time
		}
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return MemoryFact{}, err
	}
	f := MemoryFact{
		ID:        uuid.NewString(),
		RoomID:    roomID,
		Kind:      kind,
		Body:      body,
		Agent:     agent,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := d.db.Exec(`INSERT INTO memory_facts (id, room_id, kind, body, agent, created_at) VALUES(?,?,?,?,?,?)`,
		f.ID, f.RoomID, f.Kind, f.Body, f.Agent, f.CreatedAt); err != nil {
		return MemoryFact{}, err
	}
	if d.fts5 {
		_, _ = d.db.Exec(`INSERT INTO memory_fts(id, body) VALUES(?, ?)`, f.ID, f.Body)
	}
	return f, nil
}

// RecallMemoryFacts finds facts relevant to the query (FTS5 ranked when
// available, word-subset scan otherwise); with an empty query it returns the
// room's most recent facts. Lessons never outrank rank order but are labeled
// so prompt text can weight them.
func (d *DB) RecallMemoryFacts(roomID, query string, limit int) ([]MemoryFact, error) {
	if limit <= 0 {
		limit = 8
	}
	if limit > 30 {
		limit = 30
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if strings.TrimSpace(query) == "" {
		return d.scanMemoryFacts(`SELECT id, room_id, kind, body, agent, created_at, used_at, used_count
			FROM memory_facts WHERE room_id = ? ORDER BY created_at DESC LIMIT ?`, roomID, limit)
	}
	if d.fts5 {
		return d.scanMemoryFacts(`SELECT f.id, f.room_id, f.kind, f.body, f.agent, f.created_at, f.used_at, f.used_count
			FROM memory_fts t JOIN memory_facts f ON f.id = t.id
			WHERE memory_fts MATCH ? AND f.room_id = ?
			ORDER BY rank, f.used_count DESC, f.created_at DESC LIMIT ?`,
			ftsQuery(strings.Fields(query)), roomID, limit)
	}
	// Fallback: every quoted word must appear as a substring somewhere.
	words := strings.Fields(strings.ToLower(query))
	parts := make([]string, 0, len(words))
	args := make([]any, 0, len(words)+2)
	for _, w := range words {
		d := strings.ReplaceAll(w, `"`, `""`)
		parts = append(parts, `body LIKE ? ESCAPE '\'`)
		args = append(args, `%`+d+`%`)
	}
	args = append(args, roomID, limit)
	return d.scanMemoryFacts(fmt.Sprintf(`SELECT id, room_id, kind, body, agent, created_at, used_at, used_count
		FROM memory_facts WHERE (%s) AND room_id = ? ORDER BY used_count DESC, created_at DESC LIMIT ?`,
		strings.Join(parts, " AND ")), args...)
}

func (d *DB) scanMemoryFacts(q string, args ...any) ([]MemoryFact, error) {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryFact
	for rows.Next() {
		var f MemoryFact
		var usedAt sql.NullTime
		if err := rows.Scan(&f.ID, &f.RoomID, &f.Kind, &f.Body, &f.Agent, &f.CreatedAt, &usedAt, &f.UsedCount); err != nil {
			return nil, err
		}
		if usedAt.Valid {
			f.UsedAt = usedAt.Time
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// TouchMemoryFacts bumps usage so recall can prefer facts that keep proving
// useful across runs.
func (d *DB) TouchMemoryFacts(ids []string) {
	if len(ids) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().UTC()
	for _, id := range ids {
		_, _ = d.db.Exec(`UPDATE memory_facts SET used_count = used_count + 1, used_at = ? WHERE id = ?`, now, id)
	}
}

// ForgetMemoryFact invalidates one fact (agent-initiated), removing it from
// both the table and the FTS mirror. Returns whether a row was removed.
func (d *DB) ForgetMemoryFact(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, fmt.Errorf("memory id is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`DELETE FROM memory_facts WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	if d.fts5 {
		_, _ = d.db.Exec(`DELETE FROM memory_fts WHERE id = ?`, id)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return true, nil
	}
	return false, nil
}

// RoomMemoryFacts lists a room's facts for inspection/tests.
func (d *DB) RoomMemoryFacts(roomID string, limit int) ([]MemoryFact, error) {
	if limit <= 0 {
		limit = 50
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.scanMemoryFacts(`SELECT id, room_id, kind, body, agent, created_at, used_at, used_count
		FROM memory_facts WHERE room_id = ? ORDER BY created_at DESC LIMIT ?`, roomID, limit)
}
