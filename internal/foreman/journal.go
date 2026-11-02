package foreman

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// JournalEntry is one structured fact about what changed in the workspace and
// why. Unlike the room-scoped memory fact bank, the journal is workspace-scoped
// and deliberately terse — file/function/action/dependency — so a new session
// in any room can pick up where earlier sessions left off without re-exploring.
type JournalEntry struct {
	ID         string    `json:"id"`
	RoomID     string    `json:"room_id,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	File       string    `json:"file,omitempty"`
	Func       string    `json:"func,omitempty"`
	Action     string    `json:"action"`
	Dependency string    `json:"dependency,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// initJournal creates the workspace session journal. Plain SQLite — no FTS5
// requirement — so journaling works even when full-text search is unavailable.
func (d *DB) initJournal() error {
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS journal (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL DEFAULT '',
		agent TEXT NOT NULL DEFAULT '',
		file TEXT NOT NULL DEFAULT '',
		func TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL DEFAULT '',
		dependency TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	_, err := d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_journal_created ON journal(created_at)`)
	return err
}

// SaveJournal records one structured fact. An identical fact (same
// file+func+action) recorded within the last 24h collapses onto the existing
// row instead of churning the journal — repeated fix attempts read as one
// ongoing change, not a pile of noise.
func (d *DB) SaveJournal(roomID, agent, file, funcName, action, dependency string) (JournalEntry, error) {
	action = strings.TrimSpace(action)
	if action == "" {
		return JournalEntry{}, fmt.Errorf("journal action is required")
	}
	now := time.Now().UTC().Unix()
	entry := JournalEntry{
		ID: uuid.NewString(), RoomID: roomID, Agent: agent,
		File: file, Func: funcName, Action: action, Dependency: dependency,
		CreatedAt: time.Unix(now, 0).UTC(),
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var existing string
	row := d.db.QueryRow(`SELECT id FROM journal WHERE room_id = ? AND file = ? AND func = ? AND action = ? AND created_at > ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, roomID, file, funcName, action, now-24*3600)
	if row.Scan(&existing) == nil {
		if _, err := d.db.Exec(`UPDATE journal SET created_at = ? WHERE id = ?`, now, existing); err != nil {
			return JournalEntry{}, err
		}
		entry.ID = existing
		return entry, nil
	}
	if _, err := d.db.Exec(`INSERT INTO journal(id, room_id, agent, file, func, action, dependency, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, entry.ID, roomID, agent, file, funcName, action, dependency, now); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

// RecentJournal returns the newest journal entries. When roomID is non-empty it
// is filtered to that room; an empty roomID returns the workspace-wide journal
// (the shared cross-room context — useful even in a fresh room, since another
// room may have just touched the same files).
func (d *DB) RecentJournal(roomID string, limit int) ([]JournalEntry, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	query := `SELECT id, room_id, agent, file, func, action, dependency, created_at
		FROM journal `
	args := []any{}
	if roomID != "" {
		query += `WHERE room_id = ? `
		args = append(args, roomID)
	}
	query += `ORDER BY created_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var created int64
		if err := rows.Scan(&e.ID, &e.RoomID, &e.Agent, &e.File, &e.Func, &e.Action, &e.Dependency, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// JournalForRoom returns entries scoped to a single room (most recent first),
// with a higher limit than the prompt injection — the UI shows the full history.
func (d *DB) JournalForRoom(roomID string, limit int) ([]JournalEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT id, room_id, agent, file, func, action, dependency, created_at
		FROM journal WHERE room_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, roomID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var created int64
		if err := rows.Scan(&e.ID, &e.RoomID, &e.Agent, &e.File, &e.Func, &e.Action, &e.Dependency, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// JournalTTLPrune compacts entries older than maxAge into a single per-file
// summary row per room, so the active table stops growing without bound. The
// full detail is not deleted (it stays in the historical rows that are older
// than maxAge but still queried by JournalForRoom); this only collapses the
// high-churn tail so session injection and the UI stay cheap. Returns the
// number of rows removed.
func (d *DB) JournalTTLPrune(maxAge time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-maxAge).Unix()
	d.mu.Lock()
	defer d.mu.Unlock()
	// Collapse: keep the newest entry per (room, file) among the old ones, drop
	// the rest. A file touched once long ago becomes a single retained row.
	res, err := d.db.Exec(`DELETE FROM journal
		WHERE created_at < ?
		  AND id NOT IN (
		    SELECT id FROM journal j2
		    WHERE j2.created_at < ?
		      AND j2.room_id = journal.room_id
		      AND j2.file = journal.file
		    ORDER BY j2.created_at DESC, j2.rowid DESC
		    LIMIT 1
		  )`, cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// fileSum aggregates one file's journal entries during consolidation.
type fileSum struct {
	count int
	last  journalRec
}

// journalRec is the newest entry seen for a file during consolidation.
type journalRec struct {
	file, funcName, action string
	created                int64
}

// JournalConsolidated renders a per-file digest of recent activity: each file
// becomes one line with its change count and the most recent action. This is
// the deterministic replacement for "read every journal ever" — a session start
// injects a handful of these lines, and the full history only grows in the DB.
func (d *DB) JournalConsolidated(maxEntries, maxChars int) []string {
	if maxEntries <= 0 || maxEntries > 500 {
		maxEntries = 200
	}
	if maxChars <= 0 {
		maxChars = 900
	}
	d.mu.RLock()
	rows, err := d.db.Query(`SELECT file, func, action, created_at FROM journal ORDER BY created_at DESC, rowid DESC LIMIT ?`, maxEntries)
	if err != nil {
		d.mu.RUnlock()
		return nil
	}
	var recs []journalRec
	for rows.Next() {
		var r journalRec
		if rows.Scan(&r.file, &r.funcName, &r.action, &r.created) == nil {
			recs = append(recs, r)
		}
	}
	d.mu.RUnlock()
	rows.Close()
	if len(recs) == 0 {
		return nil
	}

	// Newest entry per file wins the "last action" slot; count everything.
	sums := map[string]*fileSum{}
	var order []string
	for _, r := range recs {
		s, ok := sums[r.file]
		if !ok {
			s = &fileSum{}
			sums[r.file] = s
			order = append(order, r.file)
		}
		s.count++
		if s.last.created == 0 || r.created > s.last.created {
			s.last = r
		}
	}
	sortStableByLastActivity(sums, order)

	var lines []string
	used := 0
	for _, file := range order {
		s := sums[file]
		var b strings.Builder
		fmt.Fprintf(&b, "- %s: %d change(s)", file, s.count)
		if s.last.action != "" {
			b.WriteString(" — last: " + s.last.action)
			if s.last.funcName != "" {
				b.WriteString(" (" + s.last.funcName + ")")
			}
		}
		line := b.String()
		if used+len(line) > maxChars {
			break
		}
		lines = append(lines, line)
		used += len(line)
	}
	return lines
}

// sortStableByLastActivity orders files by their newest entry, newest first.
func sortStableByLastActivity(sums map[string]*fileSum, order []string) {
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && sums[order[j]].last.created > sums[order[j-1]].last.created; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
}

// Store wrappers -------------------------------------------------------------

func (s *Store) SaveJournal(roomID, agent, file, funcName, action, dependency string) (JournalEntry, error) {
	if s.db == nil {
		return JournalEntry{}, fmt.Errorf("store unavailable")
	}
	return s.db.SaveJournal(roomID, agent, file, funcName, action, dependency)
}

func (s *Store) RecentJournal(roomID string, limit int) ([]JournalEntry, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.RecentJournal(roomID, limit)
}

func (s *Store) JournalForRoom(roomID string, limit int) ([]JournalEntry, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.JournalForRoom(roomID, limit)
}

func (s *Store) JournalTTLPrune(maxAge time.Duration) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	return s.db.JournalTTLPrune(maxAge)
}

func (s *Store) JournalConsolidated(maxEntries, maxChars int) []string {
	if s.db == nil {
		return nil
	}
	return s.db.JournalConsolidated(maxEntries, maxChars)
}
