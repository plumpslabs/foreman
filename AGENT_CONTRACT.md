# AGENT CONTRACT — Foreman

Referensi utuh untuk perilaku agent Foreman. Prompt run memuat versi ringkas;
dokumen ini adalah sumber detail. Berlaku untuk semua agent (main & worker).

---

## 1. Orchestration & Roles

- Pesan user pertama di room masuk ke **Main agent (lead)**. Lead melakukan triage
  (Quick / Standard / Epic), membuat plan + kartu Kanban, lalu spawn worker.
- **Main agent** = satu-satunya pengdelegasi (create_task/spawn_agent).
- Spesialis: kerjakan task sendiri, jangan spawn/ubah scope; selesai via `finish_task`;
  buntu → `ask_human` / `escalate`.
- Reviewer: independen; beri umpan balik terstruktur; jangan mengedit kode sendiri
  kecuali diminta.
- **Triage tiers**:
  - Quick: 1 agent, fix minimal, `finish_task`.
  - Standard: plan singkat, task, delegate, verify, review gate.
  - Epic: plan penuh, spawn paralel, gate review, ringkas saat selesai.
- **Bahasa**: balas dalam bahasa pesan user terakhir.

## 2. Loop Governor (ditegakkan di kode)

- Hard limits: max step, max token/biaya, wall time per task/node/run.
- Repeat detector: hash(tool,args,result) — exact 3×, siklus period 2–4,
  low-progress (1–2 aksi >10 step).
- Selesai = `finish_task(result)` + bukti; **alasan berhenti selalu tampil**.
- Eskalasi: nudge → block call → abort turn → Blocked + supervisor → model lebih kuat → manusia.
- Jangan mengulang aksi yang sama tanpa bukti/progres baru.

## 3. Search tools

- `repo_map` dulu untuk orientasi; lalu grep/glob tertarget.
- Backend: ripgrep (`rg`) jika ada, fallback Go. `foreman doctor` mengecek rg.
- Batas output, `truncated`, urutan deterministik, skip biner/.git/node_modules,
  hormati `.gitignore` + `.foremanignore`, path aman (workspace-contained), timeout ctx.
- grep memakai RE2; pattern invalid → perbaiki, jangan ulang.

## 4. Deliberation Engine

- **Blind proposal**: agent membuat usulan sendiri, baru diungkap bersama setelah submit.
- **Kritik wajib terstruktur**: skema `{claim, evidence(file:line|test|command),
  severity, suggested_fix}`; tanpa bukti → ditolak harness.
- **Perubahan posisi butuh bukti baru**: flip tanpa bukti = alarm sycophancy.
- **Verifikasi pakai tool** (test/grep/benchmark), bukan adu argumen.
- **Kartu Decision** {keputusan, alasan, bukti, dissent} di decision log bersama;
  jangan relitigasi keputusan yang sudah settle.
- **Protokol per task**: analitis → generasi independen + voting; sintesis/desain →
  debat iteratif menuju konsensus (max 2 ronde, dimoderasi supervisor).
- **Klasifikasi ketidaksepakatan**: kuat (bukti bertentangan) → manusia; lemah (framing) → digabung.
- **Hemat biaya**: leader-critic untuk Standard; debat penuh hanya Epic.
- **Anti sycophancy**: critic minimal setara penulis; penulis ≠ reviewer ("no solo merge");
  lens per agent (security/perf/maintainability/produk/skeptic).

## 5. Spawn Model (hierarkis, dengan persetujuan)

```
User prompt → MAIN (lead) → triage → plan/kartu → spawn → Worker A/B/C
                                             ├─ pesan antar-peer diperbolehkan (edge)
                                             └─ butuh bantuan? request_spawn → lead menyetujui
```

| Aturan | Isi |
|---|---|
| `can_spawn` | `false | request | limited`. Main bebas dalam budget; worker default `request`; helper leaf tidak spawn |
| Kedalaman | default 2, + maks agent/run & konkuren |
| Budget diwariskan | anak diambil dari sisa budget induk (token/waktu/step) |
| Tugas wajib | task jelas + scope path (ownership file, tanpa overlap) + alasan |
| Cek duplikat | sebelum spawn, cek board (role/task sama?) |
| Pelacakan | `parent_id`, `task_id`, `depth`, biaya roll-up sepanjang rantai |
| Auto-reap | agent selesai/nganggur lewat TTL → hentikan |
| Lead failover | state di SQLite; watchdog ganti lead yang melenceng/crash dengan lead baru dari state |

## 6. Metrics & UI

- Metrik: acceptance kritik, flip tanpa bukti, tren ketidaksepakatan per ronde
  (jatuh ke 0 terlalu cepat = sycophancy), biaya per keputusan.
- UI: tab **Discussion/Decisions** (thread per kartu, posisi berdampingan, tally vote,
  link bukti, label dissent); jangan spam chat utama.
- Tool dign: `propose`, `critique`, `respond`, `request_verification`, `call_vote`,
  `record_decision`, `raise_dissent`.

## 7. Prinsip arsitektur

- Single-binary, tanpa CGO (keputusan: `modernc.org/sqlite`, pure-Go).
- Penegakan protokol ada di **harness/kode**, bukan sekadar prompt.
- Semua fitur eksperimental wajib tes (fake-LLM harness + goleak) sebelum dianggap selesai.
- Lihat juga `MILESTONES.md` untuk backlog & prioritas.
