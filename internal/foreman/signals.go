package foreman

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Internal Signals: a per-project usage graph, persisted in .foreman/foreman.db,
// that learns from what agents actually do and makes every cold start cheaper.
// It is NOT a neural network — it is a deterministic co-usage/dependency graph:
//
//	edges kind "co_touch": two files read/edited within the same run
//	edges kind "edit":     files actually modified (self-edge, weight 3)
//
// Structure edges ("import") are deliberately deferred: robust multi-language
// import parsing is a tree-sitter-layer problem (opt-in build), while co-touch
// edges are emitted by instrumentation in the tool layer — the model never has
// to cooperate for the graph to learn (policy in code, not prompt).
//
// Files that drift out of use decay (last_seen), so the graph stays current
// rather than turning into sediment; this follows the temporal-graph practice
// (Zep/Graphiti) of pairing growth with decay.

const (
	// signalWeightCoTouch is added to a pair each time files are touched in one run.
	signalWeightCoTouch = 1.0
	// signalWeightEdit marks an actual modification.
	signalWeightEdit = 3.0
	// signalMaxWeight caps a single edge so one repetitive run cannot drown the graph.
	signalMaxWeight = 50.0
	// signalMaxRunFiles caps the per-run file set; pairwise edges stay bounded.
	signalMaxRunFiles = 12
	// signalPairCap bounds emitted pairs per run (12 files => 66 pairs max).
	signalPairCap = 66
)

// signalSymmetricKinds canonicalize their (src, dst) pair so touching A,B and
// B,A writes the same edge.
var signalSymmetricKinds = map[string]bool{"co_touch": true, "edit": true}

