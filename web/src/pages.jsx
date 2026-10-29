import { useEffect, useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';

const esc = (s) => String(s ?? '');
const timeAgo = (iso) => {
  const d = new Date(iso);
  const s = Math.floor((Date.now() - d.getTime()) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return d.toLocaleDateString();
};

function avatarInitials(name) {
  return String(name || '?').slice(0, 2).toUpperCase();
}

function avatarClass(tone) {
  switch (tone) {
    case 'green': return 'agent-avatar green';
    case 'blue': return 'agent-avatar blue';
    case 'purple': return 'agent-avatar purple';
    case 'orange': return 'agent-avatar orange';
    default: return 'agent-avatar';
  }
}

/* ---------- Shared components ---------- */

function StatCard({ label, icon, num, sub }) {
  return (
    <article class="stat">
      <div class="stat-top">
        <span class="stat-icon"><Icon name={icon} size={15} /></span>
        <span>{esc(label)}</span>
      </div>
      <div class="stat-num">{esc(num)}</div>
      <div class="stat-foot">{esc(sub)}</div>
    </article>
  );
}

function RoomRow({ room, onOpen }) {
  const statusTag = {
    planning: 'muted', running: 'blue', review: 'orange', cancelled: 'red', completed: 'green',
  }[room.status] || 'muted';
  const teamCount = (room.teamIds || []).length;
  return (
    <article
      class="room-row clickable"
      role="button"
      tabindex="0"
      aria-label={`Open room ${room.name}`}
      onClick={() => onOpen(room.id)}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen(room.id); } }}
    >
      <div class="room-symbol"><Icon name="message-square" /></div>
      <div class="room-main">
        <div class="room-title">{esc(room.name)}</div>
        <div class="room-sub">{esc(room.goal || 'No goal provided.')}</div>
        <div class="room-foot">
          <span class={`tag ${statusTag}`}>{esc(room.status)}</span>
          <span class="meta-text">{teamCount} team{teamCount === 1 ? '' : 's'} · {timeAgo(room.createdAt)}</span>
        </div>
      </div>
    </article>
  );
}

function TemplateCard({ template, onImport }) {
  return (
    <article class="team-card">
      <div class="team-card-top">
        <div class="team-icon"><Icon name="users" size={19} /></div>
      </div>
      <div class="team-name">{esc(template.name)}</div>
      <div class="team-desc">{esc(template.purpose)}</div>
      <div class="meta-text" style="margin-bottom:9px">
        Main agent: <b>{esc(template.mainAgent)}</b> · {(template.agents || []).length} roles
      </div>
      <div style="display:flex;gap:5px;flex-wrap:wrap">
        {(template.agents || []).map((a) => (
          <span class="tag">{esc(a.name)} · {esc(String(a.role).split('·')[0].trim())}</span>
        ))}
      </div>
      <button class="btn primary" style="margin-top:14px" onClick={() => onImport({ kind: 'team', templateId: template.id })}>
        Import editable copy
      </button>
    </article>
  );
}

function ActivityItem({ icon, text, time }) {
  return (
    <div class="activity-item">
      <div class="activity-mark"><Icon name={icon} size={15} /></div>
      <div>
        <div class="activity-text">{esc(text)}</div>
        <div class="activity-time">{esc(time)}</div>
      </div>
    </div>
  );
}

/* ---------- Pages ---------- */

export function HomePage({ state, openRoom, setModal }) {
  const { rooms, templates, tasks, skills } = state;
  const completedTasks = tasks.filter((t) => t.status === 'done').length;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Your command center</div>
          <h1 class="page-title">Make the work visible.</h1>
          <div class="page-sub">Coordinate your AI teams, keep work visible in rooms, and let Foreman handle orchestration.</div>
        </div>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button class="btn" onClick={() => setModal({ kind: 'rooms' })}><Icon name="users" />Manage teams</button>
          <button class="btn primary" onClick={() => setModal({ kind: 'room' })}><Icon name="plus" />New room</button>
        </div>
      </div>
      <div class="grid-stats">
        <StatCard label="Active rooms" icon="message-square" num={rooms.length}
          sub={`${rooms.filter((r) => ['running', 'planning'].includes(r.status)).length} in progress`} />
        <StatCard label="Tasks this week" icon="check-check" num={tasks.length} sub={`${completedTasks} completed`} />
        <StatCard label="Templates" icon="layers" num={templates.length} sub="Import editable teams" />
        <StatCard label="Skills" icon="puzzle" num={skills.length} sub="Reusable capabilities" />
      </div>
      <div class="section-head">
        <div>
          <div class="section-title">Your rooms</div>
          <div class="section-note">Task-focused workspaces — each room is a session</div>
        </div>
        <button class="btn subtle" onClick={() => setModal({ kind: 'rooms' })}>View all <Icon name="arrow-up-right" size={14} /></button>
      </div>
      <div class="list">
        {rooms.length
          ? rooms.map((r) => <RoomRow key={r.id} room={r} onOpen={openRoom} />)
          : <div class="empty">No rooms yet. Create a room to define a goal and invite teams.</div>}
      </div>
      <div class="section-head">
        <div>
          <div class="section-title">Team templates</div>
          <div class="section-note">Start from a built-in team and customize it</div>
        </div>
      </div>
      <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:12px">
        {templates.map((t) => <TemplateCard key={t.id} template={t} onImport={setModal} />)}
      </div>
    </>
  );
}

