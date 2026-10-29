# Foreman Dashboard Redesign

**Goal:** Room-centric UX where a **room = workspace session**. Per-room Kanban/Plan/Changes instead of global menu pages. Right sidebar (rail) holds room monitoring: team members, task progress, live activity, execution policy. Left sidebar is workspace navigation. Chat area improved. Settings fixed to scroll properly.

## Navigation map

| Sidebar item | Icon | Route/page |
|---|---|---|
| Overview | `layout-dashboard` | Workspace dashboard |
| Rooms | `messages-square` | Room list |
| Teams | `users` | Team templates + my teams |
| Agent library | `bot` | Available agent profiles |
| Skills | `puzzle` | Reusable capability bundles |
| Runs & activity | `activity` | Execution history |
| Settings | `settings-2` | Providers + execution policy |

**Room detail (3 columns):**

- Left/center: toolbar (back, title, invite) → room tabs (Chat, Kanban, Plan, Changes) → tab content
- Right rail: Room team (members with status), Task progress (track + mini list), Live activity, Execution policy

## Room tabs

| Tab | Icon | Content |
|---|---|---|
| Chat | `message-square` | Chat history scrollable + compose box with agent picker, effort picker, approval toggle |
| Kanban | `kanban` | Per-room board: Backlog · Todo · In progress · Done (+move/edit/add) |
| Plan | `git-branch` | Task graph with dependencies + scheduling rationale |
| Changes | `git-diff` | Proposed changes / review surface |

## Design tokens (from reference, preserved)

- bg `#0b0d10`, panel `#111419`, panel2 `#151920`, line `#252a33`, muted `#89919e`, text `#eef1f6`, accent `#c7f36b`, blue `#8db7ff`, orange `#ffc27a`, red `#ff8d8d`, purple `#c1a4ff`
- Font Inter; 14px body; grid: sidebar 232px + 1fr
- Responsive: mobile collapses sidebar into drawer; rail collapses at 760px

## Data flow (backed by Go)

- `/api/workspace` → rooms, teams, tasks, runs, templates, providers, skills, settings
- Create room → POST `/api/rooms`; invite teams
- Room tasks → POST `/api/rooms/{id}/tasks`; update status → PUT `/api/tasks/{id}/status`; run → POST `/api/tasks/{id}/run`
- Provider → POST `/api/providers`, test → POST `/api/providers/test`
- Agents → POST `/api/agents`, PUT/DELETE `/api/agents/{id}`
- Skills → POST/DELETE `/api/skills` (POST also updates when `id` is present); bundle files travel in `files[]`
- Settings → PUT `/api/settings`
- Chat → POST `/api/rooms/{id}/messages` (user-only until runtime connected)

## Agent library & skills

- **Agent profiles** are standalone records (`state.agents`). Creating or editing a profile sets
  the name, role, description, markdown instructions, provider/model override, effort, allowed
  tools, attached skills, and review requirement.
- **Provider-bound model picker** — the model field is a searchable combobox: the trigger shows the
  picked model as a badge with its source provider; clicking opens a searchable list of every
  configured provider's models, and picking a model from another provider switches the provider
  automatically. Custom models can be typed in via the search box.
- **Teams keep their own agent copies.** Editing a library profile does not rewrite an imported
  team; edit the team to change its roster. Team agents are added via a picker modal that lists
  library agents (excluding ones already on the team), and each roster row can be promoted to
  main agent with a "Make main" control.
- **Skill bundles** store `files: [{path, content}]` with relative paths and sub-folders. Paths are
  validated (no `..`, no absolute paths), each file is capped at 256 KiB, and a bundle at 1 MiB.
- **Mode** — `active` skills are intended for context injection; `passive` skills are reference
  only. Toggle from the Skills page or the skill editor.
- Skills are referenced by ID; legacy capability names from templates are resolved to IDs on save.

## Backend state shape (schema 3)

```ts
WorkspaceState { schemaVersion, projectPath, rooms[], teams[], agents[], templates[], providers[], tasks[], runs[], skills[], settings }
Room { id, name, goal, teamIds[], status, messages[], createdAt }
Task { id, roomId, title, description, status, priority, effort, dependencies[], approved, createdAt, updatedAt }
Task status: todo | blocked | in_progress | review | done | cancelled
Team { id, templateId, name, purpose, mainAgent, agents[], instructions, createdAt }
TeamTemplate { id, version, name, purpose, mainAgent, agents[], instructions }
Agent { id, name, role, description, instructions, providerId, model, effort, skills[], tools[], requireReview }
Skill { id, name, description, version, instructions, enabled, mode, files[{path, content}] }
ProviderConfig { id, name, protocol, baseUrl, models[], defaultModel, hasApiKey, enabled }
Settings { theme, effortMode, defaultEffort, maxConcurrentRuns, maxWorkersPerTask, maxDelegationDepth, maxRetries, maxExecutionSeconds, budgetUsd, requireRunApproval, allowProjectCommands }
ChatMessage { id, role, content, createdAt }
```

Schema 2 workspaces migrate in place when opened: team agents become library profiles and skills
gain a mode plus a file tree.

## Default agents (from templates)

- **Atlas** — Main agent · Planner & Coordinator (product-team)
- **Mira** — Backend engineer
- **Quinn** — QA & Test Analyst
- **Nova** — Lead Frontend Architect (frontend-team)
- **Pixel** — UI Specialist & Stylist
- **Engineering Lead**, **Architect**, **Implementer**, **Independent Reviewer** (deep-engineering)
- **Quick Coordinator**, **Implementer** (quick-fix)

