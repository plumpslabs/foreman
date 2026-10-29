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
- Skills → POST/DELETE `/api/skills`; Settings → PUT `/api/settings`
- Chat → POST `/api/rooms/{id}/messages` (user-only until runtime connected)

## Backend state shape (unchanged)

```ts
WorkspaceState { schemaVersion, projectPath, rooms[], teams[], templates[], providers[], tasks[], runs[], skills[], settings }
Room { id, name, goal, teamIds[], status, messages[], createdAt }
Task { id, roomId, title, description, status, priority, effort, dependencies[], approved, createdAt, updatedAt }
Task status: todo | blocked | in_progress | review | done | cancelled
TeamTemplate { id, name, purpose, mainAgent, agents[], instructions }
Agent { name, role, description, instructions, providerId, model, skills[], tools[], requireReview }
Skill { id, name, description, instructions, enabled }
ProviderConfig { id, name, protocol, baseUrl, models[], defaultModel, hasApiKey, enabled }
Settings { theme, effortMode, defaultEffort, maxConcurrentRuns, maxWorkersPerTask, maxDelegationDepth, maxRetries, maxExecutionSeconds, budgetUsd, requireRunApproval, allowProjectCommands }
ChatMessage { id, role, content, createdAt }
```

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

Skills are metadata; they attach to agents but the runtime bridge does not yet inject them (marked in UI).

## Icons

- README: use `publics/foreman.png` (1254×1254). Add to `web/public/foreman.png` for dashboard manifest/pwa.
- All nav items use Lucide icons (pulled from reference template).
