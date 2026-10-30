package foreman

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection for autonomous room orchestration and state persistence.
type DB struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
	// fts5 reports whether the code search index (FTS5) is available.
	fts5 bool
}

// OpenDB opens or creates the SQLite database in the project's .foreman directory.
func OpenDB(projectPath string) (*DB, error) {
	dir := filepath.Join(projectPath, ".foreman")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create .foreman dir: %w", err)
	}
	dbPath := filepath.Join(dir, "foreman.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	d := &DB{db: db, path: dbPath}
	if err := d.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite schema: %w", err)
	}
	// Code search index is optional: if FTS5 is unavailable the app still runs
	// and search_index falls back to a plain scan.
	if err := d.initCodeIndex(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init code index: %w", err)
	}
	return d, nil
}

// Close closes the underlying SQLite database.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS rooms (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		goal TEXT NOT NULL DEFAULT '',
		triage_tier TEXT NOT NULL DEFAULT 'standard',
		status TEXT NOT NULL DEFAULT 'planning',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS room_teams (
		room_id TEXT NOT NULL,
		team_id TEXT NOT NULL,
		sort_order INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (room_id, team_id)
	);

	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		team_id TEXT NOT NULL DEFAULT '',
		agent_name TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'backlog',
		priority TEXT NOT NULL DEFAULT 'normal',
		effort TEXT NOT NULL DEFAULT 'medium',
		dependencies TEXT NOT NULL DEFAULT '[]',
		evidence TEXT NOT NULL DEFAULT '',
		retry_count INTEGER NOT NULL DEFAULT 0,
		hidden INTEGER NOT NULL DEFAULT 0,
		parent_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_tasks_room_status ON tasks(room_id, status);

	CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		role TEXT NOT NULL,
		agent_name TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL,
		activity_type TEXT NOT NULL DEFAULT '',
		metadata_json TEXT NOT NULL DEFAULT '{}',
		created_at DATETIME NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_messages_room_created ON messages(room_id, created_at);

	CREATE TABLE IF NOT EXISTS plans (
		room_id TEXT PRIMARY KEY,
		title TEXT NOT NULL DEFAULT 'Plan',
		tier TEXT NOT NULL DEFAULT 'standard',
		markdown TEXT NOT NULL DEFAULT '',
		nodes_json TEXT NOT NULL DEFAULT '[]',
		edges_json TEXT NOT NULL DEFAULT '[]',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS mailbox (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		from_agent TEXT NOT NULL,
		to_agent TEXT NOT NULL,
		content TEXT NOT NULL,
		is_read INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_mailbox_room_to ON mailbox(room_id, to_agent);

	CREATE TABLE IF NOT EXISTS memory (
		room_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		updated_by TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (room_id, key)
	);

	CREATE TABLE IF NOT EXISTS panels (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		title TEXT NOT NULL,
		icon TEXT NOT NULL DEFAULT 'layout',
		data_source TEXT NOT NULL,
		renderer TEXT NOT NULL DEFAULT 'markdown',
		status TEXT NOT NULL DEFAULT 'pending',
		manifest_json TEXT NOT NULL DEFAULT '{}',
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS approvals (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		type TEXT NOT NULL,
		description TEXT NOT NULL,
		severity TEXT NOT NULL DEFAULT 'info',
		payload_json TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'pending',
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS artifacts (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL,
		title TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'file',
		path TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS heartbeats (
		agent_name TEXT NOT NULL,
		room_id TEXT NOT NULL,
		last_heartbeat DATETIME NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		PRIMARY KEY (agent_name, room_id)
	);
	`
	_, err := d.db.Exec(schema)
	if err != nil {
		return err
	}
	// Forward migrations for databases created by earlier versions. ALTER TABLE
	// errors when the column already exists, which is expected and ignored.
	for _, stmt := range []string{
		`ALTER TABLE approvals ADD COLUMN severity TEXT NOT NULL DEFAULT 'info'`,
	} {
		_, _ = d.db.Exec(stmt)
	}
	return nil
}

// DeriveRoomGoal extracts a clean, action-oriented goal from the first user prompt if empty.
func DeriveRoomGoal(firstPrompt string) string {
	s := strings.TrimSpace(firstPrompt)
	// Strip leading mentions like @agent or #team
	for strings.HasPrefix(s, "@") || strings.HasPrefix(s, "#") {
		idx := strings.IndexByte(s, ' ')
		if idx < 0 {
			break
		}
		s = strings.TrimSpace(s[idx+1:])
	}
	if s == "" {
		return "Resolve requested issue"
	}
	lines := strings.Split(s, "\n")
	firstLine := strings.TrimSpace(lines[0])
	if len([]rune(firstLine)) > 100 {
		return string([]rune(firstLine)[:97]) + "…"
	}
	return firstLine
}

// PlanRecord models the structured plan diagram and markdown for a room.
type PlanRecord struct {
	RoomID    string    `json:"roomId"`
	Title     string    `json:"title"`
	Tier      string    `json:"tier"` // quick, standard, epic
	Markdown  string    `json:"markdown"`
	NodesJSON string    `json:"nodesJson"`
	EdgesJSON string    `json:"edgesJson"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PanelRecord models a dynamic agent-registered UI panel.
type PanelRecord struct {
	ID           string    `json:"id"`
	RoomID       string    `json:"roomId"`
	Title        string    `json:"title"`
	Icon         string    `json:"icon"`
	DataSource   string    `json:"dataSource"`
	Renderer     string    `json:"renderer"` // table, markdown, chart, kanban, diagram
	Status       string    `json:"status"`   // pending, approved, rejected
	ManifestJSON string    `json:"manifestJson"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ApprovalRecord models human review gates for merge, risky commands, or custom panels.
type ApprovalRecord struct {
	ID          string    `json:"id"`
	RoomID      string    `json:"roomId"`
	Type        string    `json:"type"` // merge, command, panel, question, escalation, review, request_spawn
	Description string    `json:"description"`
	Severity    string    `json:"severity,omitempty"` // info, warning, blocker
	PayloadJSON string    `json:"payloadJson"`
	Status      string    `json:"status"` // pending, approved, rejected
	CreatedAt   time.Time `json:"createdAt"`
}

// ArtifactRecord models output files, reports, diagrams, and diff summaries.
type ArtifactRecord struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"roomId"`
	Title     string    `json:"title"`
	Type      string    `json:"type"` // file, report, diagram, diff
	Path      string    `json:"path"`
	Summary   string    `json:"summary"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

// SavePlan saves or updates the room's plan and graph representation.
func (d *DB) SavePlan(p PlanRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	query := `
	INSERT INTO plans (room_id, title, tier, markdown, nodes_json, edges_json, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(room_id) DO UPDATE SET
		title = excluded.title,
		tier = excluded.tier,
		markdown = excluded.markdown,
		nodes_json = excluded.nodes_json,
		edges_json = excluded.edges_json,
		updated_at = excluded.updated_at
	`
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	_, err := d.db.Exec(query, p.RoomID, p.Title, p.Tier, p.Markdown, p.NodesJSON, p.EdgesJSON, p.CreatedAt, p.UpdatedAt)
	return err
}

// GetPlan retrieves the room's current plan.
func (d *DB) GetPlan(roomID string) (*PlanRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	row := d.db.QueryRow(`SELECT room_id, title, tier, markdown, nodes_json, edges_json, created_at, updated_at FROM plans WHERE room_id = ?`, roomID)
	var p PlanRecord
	err := row.Scan(&p.RoomID, &p.Title, &p.Tier, &p.Markdown, &p.NodesJSON, &p.EdgesJSON, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// AtomicClaimTask claims an available task for an agent within an isolated transaction.
func (d *DB) AtomicClaimTask(taskID, agentName string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var status string
	row := tx.QueryRow(`SELECT status FROM tasks WHERE id = ?`, taskID)
	if err := row.Scan(&status); err != nil {
		return false, err
	}
	if status != "backlog" && status != "todo" {
		return false, nil // already claimed
	}

	_, err = tx.Exec(`UPDATE tasks SET status = 'in_progress', agent_name = ?, updated_at = ? WHERE id = ?`,
		agentName, time.Now().UTC(), taskID)
	if err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RecordActivity records a compact activity item into room messages.
func (d *DB) RecordActivity(roomID, agentName, activityType, content string, metadata map[string]any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	metaBytes, _ := json.Marshal(metadata)
	if len(metaBytes) == 0 {
		metaBytes = []byte("{}")
	}
	_, err := d.db.Exec(`
		INSERT INTO messages (id, room_id, role, agent_name, content, activity_type, metadata_json, created_at)
		VALUES (?, ?, 'activity', ?, ?, ?, ?, ?)
	`, uuid.NewString(), roomID, agentName, content, activityType, string(metaBytes), time.Now().UTC())
	return err
}

// RecordHeartbeat logs a periodic heartbeat for an active agent.
func (d *DB) RecordHeartbeat(agentName, roomID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
		INSERT INTO heartbeats (agent_name, room_id, last_heartbeat, status)
		VALUES (?, ?, ?, 'active')
		ON CONFLICT(agent_name, room_id) DO UPDATE SET
			last_heartbeat = excluded.last_heartbeat,
			status = 'active'
	`, agentName, roomID, time.Now().UTC())
	return err
}

// CheckStalledAgents returns names of agents that have not reported a heartbeat within threshold.
func (d *DB) CheckStalledAgents(roomID string, threshold time.Duration) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cutoff := time.Now().UTC().Add(-threshold)
	rows, err := d.db.Query(`SELECT agent_name FROM heartbeats WHERE room_id = ? AND status = 'active' AND last_heartbeat < ?`, roomID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stalled []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			stalled = append(stalled, name)
		}
	}
	return stalled, nil
}

// SetMemory writes a key-value record to shared agent memory.
func (d *DB) SetMemory(roomID, key, value, updatedBy string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
		INSERT INTO memory (room_id, key, value, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(room_id, key) DO UPDATE SET
			value = excluded.value,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at
	`, roomID, key, value, updatedBy, time.Now().UTC())
	return err
}

// GetMemory retrieves a key from shared agent memory.
func (d *DB) GetMemory(roomID, key string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var val string
	err := d.db.QueryRow(`SELECT value FROM memory WHERE room_id = ? AND key = ?`, roomID, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SaveArtifact saves a generated artifact (report, diagram, diff, etc).
func (d *DB) SaveArtifact(a ArtifactRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	_, err := d.db.Exec(`
		INSERT INTO artifacts (id, room_id, title, type, path, summary, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, a.ID, a.RoomID, a.Title, a.Type, a.Path, a.Summary, a.Content, a.CreatedAt)
	return err
}

// ListPlans returns all saved plans.
func (d *DB) ListPlans() ([]PlanRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT room_id, title, tier, markdown, nodes_json, edges_json, created_at, updated_at FROM plans`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []PlanRecord
	for rows.Next() {
		var p PlanRecord
		if err := rows.Scan(&p.RoomID, &p.Title, &p.Tier, &p.Markdown, &p.NodesJSON, &p.EdgesJSON, &p.CreatedAt, &p.UpdatedAt); err == nil {
			list = append(list, p)
		}
	}
	return list, nil
}

// ListArtifacts retrieves all artifacts for a room (or all rooms if roomID is empty).
func (d *DB) ListArtifacts(roomID string) ([]ArtifactRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var rows *sql.Rows
	var err error
	if roomID == "" {
		rows, err = d.db.Query(`SELECT id, room_id, title, type, path, summary, content, created_at FROM artifacts ORDER BY created_at DESC`)
	} else {
		rows, err = d.db.Query(`SELECT id, room_id, title, type, path, summary, content, created_at FROM artifacts WHERE room_id = ? ORDER BY created_at DESC`, roomID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ArtifactRecord
	for rows.Next() {
		var a ArtifactRecord
		if err := rows.Scan(&a.ID, &a.RoomID, &a.Title, &a.Type, &a.Path, &a.Summary, &a.Content, &a.CreatedAt); err == nil {
			list = append(list, a)
		}
	}
	return list, nil
}

// CreateApproval creates a pending approval item for human gate.
func (d *DB) CreateApproval(app ApprovalRecord) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if app.ID == "" {
		app.ID = uuid.NewString()
	}
	if app.CreatedAt.IsZero() {
		app.CreatedAt = time.Now().UTC()
	}
	app.Severity = normalizeSeverity(app.Severity)
	_, err := d.db.Exec(`
		INSERT INTO approvals (id, room_id, type, description, severity, payload_json, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)
	`, app.ID, app.RoomID, app.Type, app.Description, app.Severity, app.PayloadJSON, app.CreatedAt)
	return app.ID, err
}

// normalizeSeverity coerces a severity string to one of info|warning|blocker,
// defaulting to info for empty or unknown values.
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "blocker", "critical", "high", "red":
		return "blocker"
	case "warning", "warn", "medium", "yellow":
		return "warning"
	default:
		return "info"
	}
}

// SeverityRank orders severities for sorting (blocker first).
func SeverityRank(s string) int {
	switch normalizeSeverity(s) {
	case "blocker":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

// ListApprovals returns pending approvals for the room (or all rooms if roomID is empty).
func (d *DB) ListApprovals(roomID string) ([]ApprovalRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var rows *sql.Rows
	var err error
	if roomID == "" {
		rows, err = d.db.Query(`SELECT id, room_id, type, description, severity, payload_json, status, created_at FROM approvals ORDER BY created_at DESC`)
	} else {
		rows, err = d.db.Query(`SELECT id, room_id, type, description, severity, payload_json, status, created_at FROM approvals WHERE room_id = ? ORDER BY created_at DESC`, roomID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ApprovalRecord
	for rows.Next() {
		var a ApprovalRecord
		if err := rows.Scan(&a.ID, &a.RoomID, &a.Type, &a.Description, &a.Severity, &a.PayloadJSON, &a.Status, &a.CreatedAt); err == nil {
			list = append(list, a)
		}
	}
	// Surface the most urgent pending items first (blocker → warning → info).
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := SeverityRank(list[i].Severity), SeverityRank(list[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return list, nil
}

// ResolveApproval marks an approval as approved or rejected.
func (d *DB) ResolveApproval(id, status string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE approvals SET status = ? WHERE id = ?`, status, id)
	return err
}

// RegisterPanel registers a dynamic panel manifest.
func (d *DB) RegisterPanel(p PanelRecord) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	// Whitelist renderer check
	validRenderers := map[string]bool{
		"table": true, "markdown": true, "chart": true, "kanban": true, "diagram": true,
	}
	if !validRenderers[p.Renderer] {
		p.Renderer = "markdown"
	}
	_, err := d.db.Exec(`
		INSERT INTO panels (id, room_id, title, icon, data_source, renderer, status, manifest_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)
	`, p.ID, p.RoomID, p.Title, p.Icon, p.DataSource, p.Renderer, p.ManifestJSON, p.CreatedAt)
	return p.ID, err
}

// ListPanels returns approved panels for a room.
func (d *DB) ListPanels(roomID string, includePending bool) ([]PanelRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	query := `SELECT id, room_id, title, icon, data_source, renderer, status, manifest_json, created_at FROM panels`
	var args []any
	if roomID != "" {
		query += ` WHERE room_id = ?`
		args = append(args, roomID)
		if !includePending {
			query += ` AND status = 'approved'`
		}
	} else if !includePending {
		query += ` WHERE status = 'approved'`
	}
	query += ` ORDER BY created_at ASC`
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []PanelRecord
	for rows.Next() {
		var p PanelRecord
		if err := rows.Scan(&p.ID, &p.RoomID, &p.Title, &p.Icon, &p.DataSource, &p.Renderer, &p.Status, &p.ManifestJSON, &p.CreatedAt); err == nil {
			list = append(list, p)
		}
	}
	return list, nil
}

// RecoverInterruptedTasks requeues tasks that were running during a crash.
// If retry_count >= 3, it marks them as 'blocked' and returns them so the supervisor can be notified.
func (d *DB) RecoverInterruptedTasks() (requeued []string, blocked []string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`SELECT id, retry_count FROM tasks WHERE status = 'in_progress'`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	type item struct {
		id    string
		retry int
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.retry); err == nil {
			items = append(items, it)
		}
	}

	now := time.Now().UTC()
	for _, it := range items {
		if it.retry >= 3 {
			_, _ = d.db.Exec(`UPDATE tasks SET status = 'blocked', updated_at = ? WHERE id = ?`, now, it.id)
			blocked = append(blocked, it.id)
		} else {
			_, _ = d.db.Exec(`UPDATE tasks SET status = 'backlog', retry_count = retry_count + 1, updated_at = ? WHERE id = ?`, now, it.id)
			requeued = append(requeued, it.id)
		}
	}
	return requeued, blocked, nil
}

// DeleteTask removes a task card.
func (d *DB) DeleteTask(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}

// DeletePlan removes a room's plan.
func (d *DB) DeletePlan(roomID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM plans WHERE room_id = ?`, roomID)
	return err
}

// DeleteArtifact removes an artifact.
func (d *DB) DeleteArtifact(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM artifacts WHERE id = ?`, id)
	return err
}
