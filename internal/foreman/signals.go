package foreman

import (
	"database/sql"
	"fmt"
	"log/slog"
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
	// signalWeightSearch marks a file the agent surfaced via search/grep. It is
	// deliberately weak: being matched by a pattern is not the same as being
	// read, let alone edited.
	signalWeightSearch = 0.5
	// signalWeightImport marks a structural dependency discovered from source
	// imports during an index refresh. Directional: src imports dst.
	signalWeightImport = 2.0
	// signalRepeatViewBonus accrues when the agent returns to a file within the
	// same run — repeated reads mean the file is being studied, not passed by.
	signalRepeatViewBonus = 0.5
	// signalMaxWeight caps a single edge so one repetitive run cannot drown the graph.
	signalMaxWeight = 50.0
	// signalMaxRunFiles caps the per-run file set; pairwise edges stay bounded.
	signalMaxRunFiles = 12
	// signalPairCap bounds emitted pairs per run (12 files => 66 pairs max).
	signalPairCap = 66
	// signalMaxMentions caps search-surfaced files per run.
	signalMaxMentions = 50
	// signalsMaxImportFiles caps how many source files an index refresh will
	// scan for import statements.
	signalsMaxImportFiles = 2000
	// signalMaxImportEdges bounds import edges per refresh so a huge repo
	// cannot balloon the table in one pass.
	signalMaxImportEdges = 5000
	// signalDecayInterval is the minimum gap between decay passes triggered
	// from run ends (search_index still decays immediately).
	signalDecayInterval = 12 * time.Hour
	// Per-run scores. A file opened once scores signalScoreView; repeats and
	// edits raise it. A stdin boundary number so pair strength stays explainable.
	signalScoreView = 1.0
	signalScoreEdit = signalWeightEdit
)

// signalSymmetricKinds canonicalize their (src, dst) pair so touching A,B and
// B,A writes the same edge.
var signalSymmetricKinds = map[string]bool{"co_touch": true, "edit": true}