export function RoomsPage({ state, openRoom, setModal }) {
  const { rooms } = state;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Shared work context</div>
          <h1 class="page-title">Rooms</h1>
          <div class="page-sub">A room is a task-focused workspace. Invite teams, chat with agents, and track work in one place.</div>
        </div>
        <button class="btn primary" onClick={() => setModal({ kind: 'room' })}><Icon name="plus" />New room</button>
      </div>
      <div class="list">
        {rooms.length
          ? rooms.map((r) => <RoomRow key={r.id} room={r} onOpen={openRoom} />)
          : <div class="empty">No rooms yet. Create one to define a goal and invite teams.</div>}
      </div>
      <div class="panel" style="margin-top:24px">
        <div class="panel-head"><h3>How rooms work</h3></div>
        <ActivityItem icon="users" text="Invite teams, not duplicated agents." time="The same team can be used across multiple rooms." />
        <ActivityItem icon="git-branch" text="Each task gets an execution boundary." time="Use a worktree or sandbox to keep parallel changes isolated." />
        <ActivityItem icon="shield-check" text="Human approval stays configurable." time="Require approval for plans, file changes, commands, or merges." />
      </div>
    </>
  );
}

export function RoomDetailHost({ room, state, notify, setModal, runTask, refresh, setActiveRoomId }) {
  const [tab, setTab] = useState('chat');
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [activeAgent, setActiveAgent] = useState(room.mainAgent || 'Atlas');
  const [inviteOpen, setInviteOpen] = useState(false);
  const [invited, setInvited] = useState(room.teamIds || []);

  useEffect(() => { setInvited(room.teamIds || []); }, [room.id]);
  useEffect(() => { setActiveAgent(room.mainAgent || 'Atlas'); }, [room.id]);

  const { teams: allTeams, tasks, settings } = state;
  const teams = allTeams.filter((t) => room.teamIds.includes(t.id));
  const roomTasks = tasks.filter((t) => t.roomId === room.id);

  const send = async (e) => {
    e.preventDefault();
    const content = draft.trim();
    if (!content) return;
    setSending(true);
    try {
      await api(`/api/rooms/${encodeURIComponent(room.id)}/messages`, {
        method: 'POST',
        body: JSON.stringify({ role: 'user', content }),
      });
      setDraft('');
      await refresh();
    } catch (error) {
      notify(error.message);
    } finally {
      setSending(false);
    }
  };

  const done = roomTasks.filter((t) => t.status === 'done').length;
  const total = roomTasks.length;
  const pct = total ? Math.round((done / total) * 100) : 0;

  const toggleInvite = (id) => setInvited((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));

  return (
    <>
      <div class="workspace">
        <div class="workspace-main">
          <div class="room-toolbar">
            <button class="icon-btn" onClick={() => setActiveRoomId('')} aria-label="Back to rooms"><Icon name="arrow-left" /></button>
            <div style="min-width:0;flex:1">
              <div class="room-title-large">{esc(room.name)}</div>
              <div class="room-desc-small">{esc(room.goal || 'No goal provided.')}</div>
            </div>
            <button class="btn subtle" onClick={() => setInviteOpen(true)}><Icon name="user-plus" /><span class="hide-sm">Invite</span></button>
          </div>

          <div class="room-tabs">
            <button class={`room-tab ${tab === 'chat' ? 'active' : ''}`} onClick={() => setTab('chat')}><Icon name="message-square" />Chat</button>
            <button class={`room-tab ${tab === 'board' ? 'active' : ''}`} onClick={() => setTab('board')}>
              <Icon name="kanban" />Kanban{total ? <span class="tag" style="margin-left:2px">{total}</span> : null}
            </button>
            <button class={`room-tab ${tab === 'plan' ? 'active' : ''}`} onClick={() => setTab('plan')}><Icon name="git-branch" />Plan</button>
            <button class={`room-tab ${tab === 'changes' ? 'active' : ''}`} onClick={() => setTab('changes')}><Icon name="git-diff" />Changes</button>
          </div>

          {tab === 'chat' && (
            <div class="room-view active">
              <div class="chat-scroll">
                {(room.messages || []).length
                  ? room.messages.map((m) => (
                    <div class={`msg ${m.role === 'user' ? 'user' : ''}`}>
                      <div class={`msg-avatar ${m.role === 'user' ? 'user' : 'main-agent'}`}>
                        {m.role === 'user' ? 'ME' : avatarInitials(room.mainAgent || 'Foreman')}
                      </div>
                      <div class="msg-content">
                        <div class="msg-meta">
                          <strong>{esc(m.role === 'user' ? 'You' : (room.mainAgent || 'Foreman'))}</strong>
                          <span>{m.role === 'user' ? 'You' : `${room.mainAgent || 'Foreman'} · Main agent`} · {timeAgo(m.createdAt)}</span>
                        </div>
                        <div class={`bubble ${m.role === 'user' ? 'user-bubble' : ''}`}>{esc(m.content)}</div>
                      </div>
                    </div>
                  ))
                  : (
                    <div class="empty">
                      No messages yet. Describe the goal below to start this room's conversation.
                    </div>
                  )}
              </div>
              <form class="chat-compose" onSubmit={send}>
                <div class="compose-tools">
                  <span class="compose-hint">Message the room · work is saved locally</span>
                  <button class="btn subtle" type="button" onClick={() => setModal({ kind: 'attach' })}>
                    <Icon name="paperclip" size={14} />Attach context
                  </button>
                </div>
                <div class="compose-box">
                  <textarea
                    rows="2"
                    placeholder="Describe a task for this room…"
                    value={draft}
                    onInput={(e) => setDraft(e.currentTarget.value)}
                  />
                  <button class="send-btn" type="submit" disabled={sending} aria-label="Send message"><Icon name="arrow-up" /></button>
                </div>
                <div class="compose-selects">
                  <button class="mini-select" type="button" onClick={() => setModal({ kind: 'route', agents: teams, current: activeAgent, onPick: setActiveAgent })}>
                    <Icon name="bot" size={13} /><span>{esc(activeAgent)}</span><Icon name="chevron-down" size={13} />
                  </button>
                  <button class="mini-select" type="button" onClick={() => setModal({ kind: 'effort', current: settings.defaultEffort })}>
                    <Icon name="gauge" size={13} /><span>Effort: {esc(settings.defaultEffort)}</span><Icon name="chevron-down" size={13} />
                  </button>
                  <button class="mini-select" type="button" onClick={() => setModal({ kind: 'approval', required: settings.requireRunApproval })}>
                    <Icon name="shield-check" size={13} /><span>Approval {settings.requireRunApproval ? 'on' : 'off'}</span>
                  </button>
                </div>
              </form>
            </div>
          )}

          {tab === 'board' && (
            <div class="room-view active">
              <Board roomId={room.id} tasks={roomTasks} runTask={runTask} setModal={setModal} />
            </div>
          )}

          {tab === 'plan' && (
            <div class="room-view active">
              <div style="padding:24px;max-width:900px;width:100%;margin:0 auto">
                <div class="section-title">Task graph</div>
                <div class="section-note" style="margin-top:5px">
                  Dependencies determine when workers can be spawned. Parallel work is scheduled only when safe.
                </div>
                <div class="panel" style="margin-top:18px;padding:0;overflow:hidden">
                  {roomTasks.length
                    ? roomTasks.map((t, i) => (
                      <div class="plan-list-task">
                        <div class={`task-number ${t.status === 'done' ? 'done' : ''}`}>
                          {t.status === 'done' ? <Icon name="check" size={13} /> : i + 1}
                        </div>
                        <div class="room-main">
                          <div class="room-title">{esc(t.title)}</div>
                          <div class="room-sub">{esc(t.description || 'No description.')}</div>
                          <div class="room-foot">
                            <span class="tag">{esc(t.priority)}</span>
                            <span class="meta-text">{esc(t.status)} · effort {esc(t.effort)}</span>
                          </div>
                        </div>
                      </div>
                    ))
                    : <div class="empty" style="border:0;margin:14px">No tasks yet. Add tasks from the Kanban tab.</div>}
                </div>
                <div class="panel" style="margin-top:14px">
                  <div class="section-title">Scheduling rationale</div>
                  <p class="section-note" style="line-height:1.8;margin-top:8px">
                    The main agent handles coordination and final synthesis. Independent read-only analysis can run in
                    parallel. Code changes wait for findings to reduce conflicts and unnecessary work.
                  </p>
                </div>
              </div>
            </div>
          )}

          {tab === 'changes' && (
            <div class="room-view active">
              <div style="padding:24px;max-width:900px;width:100%;margin:0 auto">
                <div class="section-title">Proposed changes</div>
                <div class="section-note" style="margin-top:5px">
                  A review surface for agent output. Local room state only until a run produces artifacts.
                </div>
                <div class="changes-list" style="margin-top:16px">
                  <div class="changes-row">
                    <div class="changes-symbol"><Icon name="file-code" /></div>
                    <div class="changes-main">
                      <div class="changes-title">No change sets yet</div>
                      <div class="changes-sub">Run a task to produce a reviewable change.</div>
                    </div>
                    <span class="tag orange">Pending</span>
                  </div>
                  <div class="changes-row">
                    <div class="changes-symbol"><Icon name="shield-check" /></div>
                    <div class="changes-main">
                      <div class="changes-title">Review gate</div>
                      <div class="changes-sub">{settings.requireRunApproval ? 'Changes require human approval before merge.' : 'Approval is currently disabled by policy.'}</div>
                    </div>
                    <span class={`tag ${settings.requireRunApproval ? 'green' : 'muted'}`}>{settings.requireRunApproval ? 'Enabled' : 'Off'}</span>
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>

        <aside class="right-rail">
          <div class="rail-section">
            <div class="rail-title">
              Room team
              <button class="dots" onClick={() => setInviteOpen(true)} aria-label="Invite team"><Icon name="plus" size={14} /></button>
            </div>
            {teams.length
              ? teams.map((t) => (
                <div class="agent-line">
                  <div class="agent-avatar green" style="border-radius:6px">{avatarInitials(t.mainAgent)}</div>
                  <div class="agent-info">
                    <div class="agent-name">{esc(t.mainAgent)}</div>
                    <div class="agent-role">{esc(t.name)}</div>
                  </div>
                  <span class="agent-status running"><span class="status-dot" />Ready</span>
                </div>
              ))
              : <div class="muted" style="font-size:11px;padding:4px 0">No teams invited yet.</div>}
            <button class="btn subtle" style="width:100%;margin-top:8px" onClick={() => setInviteOpen(true)}>
              <Icon name="users" size={14} />Manage participants
            </button>
          </div>

          <div class="rail-section">
            <div class="rail-title">Task progress <span style="margin-left:auto;font-weight:400;color:var(--muted)">{done} / {total}</span></div>
            <div class="progress-track"><div class="progress-fill" style={`width:${pct}%`} /></div>
            <div style="margin-top:9px">
              {roomTasks.filter((t) => t.status !== 'done').slice(0, 4).map((t) => (
                <div class="rail-task">
                  <span style="color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{esc(t.title)}</span>
                  <span class={`tag ${t.status === 'in_progress' ? 'blue' : ''}`} style="margin-left:auto">{esc(t.status)}</span>
                </div>
              ))}
              {!roomTasks.length && <div class="muted" style="font-size:11px">No tasks in this room.</div>}
            </div>
            <button class="btn subtle" style="width:100%;margin-top:7px" onClick={() => setTab('board')}>
              Open Kanban <Icon name="arrow-up-right" size={14} />
            </button>
          </div>

          <div class="rail-section">
            <div class="rail-title">Live activity</div>
            <div class="activity-feed">
              <b>Room</b> created<br />
              <span style="color:var(--muted)">{timeAgo(room.createdAt)}</span><br /><br />
              <b>Policy</b> {settings.effortMode} effort · max {settings.maxConcurrentRuns} run{settings.maxConcurrentRuns === 1 ? '' : 's'}<br />
              <span style="color:var(--muted)">Guardrails from Settings</span>
            </div>
          </div>

          <div class="rail-section">
            <div class="rail-title">Execution policy</div>
            <div style="display:flex;flex-wrap:wrap;gap:6px">
              <span class="policy-chip green">{esc(settings.effortMode)} effort</span>
              <span class="policy-chip blue">Max {settings.maxConcurrentRuns} runs</span>
              <span class="policy-chip blue">Timeout {settings.maxExecutionSeconds}s</span>
              {settings.requireRunApproval
                ? <span class="policy-chip orange">Approval required</span>
                : <span class="policy-chip green">Auto-run</span>}
            </div>
          </div>
        </aside>
      </div>

      {inviteOpen && (
        <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) setInviteOpen(false); }}>
          <div class="modal">
            <div class="modal-head">
              <h3>Invite teams to room</h3>
              <button class="icon-btn" onClick={() => setInviteOpen(false)} aria-label="Close"><Icon name="x" /></button>
            </div>
            <div class="modal-body">
              <div class="field">
                <label>Select teams to invite</label>
                <div class="choice-grid">
                  {allTeams.length
                    ? allTeams.map((t) => (
                      <label class={`choice ${invited.includes(t.id) ? 'selected' : ''}`}>
                        <input type="checkbox" checked={invited.includes(t.id)} onChange={() => toggleInvite(t.id)} />
                        <span><b>{esc(t.name)}</b><br /><small>{esc(t.purpose)}</small></span>
                      </label>
                    ))
                    : <span class="muted">No teams yet. Import a template first.</span>}
                </div>
              </div>
              <small>Inviting a team does not spawn every agent. The scheduler starts only the workers needed for assigned tasks.</small>
            </div>
            <div class="modal-foot">
              <button class="btn" onClick={() => setInviteOpen(false)}>Cancel</button>
              <button class="btn primary" onClick={() => { notify('Room participants updated'); setInviteOpen(false); }}>Save participants</button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

const BOARD_COLUMNS = [
  ['todo', 'To do'],
  ['blocked', 'Blocked'],
  ['in_progress', 'In progress'],
  ['review', 'Review'],
  ['done', 'Done'],
];

function Board({ roomId, tasks, runTask, setModal }) {
  return (
    <div class="board">
      {BOARD_COLUMNS.map(([key, title]) => {
        const columnTasks = tasks.filter((t) => t.status === key);
        return (
          <div class="board-col">
            <div class="board-col-head">{title}<span>{columnTasks.length}</span></div>
            <div class="board-tasks">
              {columnTasks.length
                ? columnTasks.map((t) => (
                  <article class="board-task">
                    <div class="board-task-title">{esc(t.title)}</div>
                    <div class="board-task-desc">{esc(t.description || 'No description.')}</div>
                    <div class="board-task-foot">
                      <span class={`tag ${['high', 'urgent'].includes(t.priority) ? 'orange' : ''}`}>{esc(t.priority)}</span>
                      <span class="meta-text">effort {esc(t.effort)}</span>
                    </div>
                    <div style="display:flex;gap:5px;margin-top:9px">
                      <button class="btn small" style="flex:1" onClick={() => setModal({ kind: 'task', roomId, task: t })}>Edit</button>
                      {t.status === 'todo' && (
                        <button class="btn primary small" style="flex:1" onClick={(e) => runTask(t.id, e.currentTarget)}>Run</button>
                      )}
                      {t.status === 'review' && (
                        <span class="tag blue" style="flex:1;justify-content:center">Awaiting review</span>
                      )}
                    </div>
                  </article>
                ))
                : <div class="muted" style="font-size:11px;padding:4px">No tasks</div>}
              <button class="board-add" onClick={() => setModal({ kind: 'task', roomId, task: null })}>+ Add task</button>
            </div>
          </div>
        );
      })}
    </div>
  );
}

export function TeamsPage({ state, setModal }) {
  const { teams, templates } = state;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Reusable agent teams</div>
          <h1 class="page-title">Teams</h1>
          <div class="page-sub">Build a team once, invite it into any room. Pick a main agent, write instructions, and attach the agents it needs.</div>
        </div>
        <button class="btn primary" onClick={() => setModal({ kind: 'rooms' })}><Icon name="users" />Manage teams</button>
      </div>

      <div class="section-head" style="margin-top:0">
        <div>
          <div class="section-title">Your teams</div>
          <div class="section-note">Imported and editable copies — click a team to inspect or edit it</div>
        </div>
        <span class="tag">{teams.length}</span>
      </div>
      <div class="list">
        {teams.length
          ? teams.map((t) => (
            <article
              class="room-row clickable"
              role="button"
              tabindex="0"
              onClick={() => setModal({ kind: 'team-detail', team: t })}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setModal({ kind: 'team-detail', team: t }); } }}
            >
              <div class="agent-avatar blue" style="border-radius:6px">{avatarInitials(t.mainAgent)}</div>
              <div class="room-main">
                <div class="room-title">
                  {esc(t.name)}
                  <span class="tag">{esc(t.templateId)} template</span>
                </div>
                <div class="room-sub">{esc(t.purpose)}</div>
                <div class="room-foot">
                  <span class="meta-text">Main: {esc(t.mainAgent)} · {(t.agents || []).length} agents</span>
                </div>
                <div class="chip-row">
                  {(t.agents || []).map((a) => <span class="chip">{esc(a.name)}</span>)}
                </div>
              </div>
              <span class="row-action"><Icon name="pencil" size={14} />Edit</span>
            </article>
          ))
          : <div class="empty">No teams imported yet. Import a template below to create an editable copy.</div>}
      </div>

      <div class="section-head">
        <div>
          <div class="section-title">Team templates</div>
          <div class="section-note">Start from a built-in crew and customize it</div>
        </div>
      </div>
      <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(280px,1fr));">
        {templates.map((t) => <TemplateCard key={t.id} template={t} onImport={setModal} />)}
      </div>
    </>
  );
}

