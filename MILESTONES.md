# MILESTONES — Foreman

Dokumen ini mencatat pekerjaan yang belum selesai, prioritas, dan kriteria selesainya.
Sumber: hasil riset & diskusi arsitektur (Claude Code / OpenCode / Aider / CodeBuddy / Crush / TokenOps / MCP security).

## Legenda prioritas
- P0 = blokir / penting, segera
- P1 = penting, setelah P0
- P2 = bernilai tapi bisa menunggu


## Backlog (belum dikerjakan)

### P0 — Test loop dengan fake-LLM harness (SELESAI — detektor)
- **Apa**: detektor loop diekstrak jadi `actionDetector` murni (`loopdetect.go`) dan diuji tanpa LLM (`loopdetect_test.go`): doom 3×, siklus period 2–4, low-progress, reset, benign.
- **Status**: ✅ tes scripted hijau. Perbaikan: window diisi sebelum early-return (bug lama); low-progress kini berbasis **nama tool** (menangkap "tool sama, args berubah" yang lolos doom/cycle).
- **Sisa (lanjutan)**: property test "no infinite loop" end-to-end via `fakeEngine` + kasus budget & guard.

### P1 — Spike `gotreesitter` untuk `repo_map` AST (SELESAI — keputusan: opt-in)
- **Apa**: extractor dibungkus interface `symbolSource` (`symbols.go`); implementasi tree-sitter (`symbols_treesitter.go`, build tag `ts_symbols`) + benchmark (`symbols_bench_test.go`).
- **Hasil benchmark** (43 file nyata, build stripped):
  - Binary: default **51.1 MB** (tak berubah) vs `-tags ts_symbols` **60.1 MB** (+9 MB / +18%).
  - Kecepatan: **~57× lebih lambat** (0.11 ms/file → 6.3 ms/file).
  - Akurasi: **lebih baik** (tanpa local var; `type X`/`func Y` konsisten; deklarasi bersarang).
- **Keputusan**: default tetap **heuristik** (cepat & ringan); tree-sitter **opt-in** via `go build -tags ts_symbols` (atau `make` dengan `GOFLAGS=-tags=ts_symbols`). Pure-Go, tanpa CGO.
- **Sisa (bila mau default)**: parse hanya file yang muat budget token (lazy), jangan seluruh repo.

### P1 — FTS5 probe + inverted index (probe SELESAI — FTS5 tersedia)
- **Hasil probe**: `modernc.org/sqlite` mengompilasi `ENABLE_FTS5`; `CREATE VIRTUAL TABLE ... USING fts5` + `MATCH` berjalan. Jadi inverted index **bisa** dibangun tanpa CGO.
- **Sisa**: bangun tabel `code_index` incremental (dari `walkProject`) + tool `search_index(q)`; fallback ke grep bila tak tersedia.

### P1 — Near-duplicate (Jaccard)
- **Apa**: deteksi pengulangan hampir-sama (bukan persis) via kemiripan teks.
- **Kenapa**: menangkap loop yang "sedikit beda" tiap iterasi.
- **Kriteria selesai**: diuji dgn fake harness; false-positive rendah.

### P1 — Multi-agent: hop / ping-pong / card-bounce
- **Apa**: counter hop pada mention antar-agent (A→B→C melewati batas → tolak); deteksi ping-pong tanpa perubahan state; batas bolak-balik kartu Review↔In progress (naik ke supervisor).
- **Kenapa**: mencegah "invite loop" antar agent saat orchestration paralel muncul.
- **Catatan**: relevansi penuh setelah orchestration berbasis graph/paralel ada.
- **Kriteria selesai**: batas & eskalasi teruji.

### P2 — Sandbox shell (policy engine + isolasi)
- **Apa**: eksekusi perintah dalam sandbox (allowlist, timeout, isolasi proses) — bukan sekadar `sh -c`.
- **Kenapa**: keamanan tool `run_checks`/`bash`.
- **Kriteria selesai**: deny-list/allow-list teruji; tidak ada escape path.

### P2 — Limiter + retry dengan `Retry-After`
- **Apa**: rate limiter per provider + backoff yang menghormati header `Retry-After` (fair warning: `x/time/rate` saja tidak cukup).
- **Kriteria selesai**: simulasi 429/rate-limit teruji.

### P2 — Keyring untuk token (setelah env var)
- **Apa**: simpan token MCP/provider di OS keychain (`zalando/go-keyring`), fallback file terenkripsi; prioritas env var dulu.
- **Kriteria selesai**: token tidak plaintext di log/disk kecuali fallback eksplisit.

### P2 — MCP gateway pooling (stdio)
- **Apa**: daemon/gateway agar N room berbagi proses stdio (hemat proses & cold-start).
- **Catatan**: sengaja ditahan; aktifkan setelah ada banyak room paralel.
- **Kriteria selesai**: isolasi per-session tetap terjamin.


## Sudah selesai (referensi cepat)
- Loop guard (max rounds) + doom 3× + siklus period 2–4 + low-progress — kode engine; detektor murni `actionDetector` + unit test (doom/cycle/low-progress/reset/benign).
- Budget run-scoped (`BudgetUSD` + token/cost riil dari `UsageMetadata`).
- Eskalasi model pada retry (fallback model).
- Panic recovery: HTTP + per-goroutine run/drain (run ditandai `failed`).
- Gitignore benar: go-git matcher (bukan sabhiram) — `git check-ignore` semantics.
- Spawn gating: hanya lead yang boleh spawn (`request_spawn` + approve flow sudah jalan), depth cap default 2, task membawa `parent_id` (chain depth).
- `repo_map` heuristik (default) + extractor tree-sitter opt-in (`-tags ts_symbols`, interface `symbolSource`).
- SearchBackend: `rg` preferred + fallback Go; `foreman doctor` cek rg.
- MCP: global servers + per-room toggle + status koneksi + prefix nama tool `mcp__<server>_<tool>`.
- Fake-LLM harness (`fakeEngine`) + goleak — fondasi tes.
- Prompt terpadu: orchestration + role + loop governor + search tools.
- UI: notice chat, live activity detail, task detail modal, badge MCP.
- Plan tab = dependency graph: `PlanGraph` (web/src/plan.jsx) — layered longest-path, edge = "A memblokir B", node berwarna status, panel Ready/In-progress/Waiting + progress.
- Tool `diagnose` (bug/ops): memilih check sesuai bahasa (go/rust/node/python), read-only, gated `allowProjectCommands`; ringkasan terstruktur (checks/failed/summary) — `diagnose.go` + tes.
- Code search index: FTS5 (`code_index` + `code_files`) incremental — `IndexWorkspace`/`SearchWorkspace` di `codeindex.go`; tool `search_index` (grep/glob/search set) dengan fallback scan bila FTS5 absen; tes incremental (index/ubah/hapus).
- Issue severity (🔴 blocker / 🟡 warning / ⚪ info): field `Severity` pada approval & task, `escalate`/`ask_human`/`create_task`/`update_task` menerima severity, inbox diurut & diwarnai, topbar + Kanban + notice chip berwarna.
- Changes tab: breadcrumb sticky (folder + nama file tebal), nomor baris old/new, dan **diff2html** (side-by-side + syntax highlight) dengan toggle Split/Unified.
- Layout room dipadatkan: right rail collapsible (persisten `localStorage`), chat/tab lebih lebar.


## Catatan proses
- Setiap item P0–P1 harus disertai tes (fake harness bila relevan) sebelum dianggap selesai.
- Jangan menambah dependency CGO (keputusan arsitektur: single-binary, `modernc.org/sqlite`).