// signalSelfEdgeKinds are edge kinds allowed with src == dst ("A affects A"):
// edits record modified files, searches record files that surfaced in results.
var signalSelfEdgeKinds = map[string]bool{"edit": true, "search": true}

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
	if kind == "" || src == "" || dst == "" || (src == dst && !signalSelfEdgeKinds[kind]) {
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

// SignalEdgeDelta is one upsert in a batch flush.
type SignalEdgeDelta struct {
	Kind   string
	Src    string
	Dst    string
	Weight float64
}

// SignalTouchBatch upserts many edges in ONE transaction: either the whole run
// flush lands or none of it does. That turns the tracker's best-effort writes
// into at-least-once semantics for paired edges. The daily activity counter is
// bumped once for the batch.
func (d *DB) SignalTouchBatch(deltas []SignalEdgeDelta) error {
	normalized := make([]SignalEdgeDelta, 0, len(deltas))
	for _, delta := range deltas {
		kind := strings.TrimSpace(delta.Kind)
		src, dst := normalizeSignalPair(kind, strings.TrimSpace(delta.Src), strings.TrimSpace(delta.Dst))
		if kind == "" || src == "" || dst == "" || (src == dst && !signalSelfEdgeKinds[kind]) {
			return fmt.Errorf("invalid signal edge (kind=%q src=%q dst=%q)", kind, src, dst)
		}
		w := delta.Weight
		if w <= 0 {
			w = signalWeightCoTouch
		}
		normalized = append(normalized, SignalEdgeDelta{Kind: kind, Src: src, Dst: dst, Weight: w})
	}
	if len(normalized) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	stmt, err := tx.Prepare(`INSERT INTO signals_edges (kind, src, dst, weight, hits, last_seen)
		VALUES(?, ?, ?, ?, 1, ?)
		ON CONFLICT(kind, src, dst) DO UPDATE SET
			weight = MIN(? + weight * 0.5, ?),
			hits = hits + 1,
			last_seen = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, delta := range normalized {
		if _, err := stmt.Exec(delta.Kind, delta.Src, delta.Dst, delta.Weight, now,
			delta.Weight, signalMaxWeight, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO signals_activity (day, touches) VALUES(?, 1)
		ON CONFLICT(day) DO UPDATE SET touches = touches + 1`, now.Format("2006-01-02")); err != nil {
		return err
	}
	return tx.Commit()
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
	// Kinds counts edges per kind (co_touch, edit, search, import) so the
	// dashboard can show the structure/usage split without extra queries.
	Kinds map[string]int `json:"kinds,omitempty"`
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
	if r.Kinds, err = d.SignalKindCounts(); err != nil {
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

// SignalKindCounts returns how many edges exist per kind, for the dashboard's
// structure/usage split.
func (d *DB) SignalKindCounts() (map[string]int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(`SELECT kind, COUNT(*) FROM signals_edges GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// SignalsDecay halves weights for edges not touched recently and removes
// stale, low-weight edges so the graph tracks what matters NOW. Called
// opportunistically (index refresh, run ends), so there is no background job
// to babysit.
func (d *DB) SignalsDecay() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := decayLocked(d); err != nil {
		return err
	}
	d.decayLast = time.Now().UTC()
	return nil
}

// SignalsDecayIfStale runs decay only when at least minInterval has passed
// since the last pass. Run ends call this so the graph also ages in projects
// where agents never invoke search_index (the only caller of the eager decay).
// Returns true when a pass actually ran.
func (d *DB) SignalsDecayIfStale(minInterval time.Duration) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.decayLast.IsZero() && time.Since(d.decayLast) < minInterval {
		return false, nil
	}
	if err := decayLocked(d); err != nil {
		return false, err
	}
	d.decayLast = time.Now().UTC()
	return true, nil
}

// decayLocked does the expiry work; callers must hold d.mu.
func decayLocked(d *DB) error {
	old := time.Now().UTC().Add(-14 * 24 * time.Hour)
	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if _, err := d.db.Exec(`UPDATE signals_edges SET weight = weight * 0.5 WHERE last_seen < ?`, old); err != nil {
		return err
	}
	_, err := d.db.Exec(`DELETE FROM signals_edges WHERE weight < 0.25 AND last_seen < ?`, stale)
	return err
}

// signalTracker learns the co-usage of ONE run. Three strengths feed it, all
// instrumented at the tool layer so agents power the graph without ever being
// asked to:
//
//   - view (view_file): weight 1.0; repeated reads within the run accrue
//     +0.5 each — a file the agent returns to is being studied.
//   - edit (write tools): weight 3.0, overriding any view score.
//   - search mentions (grep/search_index result files): weak self-edge, 0.5.
//
// finalize emits pairwise co_touch edges whose per-run weight is the weaker
// side's score (a hint-glanced file cannot drag a whole pair to "hot"), the
// edit/search self-edges, and flushes everything in a single transaction.
type signalTracker struct {
	mu        sync.Mutex
	room      string
	paths     []string // insertion ordered
	scores    map[string]float64
	edited    map[string]bool
	mentioned []string
}

func newSignalTracker(room string) *signalTracker {
	return &signalTracker{room: room, scores: map[string]float64{}, edited: map[string]bool{}}
}

// touch records one file interaction. edited=true marks a real modification
// and dominates the file's score for the run.
func (t *signalTracker) touch(rel string, edited bool) {
	if t == nil || strings.TrimSpace(rel) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if edited {
		t.edited[rel] = true
	}
	score, seen := t.scores[rel]
	if !seen {
		// Files beyond the per-run path cap still contribute self-edges
		// (edit/search) but never join pairwise co_touch, so the pair count
		// stays bounded.
		if len(t.paths) < signalMaxRunFiles {
			t.paths = append(t.paths, rel)
		}
		score = signalScoreView
	} else {
		score += signalRepeatViewBonus
	}
	if edited {
		score = signalScoreEdit
	} else if score > signalScoreEdit {
		score = signalScoreEdit
	}
	t.scores[rel] = score
}

// mention records files surfaced by search/grep results. Mentions become weak
// self-edges only — they must not create co_touch pairs, or a lucky regex poll
// would fabricate relationships.
func (t *signalTracker) mention(rels ...string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := map[string]bool{}
	for _, m := range t.mentioned {
		seen[m] = true
	}
	for _, rel := range rels {
		rel = strings.TrimSpace(rel)
		if rel == "" || seen[rel] {
			continue
		}
		if len(t.mentioned) >= signalMaxMentions {
			return
		}
		seen[rel] = true
		t.mentioned = append(t.mentioned, rel)
	}
}

// mentionMatches extracts file paths from tool-result match maps and mentions
// them. Best effort: maps without a "file" key are skipped.
func (t *signalTracker) mentionMatches(matches []map[string]any) {
	if t == nil {
		return
	}
	rels := make([]string, 0, len(matches))
	for _, m := range matches {
		if f, ok := m["file"].(string); ok {
			rels = append(rels, f)
		}
	}
	t.mention(rels...)
}

// resultMatches is a type-narrow helper for tool result payloads carrying a
// "matches" key.
func resultMatches(result map[string]any) []map[string]any {
	ms, _ := result["matches"].([]map[string]any)
	return ms
}

// finalize flushes the run's learned edges into the store. The flush is one
// transaction (at-least-once); decay runs afterwards on a throttle.
func (t *signalTracker) finalize(store *Store) {
	if t == nil || store == nil || store.DB() == nil {
		return
	}
	t.mu.Lock()
	paths := append([]string(nil), t.paths...)
	scores := make(map[string]float64, len(t.scores))
	for k, v := range t.scores {
		scores[k] = v
	}
	edited := make([]string, 0, len(t.edited))
	for p, e := range t.edited {
		if e {
			edited = append(edited, p)
		}
	}
	mentioned := append([]string(nil), t.mentioned...)
	t.mu.Unlock()
	db := store.DB()
	deltas := make([]SignalEdgeDelta, 0, len(paths)+len(edited)+len(mentioned))
	for i := 0; i < len(paths) && len(deltas) < signalPairCap; i++ {
		for j := i + 1; j < len(paths) && len(deltas) < signalPairCap; j++ {
			w := scores[paths[i]]
			if b := scores[paths[j]]; b < w {
				w = b
			}
			if w <= 0 {
				w = signalWeightCoTouch
			}
			deltas = append(deltas, SignalEdgeDelta{Kind: "co_touch", Src: paths[i], Dst: paths[j], Weight: w})
		}
	}
	for _, p := range edited {
		deltas = append(deltas, SignalEdgeDelta{Kind: "edit", Src: p, Dst: p, Weight: signalWeightEdit})
	}
	for _, p := range mentioned {
		deltas = append(deltas, SignalEdgeDelta{Kind: "search", Src: p, Dst: p, Weight: signalWeightSearch})
	}
	if err := db.SignalTouchBatch(deltas); err != nil {
		slog.Warn("signals flush failed", "room", t.room, "edges", len(deltas), "err", err)
	}
	// Age the graph after a run, throttled: projects where agents never run
	// search_index (the only eager-decay caller) still get decay here.
	if _, err := db.SignalsDecayIfStale(signalDecayInterval); err != nil {
		slog.Warn("signals decay failed", "room", t.room, "err", err)
	}
}