export function AgentsPage({ state, setModal }) {
  const agents = state.agents || [];
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Agent library</div>
          <h1 class="page-title">Agent profiles</h1>
          <div class="page-sub">
            A reusable profile defines a role, model, markdown instructions, allowed tools, and skills.
            Teams and rooms reference these profiles by name.
          </div>
        </div>
        <button class="btn primary" onClick={() => setModal({ kind: 'agent', agent: null })}><Icon name="plus" />Create agent</button>
      </div>
      <div class="list">
        {agents.length
          ? agents.map((a) => (
            <article
              class="room-row clickable"
              role="button"
              tabindex="0"
              onClick={() => setModal({ kind: 'agent', agent: a })}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setModal({ kind: 'agent', agent: a }); } }}
            >
              <div class={avatarClass(a.requireReview ? 'orange' : 'green')} style="border-radius:6px">{avatarInitials(a.name)}</div>
              <div class="room-main">
                <div class="room-title">
                  {esc(a.name)}
                  {a.requireReview ? <span class="tag orange">Requires review</span> : <span class="tag">Profile</span>}
                </div>
                <div class="room-sub">{esc(a.role)}</div>
                <div class="room-foot">
                  <span class="meta-text">{(a.tools || []).length} tools · {(a.skills || []).length} skills · {esc(a.model || 'auto model')}</span>
                </div>
              </div>
              <span class="row-action"><Icon name="pencil" size={14} />Edit</span>
            </article>
          ))
          : <div class="empty">No agent profiles yet. Create one, or import a team template to seed the library.</div>}
      </div>
    </>
  );
}

