<p align="center">
  <img src="publics/foreman.png" alt="Foreman" width="128" />
</p>

<p align="center"><b>Foreman</b><br/>
<em>Local-first agent workspace for teams of coding agents.</em><br/>
Rooms, Kanban, team templates, provider presets, MCP servers, and bounded runs — all on your own machine.</p>

<p align="center">
  <a href="https://github.com/plumpslabs/foreman/releases"><img src="https://img.shields.io/badge/release-latest-blue.svg?style=flat-square" alt="Release"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.26+-00ADD8.svg?style=flat-square&logo=go" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green.svg?style=flat-square" alt="License"></a>
</p>

---

Foreman is a local-first workspace that organizes coding agents into **rooms** — isolated work
sessions, each with its own goal, invited teams, chat, Kanban board, and plan. It is a single
self-contained Go binary: the [Google ADK Go](https://github.com/google/adk-python) engine is
embedded natively, the dashboard is a Preact SPA compiled into the binary, and all state lives in
SQLite inside the project folder. No Python, no daemons, no external services — just `foreman
start`.

## Features

- **Rooms** — each room is an isolated session with a goal, team membership, chat, and per-room
  Kanban. Tabs for **Chat · Kanban · Plan · Changes**, with a live activity rail.
- **Teams & templates** — reusable team templates with markdown instructions, purpose, main agent,
  and full agent roster.
- **Agent library** — reusable profiles: role, markdown instructions, model/provider, allowed
  tools, skills, and review policy.
- **Skills** — markdown bundles organized in folders; *active* skills are injected into an
  agent's context, *passive* ones stay as reference.
- **Runs & activity** — run history with per-run details, live status, pause/continue, and
  execution policy (concurrency, budget, approvals).
- **Signals** — signals mined from runs, surfaced as an explorable graph.
- **MCP Servers** — global server definitions (stdio or HTTP); rooms opt in per server, with
  connection testing and per-server status.
- **Provider presets** — TokenHarbor, OpenCode Zen (with local auto-detection), OpenCode Go,
  DeepSeek, MiniMax, Zro, Anthropic (Claude), Mercury, OpenAI (GPT), or a custom endpoint.
- **Code search** — repository-wide search backed by SQLite FTS5, with ripgrep as a fast backend
  when available.
- **Symbol map** — every indexed file's declarations (functions, methods, types) with exact line
  ranges, persisted in SQLite and rebuilt incrementally; Go files use the stdlib AST for exact
  positions. The `symbol_map` tool locates a definition instantly, so an agent reads only the
  precise `view_file` line range instead of grepping around.
- **Session journal** — structured, workspace-wide facts about what changed and why
  (`file`/`func`/`action`/`dependency`), written by the `memory` journal tool, the `/journal`
  slash command, or automatically when a task finishes. Surfaced in a room **Journal** tab and
  injected into every new run as a consolidated per-file digest — no cold-start re-exploration.
- **Rework guardrail** — after three consecutive edit→still-failing diagnose cycles, the loop
  governor stops the run and points at rollback or a human decision instead of burning tokens on
  the same fix.
- **Markdown everywhere** — instructions, plans, and chat render formatted markdown, not raw text.
- **Hash routing** — `#/rooms/:id` keeps your place across a refresh.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/plumpslabs/foreman/main/scripts/install.sh | sh
```

The installer downloads the latest prebuilt binary for your OS/architecture, verifies its
checksum against the release's `checksums.txt`, and installs it to `/usr/local/bin` (or
`~/.local/bin` if `/usr/local/bin` isn't writable), then creates an `fm` alias next to it.

The binary is fully self-contained — engine and dashboard are embedded, so there is nothing else
to install. Build from source with Go 1.26+:

```sh
go build -o foreman ./cmd/foreman
```

## Run

```sh
foreman start                 # dashboard for the current project (alias: fm start)
foreman start --project DIR   # ...or a specific project
foreman start --port 8080     # preferred port (0 picks an available one)
foreman start --no-open       # headless / remote terminal
foreman doctor                 # engine + workspace health check
foreman doctor --project DIR   # inspect a specific project
foreman version                # print version
```

`foreman start` prints a startup summary — project path, data directory, engine status, and the
dashboard URL — then opens your browser. `foreman doctor` verifies the embedded Google ADK Go
engine, reports whether ripgrep is available for fast search, and confirms the workspace state.

Foreman binds to `127.0.0.1` only, rejects cross-origin POSTs, and never starts an agent or an
external request on its own.

## Dashboard

Open `http://127.0.0.1:<port>/?project=<project-dir>` — the UI is room-centric:

- **Sidebar** — Overview · Rooms · Teams · Agent library · Skills · Runs & activity · Learned
  Graph, plus MCP Servers and Settings.
- **Rooms** — every room is an isolated work session with its own goal, invited teams, chat,
  tasks, and policy.
- **Room detail** — toolbar plus tabs (Chat · Kanban · Plan · Changes · Artifacts) and a right
  rail with room team, task progress, live activity, and execution policy.
- **Kanban** lives *inside* each room — tasks are scoped to the room they belong to.
- **Agent library** — reusable profiles with markdown instructions, model/provider, allowed
  tools, skills, and review policy.
- **Skills** — import a folder of markdown or create a skill; toggle *active* (injected) vs.
  *passive* (reference).
- **Teams** — imported teams first; open a team to edit its name, purpose, markdown
  instructions, main agent, and inspect every agent on it.
- **MCP Servers** — global server definitions with per-server status (Connected · tools count,
  Failed, Checking…), one-click connection test, and re-check; rooms opt in to individual
  servers.
- **Settings** — tabbed: **Providers**, **Execution**, and **Appearance**.
- **Room controls** — live runtime status with pause and continue, invite/kick teams, and agent
  questions answered with options or a custom reply.
- **Changes** — unified diffs render red/green with per-file collapse.

## Architecture

```
Foreman Workspace (this repo)
├── Dashboard          Preact SPA (web/), built with Vite, embedded via go:embed
├── Orchestration      Go: rooms, teams, Kanban, tasks + dependencies, approvals,
│                      budget/concurrency policy, run history (internal/foreman)
├── Engine             Google ADK Go, embedded natively (google.golang.org/adk/v2)
│                      — agent loop, tools, and model routing, no subprocess
└── Persistence        SQLite via modernc.org/sqlite (pure Go, zero CGO)
```

Foreman owns orchestration, persistence, policy, and UX. The agent loop, tool integration, and
model routing come from the [Google ADK Go](https://github.com/google/adk-python) engine, compiled
directly into the binary — there is no Python bridge and no per-run subprocess. SQLite is served
by `modernc.org/sqlite`, a pure-Go translation of the C library, so the whole project builds and
runs with **zero CGO**.

## Provider setup

Settings → Providers ships presets for **TokenHarbor**, **OpenCode Zen**, **OpenCode Go**,
**DeepSeek**, **MiniMax**, **Zro**, **Anthropic (Claude)**, **Mercury**, **OpenAI (GPT)**, and a
**custom** endpoint. Each provider stores a base URL, protocol, API key, and one or more model IDs
— the first model is the default. The provider editor can **discover models** from the endpoint
and **test the connection** with a real request.

**OpenCode Zen** is special: if the `opencode` CLI is on your PATH, Foreman runs `opencode models`
and auto-detects the Zen gateway's **free** models (e.g. `glm-5.3-flash-free`) — the prefix-stripped
model IDs are pre-filled straight into the provider form, ready to save. Zen's free tier is
keyless (`public`), so you can pick a free model and start running immediately.

Keys are stored separately in `.foreman/credentials.json` (mode 0600) and never returned by the
API. An agent can pin its own provider and model; otherwise runs use the first enabled provider.

## Data

| What | Where |
| --- | --- |
| Global profile (team templates, agent library, teams, skills, providers, MCP servers, settings) | `~/.foreman/global.json` (or `$FOREMAN_GLOBAL_PATH`) |
| Per-project state (rooms, tasks, runs, approvals, artifacts, messages, memory, signals) | `<project>/.foreman/foreman.db` — SQLite, WAL mode, incl. an FTS5 code-search index, a line-precise symbol map, and the session journal |
| Conversation threads (per room + agent, survives restarts) | `<project>/.foreman/sessions.db` |
| Provider API keys | `<project>/.foreman/credentials.json` (0600) |
| Project metadata | `<project>/.foreman/workspace.json` (atomic writes) |

SQLite is opened with WAL journaling and a busy timeout; writes are atomic, so a crash never
corrupts the workspace.

## Dashboard development

```sh
cd web
npm install
npm run dev      # Vite dev server
npm run build    # build into internal/foreman/web/dist (embedded by go:embed)
npm run smoke    # jsdom smoke test against the built bundle
```

`make build` runs the web build first, so `go build ./cmd/foreman` alone works when `dist/` is
already present.

## Development

```sh
make check   # go vet + gofmt
make test    # unit tests
make race    # tests with race detector
make run     # foreman start for the current directory
```

## Security

- The dashboard binds to `127.0.0.1` only and rejects cross-origin POSTs.
- Provider API keys live in `.foreman/credentials.json` with mode `0600` and are never returned
  by the API.
- The workspace and its database never leave your machine unless you move the project folder
  yourself.
- Foreman never starts an agent or fires an external request on its own — runs start only when
  you enqueue a task.

## Releases

Tagged pushes trigger GoReleaser (`release.yml`), which publishes `foreman_<ver>_<os>_<arch>`
archives plus `checksums.txt` — the artifacts the curl installer consumes.

## License

MIT — see [LICENSE](LICENSE).
