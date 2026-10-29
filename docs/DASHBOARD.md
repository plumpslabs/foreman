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
- **Teams keep their own agent copies.** Editing a library profile does not rewrite an imported
  team; edit the team to change its roster.
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
| Tool access | Per agent profile (tools[]) | enforced by bridge/runtime; dashboard shows which tools each agent may use |
| Create / edit agent | Workspace owner | unique name, skill and provider references must exist |
| Create / import skill | Workspace owner | relative paths only, 256 KiB per file, 1 MiB per bundle |
| Switch skill mode | Workspace owner | active = context injection, passive = reference only |

Skills are stored metadata; the runtime bridge does not yet inject them into a run (the active/passive
mode is persisted so injection can be wired without another migration).

## Icons

- README: uses `publics/foreman.png` (1254×1254). Vite copies `publics/` into the embedded dist as
  the favicon and sidebar mark, so there is one source image.
- All navigation, tabs, and actions use a small inline-SVG set in `web/src/icons.jsx` — no CDN and
  no extra dependency, so the dashboard works offline.