// initSignals creates the edges + daily activity tables. Deterministic schema,
// no FTS dependency — signals are aggregation, not text search.
func (d *DB) initSignals() error {
	if _, err := d.db.Exec(`
	CREATE TABLE IF NOT EXISTS signals_edges (
		kind TEXT NOT NULL,
		src TEXT NOT NULL,
		dst TEXT NOT NULL,
		weight REAL NOT NULL DEFAULT 0,
		hits INTEGER NOT NULL DEFAULT 0,
		last_seen DATETIME NOT NULL,
		PRIMARY KEY (kind, src, dst)
	)`); err != nil {
		return err
	}
	if _, err := d.db.Exec(`
	CREATE TABLE IF NOT EXISTS signals_activity (
		day TEXT PRIMARY KEY,
		touches INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	return nil
}

func normalizeSignalPair(kind, src, dst string) (string, string) {
	if signalSymmetricKinds[strings.TrimSpace(kind)] && dst < src {
		src, dst = dst, src
	}
	return src, dst
}

// SignalTouch upserts one usage edge: hits++, weight accrued (capped),
// last_seen advanced, daily activity bumped.
func (d *DB) SignalTouch(kind, src, dst string, weight float64) error {
	kind = strings.TrimSpace(kind)
	src, dst = normalizeSignalPair(kind, strings.TrimSpace(src), strings.TrimSpace(dst))
	if kind == "" || src == "" || dst == "" || (src == dst && kind != "edit") {
		return fmt.Errorf("invalid signal edge (kind=%q src=%q dst=%q)", kind, src, dst)
	}
	if weight <= 0 {
		weight = signalWeightCoTouch
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().UTC()
	if _, err := d.db.Exec(`INSERT INTO signals_edges (kind, src, dst, weight, hits, last_seen)
		VALUES(?, ?, ?, ?, 1, ?)
		ON CONFLICT(kind, src, dst) DO UPDATE SET
			weight = MIN(? + weight * 0.5, ?),
			hits = hits + 1,
			last_seen = ?`,
		kind, src, dst, weight, now, weight, signalMaxWeight, now); err != nil {
		return err
	}
	day := now.Format("2006-01-02")
	_, err := d.db.Exec(`INSERT INTO signals_activity (day, touches) VALUES(?, 1)
		ON CONFLICT(day) DO UPDATE SET touches = touches + 1`, day)
	return err
}

// SignalEdge is one row of the learned graph, for tool results and the API.
// Src/Dst are the raw table endpoints; Path/OtherPath are the presentable,
// resolved view (Path is the subject of the query, OtherPath its neighbor).
type SignalEdge struct {
	Kind      string    `json:"kind"`
	Path      string    `json:"path"`
	OtherPath string    `json:"other_path,omitempty"`
	Weight    float64   `json:"weight"`
	Hits      int       `json:"hits"`
	LastSeen  time.Time `json:"last_seen"`
	Src       string    `json:"-"`
	Dst       string    `json:"-"`
}

// SignalsTopPaths ranks files by summed edge weight across the requested
// kinds. This is the "hot files" backbone consumed by the run prompt and the
// dashboard Signals page. With no kinds filter, every edge kind counts.
func (d *DB) SignalsTopPaths(n int, kinds ...string) ([]SignalEdge, error) {
	if n <= 0 {
		n = 6
	}
	if n > 50 {
		n = 50
	}
	filter := ""
	srcFilter := ""
	// The filter applies to both sides of the UNION ALL. The second side must
	// additionally exclude self-edges (dst == src) so an edit self-edge is not
	// double-counted as both src and dst.
	args := make([]any, 0, 2*len(kinds)+1)
	if len(kinds) > 0 {
		ph := make([]string, 0, len(kinds))
		for range kinds {
			ph = append(ph, "?")
		}
		placeholders := strings.Join(ph, ",")
		filter = " WHERE kind IN (" + placeholders + ")"
		srcFilter = filter + " AND dst != src"
		for _, k := range kinds {
			args = append(args, k)
		}
		for _, k := range kinds {
			args = append(args, k)
		}
	} else {
		srcFilter = " WHERE dst != src"
	}
	args = append(args, n)
	query := `SELECT path, SUM(weight) AS weight, SUM(hits) AS hits FROM (
		SELECT src AS path, weight, hits FROM signals_edges` + filter + `
		UNION ALL
		SELECT dst AS path, weight, hits FROM signals_edges` + srcFilter + `
	) GROUP BY path HAVING SUM(hits) > 0 ORDER BY weight DESC, hits DESC, path LIMIT ?`
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SignalEdge{}
	for rows.Next() {
		var e SignalEdge
		if err := rows.Scan(&e.Path, &e.Weight, &e.Hits); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SignalsNeighbors returns everything connected to one path from both sides,
// the single call that replaces a handful of grep/glob rounds.
func (d *DB) SignalsNeighbors(path string, n int) ([]SignalEdge, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	if n <= 0 {
		n = 8
	}
	if n > 30 {
		n = 30
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`
		SELECT kind, src, dst, weight, hits, last_seen FROM signals_edges
		WHERE (src = ? OR dst = ?) AND weight >= 0.25
		ORDER BY weight DESC, hits DESC LIMIT ?`, path, path, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignalEdge
	for rows.Next() {
		var (
			e        SignalEdge
			lastSeen time.Time
		)
		if err := rows.Scan(&e.Kind, &e.Src, &e.Dst, &e.Weight, &e.Hits, &lastSeen); err != nil {
			return nil, err
		}
		e.LastSeen = lastSeen
		if e.Src == path {
			e.Path, e.OtherPath = e.Src, e.Dst
		} else {
			e.Path, e.OtherPath = e.Dst, e.Src
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SignalsReport is the dashboard payload: totals, top paths, and growth.
type SignalsReport struct {
	Files    int              `json:"files"`
	Edges    int              `json:"edges"`
	Touches  int              `json:"touches"`
	TopPaths []SignalEdge     `json:"top_paths"`
	Activity []SignalActivity `json:"activity"`
}

// SignalActivity is one day's usage total for the growth chart.
type SignalActivity struct {
	Day     string `json:"day"`
	Touches int    `json:"touches"`
}

// SignalsReport assembles the API/dashboard snapshot under one consistent
// read of the totals, then delegated reads for ranking and the daily chart.
func (d *DB) SignalsReport(topN int) (SignalsReport, error) {
	r := SignalsReport{}
	d.mu.RLock()
	err := d.db.QueryRow(`SELECT
			(SELECT COUNT(*) FROM signals_edges),
			(SELECT COUNT(*) FROM (SELECT src AS p FROM signals_edges UNION SELECT dst FROM signals_edges)),
			(SELECT COALESCE(SUM(touches), 0) FROM signals_activity)`).Scan(&r.Edges, &r.Files, &r.Touches)
	d.mu.RUnlock()
	if err != nil && err != sql.ErrNoRows {
		return r, err
	}
	if r.TopPaths, err = d.SignalsTopPaths(topN); err != nil {
		return r, err
	}
	d.mu.RLock()
	rows, rerr := d.db.Query(`SELECT day, touches FROM signals_activity ORDER BY day DESC LIMIT 30`)
	if rerr == nil {
		for rows.Next() {
			var a SignalActivity
			if err2 := rows.Scan(&a.Day, &a.Touches); err2 == nil {
				r.Activity = append(r.Activity, a)
			}
		}
		rerr = rows.Err()
		rows.Close()
	}
	d.mu.RUnlock()
	if rerr != nil && rerr != sql.ErrNoRows {
		return r, rerr
	}
	return r, nil
}

// SignalsDecay halves weights for edges not touched recently and removes
// stale, low-weight edges so the graph tracks what matters NOW. Called
// opportunistically (index refresh), so there is no background job to babysit.
func (d *DB) SignalsDecay() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	old := time.Now().UTC().Add(-14 * 24 * time.Hour)
	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if _, err := d.db.Exec(`UPDATE signals_edges SET weight = weight * 0.5 WHERE last_seen < ?`, old); err != nil {
		return err
	}
	_, err := d.db.Exec(`DELETE FROM signals_edges WHERE weight < 0.25 AND last_seen < ?`, stale)
	return err
}

// signalTracker learns the co-usage of ONE run: which files the agent actually
// opened (view) and which it actually changed (edit). finalize emits pairwise
// co_touch edges plus per-file edit self-edges. Instrumented at the tool
// layer, so agents power the graph without ever being asked to.
type signalTracker struct {
	mu     sync.Mutex
	room   string
	paths  []string // insertion ordered
	edited map[string]bool
}

func newSignalTracker(room string) *signalTracker {
	return &signalTracker{room: room, edited: map[string]bool{}}
}

// touch records one file interaction. edited=true marks a real modification.
func (t *signalTracker) touch(rel string, edited bool) {
	if t == nil || strings.TrimSpace(rel) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if edited {
		t.edited[rel] = true
	}
	for _, p := range t.paths {
		if p == rel {
			return
		}
	}
	if len(t.paths) < signalMaxRunFiles {
		t.paths = append(t.paths, rel)
	}
}

// finalize flushes the run's learned edges into the store (best effort).
func (t *signalTracker) finalize(store *Store) {
	if t == nil || store == nil || store.DB() == nil {
		return
	}
	t.mu.Lock()
	paths := append([]string(nil), t.paths...)
	edited := make([]string, 0, len(t.edited))
	for p, e := range t.edited {
		if e {
			edited = append(edited, p)
		}
	}
	t.mu.Unlock()
	db := store.DB()
	pairs := 0
	for i := 0; i < len(paths) && pairs < signalPairCap; i++ {
		for j := i + 1; j < len(paths) && pairs < signalPairCap; j++ {
			if err := db.SignalTouch("co_touch", paths[i], paths[j], signalWeightCoTouch); err == nil {
				pairs++
			}
		}
	}
	for _, p := range edited {
		_ = db.SignalTouch("edit", p, p, signalWeightEdit)
	}
}
