# Foreman: foundation and roadmap

## Repository audit

This repository is a Go 1.26 CLI application, not an empty static prototype. It has a Bubble Tea terminal UI, a tool-using agent loop, provider adapters, subagents, SQLite-backed session history, skills, and extensive unit tests. The supplied Foreman HTML is a visual/interaction prototype; its dashboard data and agent execution are simulated. The baseline `go test ./...` passed before Foreman changes. The working tree already contained unrelated local edits, so those files were not reverted or cleaned up.

## Architecture decision

Keep the proven Go agent core and its existing tests intact while adding Foreman as a separate CLI and local application boundary. This avoids a destructive rewrite and lets later milestones explicitly connect stable provider, tool, and execution interfaces. The Foreman service owns the dashboard, local workspace persistence, future orchestration and policies. It binds only to loopback; browser code has no provider credentials or direct privileged execution access.

Foreman workspace definitions are separate from BroCode's session/event database. Initial state lives in `<project>/.foreman/workspace.json` using a versioned schema, atomic replacement, and restrictive file permissions. This small store is suitable for the first team/room foundation; use SQLite migrations once durable runs, events, tasks, and approvals are introduced.

## Foundation milestone delivered

- `foreman start [--project DIR] [--port PORT] [--no-open]` launches the embedded dashboard. Port 0 chooses an available port; a busy preferred port falls back safely.
- The embedded dark dashboard is based on the supplied prototype's visual direction and clearly distinguishes working local workspace features from unavailable runtime features.
- Three versioned team templates can be previewed and imported as editable copies. Users can create named rooms and invite the same reusable team into multiple room records.
- Dashboard and local JSON APIs are project-scoped, loopback-only, and reject cross-origin POSTs. Workspace data is atomically saved with mode 0600.
- No fake agents, provider calls, source-code transfer, or shell execution are presented as working features.

## Usage

From this repository:

```sh
go run ./cmd/foreman start
```

Or build/install the `foreman` Go command from `./cmd/foreman`, then run `foreman start` inside a project. The command opens the dashboard automatically; use `--no-open` for headless/remote-terminal workflows.

## Roadmap

1. **Foundation and UX (current):** local CLI/service, project workspace persistence, dashboard navigation, team template import, and room creation. Remaining: editable agents/teams/skills, full theme system, global Kanban and filters.
2. **Provider configuration:** typed provider/model capabilities, connection tests, safe secret handling, and per-agent routing; begin with adapters already present in the Go core.
3. **Real local execution:** runtime interface and bounded harness, real logs/status/cancellation, workspace boundaries, and artifacts. Do not expose unrestricted shell execution through the dashboard.
4. **Orchestration:** task/dependency model, bounded scheduling, approval/resource policies, isolated room runs, and explicit handoffs.
5. **Safety and recovery:** verification/review, state-transition tests, restart recovery, and end-to-end coverage.
6. **Optional integrations:** evaluate only after the local core is useful and safe.

The legacy BroCode CLI remains available during migration. Remove or rename legacy components only after their replacement is implemented, tested, and no longer required by compatibility paths.