## Room interaction

- **Routing** — the dashboard uses hash routes (`#/rooms/:id`, `#/agents/:id`, …) so a refresh
  returns to the same page.
- **Delete room** — the rooms list has a trash action (confirm dialog); deleting removes the room,
  its tasks, and its runs atomically (`DELETE /api/rooms/{id}`).
- **Runtime status** — rooms show Running/Paused/Idle from their runs. A running room exposes
  *Pause run* (a graceful SIGTERM to the bridge, which persists the conversation and stops at the
  next step boundary; the task stays `in_progress`). A room whose last run is paused, failed,
  timed out, or cancelled exposes *Continue*, which starts the same task again. Because Foreman
  persists each conversation under the task id (`persistenceDir` + `conversationId`), Continue
  resumes the saved conversation instead of starting over.
- **Participants** — teams can be invited or kicked per room (`PUT /api/rooms/{id}/teams`).
- **Agent questions** — an agent can request a decision by ending its reply with a fenced
  `question` block whose first line is the question and whose `- ` lines are options. The runtime
  stores it and the room shows a card with option buttons plus a custom answer. Answering records
  the choice in the chat.
- **Chat compose** — Enter sends, Shift+Enter adds a newline, and sends are queued so you can type
  while an earlier message is still in flight (no spam/double submit). The agent selector lists
  every agent across the invited teams; routing is via `@agent` mention.
- **Live agents strip** — above the input, every invited agent shows as a chip with a status dot:
  pulsing while running, dimmed when paused, muted when idle.
- **Room rail members** — each invited team in the right rail lists its member badges (main agent
  highlighted).
- **Invited-team card** — the chat shows a centered card naming each invited team and its members.
- **Changes** — unified diffs from a room's runs render red/green with collapsible files.

## Tool wiring and permissions

An agent's `tools[]` is the real permission list. On a run Foreman resolves it server-side
(`internal/foreman/tools.go`) into concrete OpenHands imports and passes them to the bridge:

- `file_editor` → `FileEditorTool` (always ensured)
- `task_tracker` → `TaskTrackerTool`
- `terminal` → `TerminalTool` — dropped unless the workspace sets `allowProjectCommands`
- `grep` / `glob` → `GrepTool` / `GlobTool`
- `apply_patch` → `ApplyPatchTool`, `planning_editor` → `PlanningFileEditorTool`
- `browser`, `subagents`, `workflow`, `ask_oracle`, `tom_consult`, `sleeptime` — optional; loaded
  when the SDK install provides the package, otherwise skipped with a status event

Legacy template names (`read`, `search`, `edit`, `run_tests`, `delegate`, …) are normalized to
canonical ids on import and save, so old workspaces keep working.

## Permissions model (policy-based, reflected in UI)

| Action | Who | Gate |
|---|---|---|
| Create room / task | Any workspace member | name validation, char limits |
| Run task | Agent runtime | provider configured + API key saved + approval if `requireRunApproval` |
| Edit task status | Room participant | `in_progress` blocks on incomplete dependencies |
| Approve plan/run | Human | per room + per workspace policy |
| Project commands (terminal) | Worker only if `allowProjectCommands` |
| Run concurrency | All | `maxConcurrentRuns` (1–8), enforced server-side |
| Budget | Per workspace | `budgetUsd`; 0 = unlimited |
| API keys | Admin only | stored 0600, never returned by API (only `hasApiKey`) |
| Tool access | Per agent profile (tools[]) | resolved server-side and loaded into the OpenHands run; terminal is dropped unless `allowProjectCommands` |
| Create / edit agent | Workspace owner | unique name, skill and provider references must exist |
| Create / import skill | Workspace owner | relative paths only, 256 KiB per file, 1 MiB per bundle |
| Switch skill mode | Workspace owner | active = context injection, passive = reference only |
| Manage providers | Workspace owner | URL must pass the SSRF guard; keys stay local |

## Providers

Presets ship for TokenHarbor, OpenCode Zen/Go, DeepSeek, MiniMax, Anthropic, and OpenAI, plus a
custom endpoint. A provider holds a base URL, protocol, key, and a list of models; the first model
is the default. The editor can discover models (`/models`) and test a real request. Deleting a
provider clears agent/team references and removes its credential.

An agent's `providerId` + `model` override the workspace default for runs of that agent.

## Skills and runtime injection

- **Active skills** (mode `active`) are injected into the run prompt as a `## Active skills`
  section with their instructions and bundle file list (bounded to 8000 chars total).
- **Passive skills** (mode `passive`) are listed by name + description as reference-only context.
- Skills resolve by id or name from the agent's `skills[]`; unrecognized references are ignored.
- The bridge enables the SDK's summarizing condenser (`default_condenser`) on every run so long
  conversations stay inside the model window.
- **Diagnostics stream live** — the Go engine forwards the bridge's stderr lines into the run's
  event feed (`[engine] …`), so SDK/model/HTTP failures are visible in the room instead of a
  silent "no response". The bridge also logs the resolved model and base URL, maps an ERROR/STUCK
  agent loop to a failed run, and falls back to recent event text when the final agent message is
  empty. Conversation construction steps down optional kwargs (persistence, budget, iterations) so
  SDK version differences never mask a real `TypeError`.

## Icons

- README: uses `publics/foreman.png` (1254×1254). Vite copies `publics/` into the embedded dist as
  the favicon and sidebar mark, so there is one source image.
- All navigation, tabs, and actions use a small inline-SVG set in `web/src/icons.jsx` — no CDN and
  no extra dependency, so the dashboard works offline.