export function SkillsPage({ state, setModal, deleteSkill, saveSkill }) {
  const { skills } = state;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Reusable capabilities</div>
          <h1 class="page-title">Skills</h1>
          <div class="page-sub">
            Skills are markdown bundles with optional sub-folders and files. Active skills are injected into an
            agent's context; passive skills stay as reference material.
          </div>
        </div>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button class="btn" onClick={() => setModal({ kind: 'skill-import' })}><Icon name="upload" />Import folder or .md</button>
          <button class="btn primary" onClick={() => setModal({ kind: 'skill-custom' })}><Icon name="plus" />New skill</button>
        </div>
      </div>
      <div class="list">
        {skills.length
          ? skills.map((s) => (
            <article
              class="skill-row"
              role="button"
              tabindex="0"
              onClick={() => setModal({ kind: 'skill', skill: s })}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setModal({ kind: 'skill', skill: s }); } }}
            >
              <div class="team-icon"><Icon name={s.mode === 'active' ? 'check-circle' : 'archive'} size={18} /></div>
              <div class="skill-body">
                <div class="skill-title">
                  {esc(s.name)}
                  <span class="tag">v{esc(s.version)}</span>
                  <span class={`tag ${s.mode === 'active' ? 'green' : 'muted'}`}>{s.mode === 'active' ? 'Active' : 'Passive'}</span>
                </div>
                <div class="skill-desc">{esc(s.description || 'No description.')}</div>
                <div class="skill-meta">
                  {(s.files || []).length} file{(s.files || []).length === 1 ? '' : 's'}
                  {(s.files || []).length ? ` · ${(s.files || []).map((f) => f.path).slice(0, 3).join(', ')}` : ''}
                </div>
              </div>
              <div class="skill-actions" onClick={(e) => e.stopPropagation()}>
                <button
                  class={`btn small ${s.mode === 'active' ? '' : 'primary'}`}
                  onClick={() => saveSkill({ ...s, mode: s.mode === 'active' ? 'passive' : 'active' },
                    `Skill switched to ${s.mode === 'active' ? 'passive' : 'active'}`)}
                >
                  {s.mode === 'active' ? 'Make passive' : 'Make active'}
                </button>
                <button class="icon-btn" aria-label="Delete skill"
                  onClick={() => { if (confirm(`Delete skill "${s.name}"?`)) deleteSkill(s.id); }}>
                  <Icon name="trash" size={15} />
                </button>
              </div>
            </article>
          ))
          : <div class="empty">No skills yet. Import a folder of markdown or create a skill.</div>}
      </div>
    </>
  );
}


