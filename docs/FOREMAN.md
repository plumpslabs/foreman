# Foreman: architecture and status

## What this repository is

Foreman is a local-first agent workspace: a Go service with an embedded Preact dashboard, a
versioned JSON workspace store, and an agent engine provided by the open-source
[OpenHands Software Agent SDK](https://github.com/OpenHands/software-agent-sdk) (MIT).

The former BroCode codebase (CLI, TUI, agent loop, provider adapters, tool registry, skills, MCP,
SQLite session store) was removed in the restructure; the recovery checkpoint for everything that
existed before it is commit `fd15882`.

## Architecture decision

Split the system at the layer Foreman actually differentiates on:

| Layer | Owner |
| --- | --- |
| Rooms, Kanban, teams, skills, approvals, budgets, run history | Foreman (Go) |
| Persistence, loopback-only API, policy validation | Foreman (Go) |
| Dashboard UX | Foreman (Preact, `web/` → embedded `internal/foreman/web/dist`) |
| Agent loop, tools, workspace execution, events | OpenHands SDK (Python, via embedded bridge) |
| LLM provider connection | Foreman config → OpenHands `LLM(base_url=…)` |

This keeps Foreman's differentiating orchestration and UX as first-party code while avoiding a
from-scratch rewrite of the hard, well-tested parts of a coding agent.

## Engine bridge

`internal/foreman/bridge/agent_bridge.py` is embedded in the binary and materialized to a temp
dir per run. The Go runtime (`internal/foreman/engine.go`) spawns it as a subprocess:

- **stdin**: one JSON line — workspace, prompt, provider (protocol/base URL/key/model), iteration
  budget, cost budget, project-command policy.
- **stdout**: newline-delimited JSON events (`status`/`tool`/`message`) and a terminal `result`
  (`completed`/`failed`/`blocked`, message, tokens, cost). The SDK's visualizer is disabled so
  the stream stays pure JSON.
- **cancellation**: the Go side owns the deadline (`MaxExecutionSeconds`) and kills the process;
  status maps to `cancelled`/`timed_out`.
- **budget**: enforced both by `max_budget_per_run` in the SDK and a polling guard in the bridge.
- **secrets**: API keys are passed via the config line (never argv) and redacted from persisted
  errors/events.

Interpreter selection: `FOREMAN_PYTHON` env var, else `python3`, else `python`. The SDK requires
Python 3.12+; when it is missing the run fails with an explicit install hint.

Verified end-to-end against OpenHands SDK v1.59.0: spawn → config → status events → LLM call →
result → task transition to `review`, with token accounting.

## Persistence

Initial state lives in `<project>/.foreman/workspace.json` (versioned schema, atomic replace,
mode 0600). Credentials live separately in `<project>/.foreman/credentials.json` (0600) and are
never returned by the API — only a `hasApiKey` flag. Use SQLite migrations if durable runs,
events, tasks, and approvals outgrow JSON.

## Security posture

- Loopback-only listener; non-loopback Host/RemoteAddr rejected.
- Cross-origin POSTs rejected (Origin must match Host and be loopback).
- Project path must match the server's workspace on every API call.
- Request bodies capped at 1 MB with `DisallowUnknownFields`.
- Provider URLs must be absolute HTTP(S) without credentials/query/fragment; plain HTTP only for
  loopback endpoints; connection tests refuse private/link-local targets (SSRF guard).
- Runs execute with workspace root as cwd; project commands are only enabled by explicit policy.

## Dashboard

Preact SPA in `web/`, built by Vite into `internal/foreman/web/dist` and embedded via
`go:embed`. Room-centric layout:
- Left sidebar: Overview · Rooms · Teams · Agent library · Skills · Runs & activity · Settings
- **Room = workspace session**: each room has its own goal, invited teams, chat, and per-room tasks
- Room detail: toolbar + tabs (Chat · Kanban · Plan · Changes), with a right rail showing room team,
  task progress, live activity, and execution policy
- Kanban is scoped per room (no global Kanban page)
Pages: Home, Rooms, Teams, Agents, Skills, Runs, Settings, plus the RoomDetailHost.
`npm run smoke` renders the built bundle in jsdom against a mocked API.
The layout, navigation map, and room interaction model are documented in
[`docs/DASHBOARD.md`](DASHBOARD.md).

## Roadmap

1. **Done — foundation:** CLI, workspace persistence, dashboard, team templates, rooms.
2. **Done — provider configuration:** typed provider/model config, connection tests, separate
   secret storage, per-run routing through the engine.
3. **Done — real local execution:** bounded runs (iterations, wall time, cost), event stream,
   status/cancellation, workspace cwd, artifacts in the project tree.
4. **Next — orchestration:** parallel scheduling across rooms, explicit handoffs, richer
   dependency transitions, restart recovery for runs interrupted by a crash.
5. **Later — optional integrations:** Docker workspace isolation, MCP tools, multi-engine
   adapters — evaluate only when the local core proves useful and safe.

The legacy BroCode CLI is gone; nothing in the tree references it.
