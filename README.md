<p align="center">
  <img src="publics/foreman.png" alt="Foreman" width="128" />
</p>

```text
███████╗ █████╗ ██████╗ ███╗   ██╗███████╗██╗   ██╗██╗  ██╗
██╔════╝██╔══██╗██╔══██╗████╗  ██║██╔════╝██║   ██║╚██╗██╔╝
█████╗  ███████║██████║██╔██╗ ██║█████╗  ██║   ██║ ╚███╔╝
██╔══╝  ██╔══██║██╔══██╗██║╚██╗██║██╔╝   ██║   ██║ ██╔██╗
██║     ██║  ██║██║  ██║██║ ╚████║███████╗╚██████╔╝██╔╝ ██╗
╚═╝     ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═══╝╚══════╝ ╚═════╝╚═╝  ╚═╝
local-first agent workspace
```

<p align="center"><b>Foreman</b><br/>
<em>Local-first agent workspace for teams of coding agents</em><br/>
Rooms, Kanban, team templates, provider config, and bounded runs — driven by the open-source OpenHands engine, on your own machine.</p>

<p align="center">
  <a href="https://github.com/plumpslabs/foreman/releases"><img src="https://img.shields.io/badge/release-latest-blue.svg?style=flat-square" alt="Release"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.26+-00ADD8.svg?style=flat-square&logo=go" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green.svg?style=flat-square" alt="License"></a>
</p>

---

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/plumpslabs/foreman/main/scripts/install.sh | sh
```

The installer downloads the latest prebuilt binary, verifies its checksum, and installs it to
`/usr/local/bin` (or `~/.local/bin`). It also checks whether your Python can run the agent engine.

## Run

```sh
foreman start                 # dashboard for the current project
foreman start --project DIR   # …or a specific project
foreman start --port 8080     # preferred port (falls back to an available one)
foreman start --no-open       # headless / remote terminal
```

Foreman binds to `127.0.0.1` only, rejects cross-origin POSTs, and never starts an agent or an
external request on its own.

## Dashboard

Open `http://127.0.0.1:8080/?project=<project-dir>` — the UI is room-centric:

- **Left sidebar** — workspace navigation (Overview · Rooms · Teams · Agent library · Skills · Runs & activity · Settings)
- **Rooms** — every room is an isolated work session with its own goal, invited teams, chat, and tasks
- **Room detail** — toolbar, tabs (Chat · Kanban · Plan · Changes), and a right rail showing room team, task progress, live activity, and execution policy
- **Kanban** lives *inside* each room, not as a global menu item — tasks are scoped to the room they belong to

## Architecture

```
Foreman Workspace (this repo)
├── Dashboard          Preact SPA (web/), built with Vite, embedded in the binary
├── Orchestration      Go: rooms, teams, Kanban, tasks + dependencies, approvals,
│                      budget/concurrency policy, run history (internal/foreman)
└── Engine             OpenHands Software Agent SDK via an embedded Python bridge
                       (internal/foreman/bridge/agent_bridge.py, spawned per run)
```

Foreman owns orchestration, persistence, policy, and UX. The agent loop, tools, and execution
come from the [OpenHands SDK](https://github.com/OpenHands/software-agent-sdk) (MIT), so nothing
mission-critical is reimplemented from scratch.

### Engine requirements

Runs execute in a Python subprocess:

```sh
python3 -m pip install openhands-sdk openhands-tools   # Python 3.12+ required
```

- `FOREMAN_PYTHON` overrides the interpreter (point it at a venv if you prefer).
- Without the SDK, runs fail with a clear install hint — the dashboard and API keep working.

## Provider setup

Settings → Providers accepts any OpenAI-compatible endpoint (Ollama, vLLM, gateways) or an
Anthropic endpoint: base URL, model IDs, API key. Keys are stored separately in
`.foreman/credentials.json` (mode 0600) and never returned by the API. The connection test makes
a real request to the endpoint.

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

## Data

| What | Where |
| --- | --- |
| Workspace (rooms, teams, tasks, runs, settings) | `<project>/.foreman/workspace.json` (0600, atomic writes) |
| Provider API keys | `<project>/.foreman/credentials.json` (0600) |
| Engine session store | `<project>/.foreman/agent.db` |

## Releases

Tagged pushes trigger GoReleaser (`release.yml`), which publishes `foreman_<ver>_<os>_<arch>`
archives plus `checksums.txt` — the artifacts the curl installer consumes.