export function RunsPage({ state, cancelRun }) {
  const { runs, tasks } = state;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Execution history</div>
          <h1 class="page-title">Runs & activity</h1>
          <div class="page-sub">See what ran, which agent was spawned, what it changed, and whether human approval is needed.</div>
        </div>
      </div>
      <div class="list">
        {runs.length
          ? runs.map((run) => {
            const task = tasks.find((t) => t.id === run.taskId);
            const tone = run.status === 'running' ? 'orange' : run.status === 'completed' ? 'green' : 'blue';
            return (
              <article class="room-row">
                <div class={avatarClass(tone)} style="border-radius:6px"><Icon name="activity" size={14} /></div>
                <div class="room-main">
                  <div class="room-title">{esc(run.status)} · {esc(run.model)}</div>
                  <div class="room-sub">{esc(task ? task.title : 'Unknown task')} · {esc(run.result || run.error || 'Run executing…')}</div>
                  <div class="room-foot">
                    <span class="tag">{esc(run.provider)}</span>
                    <span class="meta-text">Started {new Date(run.startedAt).toLocaleString()} · {esc(run.tokens || 0)} tokens · ${Number(run.costUsd || 0).toFixed(4)}</span>
                  </div>
                </div>
                {run.status === 'running' && (
                  <button class="btn small" onClick={() => cancelRun(run.id)}>Cancel</button>
                )}
              </article>
            );
          })
          : <div class="empty">No runs have been executed in this workspace.</div>}
      </div>
    </>
  );
}

export function SettingsPage({ state, notify, refresh }) {
  const settings = state.settings;
  const [policy, setPolicy] = useState(settings);
  const [provider, setProvider] = useState({ id: '', name: '', protocol: 'openai-compatible', baseUrl: '', models: '', defaultModel: '', enabled: true });
  const [apiKey, setApiKey] = useState('');

  useEffect(() => { setPolicy(settings); }, [JSON.stringify(settings)]);

  const savePolicy = async (e) => {
    e.preventDefault();
    try {
      await api('/api/settings', { method: 'PUT', body: JSON.stringify(policy) });
      await refresh();
      notify('Policy saved');
    } catch (err) { notify(err.message); }
  };
  const setField = (f, v, numeric = false) => setPolicy((prev) => ({ ...prev, [f]: numeric ? Number(v) : v }));

  const providerConfig = () => ({
    id: provider.id.trim(), name: provider.name.trim(), protocol: provider.protocol,
    baseUrl: provider.baseUrl.trim(), models: provider.models.split(',').map((s) => s.trim()).filter(Boolean),
    defaultModel: provider.defaultModel.trim(), enabled: provider.enabled,
  });
  const saveProvider = async (e) => {
    e.preventDefault();
    try {
      await api('/api/providers', { method: 'POST', body: JSON.stringify({ config: providerConfig(), apiKey }) });
      setApiKey('');
      await refresh();
      notify('Provider saved');
    } catch (err) { notify(err.message); }
  };
  const testProvider = async () => {
    try {
      await api('/api/providers/test', { method: 'POST', body: JSON.stringify({ config: providerConfig(), apiKey }) });
      notify(`Connected: ${provider.defaultModel}`);
    } catch (err) { notify('Connection failed: ' + err.message); }
  };

  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Workspace controls</div>
          <h1 class="page-title">Settings</h1>
          <div class="page-sub">Set guardrails for how aggressively Foreman plans, spawns workers, and applies changes.</div>
        </div>
        <button class="btn primary" onClick={savePolicy}><Icon name="check" />Save policy</button>
      </div>

      <div class="panel">
        <h3 style="margin-top:0">Providers and models</h3>
        <p class="section-note" style="margin-top:0">
          Configure OpenAI-compatible or Anthropic endpoints. Connection tests make a real request.
        </p>
        <form onSubmit={saveProvider}>
          <div class="settings-grid">
            <div class="field">
              <label>Provider ID</label>
              <input required maxlength="80" placeholder="openai-compatible" value={provider.id}
                onInput={(e) => setProvider({ ...provider, id: e.currentTarget.value })} />
            </div>
            <div class="field">
              <label>Display name</label>
              <input required maxlength="120" placeholder="Local gateway" value={provider.name}
                onInput={(e) => setProvider({ ...provider, name: e.currentTarget.value })} />
            </div>
            <div class="field">
              <label>Protocol</label>
              <select value={provider.protocol} onChange={(e) => setProvider({ ...provider, protocol: e.currentTarget.value })}>
                <option value="openai-compatible">OpenAI-compatible</option>
                <option value="anthropic">Anthropic</option>
              </select>
            </div>
            <div class="field">
              <label>Base URL</label>
              <input required placeholder="http://127.0.0.1:11434/v1" value={provider.baseUrl}
                onInput={(e) => setProvider({ ...provider, baseUrl: e.currentTarget.value })} />
            </div>
            <div class="field">
              <label>Model IDs (comma-separated)</label>
              <input required placeholder="model-id" value={provider.models}
                onInput={(e) => setProvider({ ...provider, models: e.currentTarget.value })} />
            </div>
            <div class="field">
              <label>Default model</label>
              <input required placeholder="model-id" value={provider.defaultModel}
                onInput={(e) => setProvider({ ...provider, defaultModel: e.currentTarget.value })} />
            </div>
          </div>
          <div class="field">
            <label>API key (optional for local models)</label>
            <input type="password" autocomplete="new-password" placeholder="Leave blank to retain existing" value={apiKey}
              onInput={(e) => setApiKey(e.currentTarget.value)} />
          </div>
          <label class="check">
            <input type="checkbox" checked={provider.enabled}
              onChange={(e) => setProvider({ ...provider, enabled: e.currentTarget.checked })} />
            <span>Enabled for task execution</span>
          </label>
          <div style="display:flex;gap:8px;margin-top:12px">
            <button class="btn" type="button" onClick={testProvider}>Test connection</button>
            <button class="btn primary" type="submit">Save provider</button>
          </div>
        </form>
        <div class="list" style="margin-top:16px">
          {state.providers.length
            ? state.providers.map((p) => (
              <article class="room-row">
                <div class={avatarClass(p.enabled ? 'green' : '')} style="border-radius:6px">{esc(p.name).slice(0, 2).toUpperCase()}</div>
                <div class="room-main">
                  <div class="room-title">{esc(p.name)}<span class={`tag ${p.enabled ? 'green' : ''}`}>{p.enabled ? 'Enabled' : 'Disabled'}</span></div>
                  <div class="room-sub">{esc(p.protocol)} · {esc(p.baseUrl)}</div>
                  <div class="room-foot"><span class="meta-text">{(p.models || []).join(', ')} · default {esc(p.defaultModel)} · {p.hasApiKey ? 'credential saved' : 'no credential'}</span></div>
                </div>
              </article>
            ))
            : <div class="muted" style="font-size:11px">No providers configured.</div>}
        </div>
      </div>

      <div class="panel" style="margin-top:12px">
        <h3 style="margin-top:0">Execution policy</h3>
        <p class="section-note" style="margin-top:0">
          Run concurrency is 1–8. Project commands are only enabled when the policy allows them.
        </p>
        <form onSubmit={savePolicy}>
          <div class="settings-grid">
            <div class="field">
              <label>Theme</label>
              <select value={policy.theme} onChange={(e) => setField('theme', e.currentTarget.value)}>
                <option value="dark">Dark</option>
                <option value="light">Light</option>
                <option value="system">System</option>
              </select>
            </div>
            <div class="field">
              <label>Effort mode</label>
              <select value={policy.effortMode} onChange={(e) => setField('effortMode', e.currentTarget.value)}>
                <option value="automatic">Automatic</option>
                <option value="manual">Manual</option>
              </select>
            </div>
            <div class="field">
              <label>Default effort</label>
              <select value={policy.defaultEffort} onChange={(e) => setField('defaultEffort', e.currentTarget.value)}>
                <option value="low">Low</option>
                <option value="medium">Medium</option>
                <option value="high">High</option>
              </select>
            </div>
            <div class="field">
              <label>Max concurrent runs</label>
              <input type="number" min="1" max="8" value={policy.maxConcurrentRuns}
                onInput={(e) => setField('maxConcurrentRuns', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Max workers per task</label>
              <input type="number" min="1" max="8" value={policy.maxWorkersPerTask}
                onInput={(e) => setField('maxWorkersPerTask', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Max delegation depth</label>
              <input type="number" min="0" max="5" value={policy.maxDelegationDepth}
                onInput={(e) => setField('maxDelegationDepth', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Max retries</label>
              <input type="number" min="0" max="5" value={policy.maxRetries}
                onInput={(e) => setField('maxRetries', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Run timeout (seconds)</label>
              <input type="number" min="10" max="3600" value={policy.maxExecutionSeconds}
                onInput={(e) => setField('maxExecutionSeconds', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Budget (USD, 0 = unlimited)</label>
              <input type="number" min="0" step="0.01" value={policy.budgetUsd}
                onInput={(e) => setField('budgetUsd', e.currentTarget.value, true)} />
            </div>
          </div>
          <label class="check" style="margin-top:8px">
            <input type="checkbox" checked={policy.requireRunApproval}
              onChange={(e) => setField('requireRunApproval', e.currentTarget.checked)} />
            <span>Require explicit approval before runs</span>
          </label>
          <label class="check" style="margin-top:8px">
            <input type="checkbox" checked={policy.allowProjectCommands}
              onChange={(e) => setField('allowProjectCommands', e.currentTarget.checked)} />
            <span>Allow project commands (terminal tool in the engine)</span>
          </label>
          <button class="btn primary" style="margin-top:12px">Save policy</button>
        </form>
        <p class="section-note" style="margin-top:14px">
          Workspace data: <code>.foreman/workspace.json</code> · API keys: <code>.foreman/credentials.json</code> (0600).
        </p>
      </div>
    </>
  );
}
