import { useEffect, useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';
import { Markdown } from './markdown.jsx';
import { DiffView, parseDiff } from './diff.jsx';

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

function roomRuntimeStatus(room, runs) {
  const roomRuns = (runs || []).filter((run) => run.roomId === room.id);
  const active = roomRuns.find((run) => run.status === 'running');
  if (active) return 'running';
  const last = roomRuns[0];
  if (last && ['failed', 'timed_out', 'cancelled'].includes(last.status)) return 'paused';
  return 'idle';
}

function RoomRow({ room, onOpen, runs }) {
  const statusTag = {
    planning: 'muted', running: 'blue', review: 'orange', cancelled: 'red', completed: 'green',
  }[room.status] || 'muted';
  const teamCount = (room.teamIds || []).length;
  const runtime = roomRuntimeStatus(room, runs);
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
          <span class={`runtime-dot ${runtime}`} />
          <span class={`runtime-label ${runtime}`}>
            {runtime === 'running' ? 'Running' : runtime === 'paused' ? 'Paused' : 'Idle'}
          </span>
          <span class={`tag ${statusTag}`}>{esc(room.status)}</span>
          <span class="meta-text">{teamCount} team{teamCount === 1 ? '' : 's'} · {timeAgo(room.createdAt)}</span>
        </div>
      </div>
      <span class="row-action"><Icon name="chevron-right" size={14} />Open</span>
    </article>
  );
}

function TemplateCard({ template, onUse }) {
  return (
    <article class="team-card template-card">
      <div class="team-card-top">
        <div class="team-icon"><Icon name="users" size={19} /></div>
        <span class="tag">{template.agents.length} agents</span>
      </div>
      <div class="team-name">{esc(template.name)}</div>
      <div class="team-desc">{esc(template.purpose)}</div>
      <div class="meta-text" style="margin-bottom:10px">
        Main agent: <b>{esc(template.mainAgent)}</b>
      </div>
      <div class="chip-row" style="margin-bottom:14px">
        {(template.agents || []).map((a) => <span class="chip">{esc(a.name)}</span>)}
      </div>
      <button class="btn" style="width:100%;margin-top:auto" onClick={onUse}>
        <Icon name="plus" size={14} />Use this template
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

export function HomePage({ state, openRoom, setModal, onTeamCreate, navigate }) {
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
          <button class="btn" onClick={() => navigate('teams')}><Icon name="users" />Manage teams</button>
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
        <button class="btn subtle" onClick={() => navigate('rooms')}>View all <Icon name="arrow-up-right" size={14} /></button>
      </div>
      <div class="list">
        {rooms.length
          ? rooms.map((r) => <RoomRow key={r.id} room={r} onOpen={openRoom} runs={state.runs} />)
          : <div class="empty">No rooms yet. Create a room to define a goal and invite teams.</div>}
      </div>
      <div class="section-head">
        <div>
          <div class="section-title">Team templates</div>
          <div class="section-note">Start from a built-in crew and customize it</div>
        </div>
      </div>
      <div class="template-grid">
        {templates.map((t) => <TemplateCard key={t.id} template={t} onUse={onTeamCreate} />)}
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
          ? rooms.map((r) => <RoomRow key={r.id} room={r} onOpen={openRoom} runs={state.runs} />)
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

export function RoomDetailHost({ room, state, notify, setModal, runTask, refresh, onBack, cancelRun }) {
  const [tab, setTab] = useState('chat');
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [activeAgent, setActiveAgent] = useState(room.mainAgent || 'Atlas');
  const [inviteOpen, setInviteOpen] = useState(false);
  const [invited, setInvited] = useState(room.teamIds || []);
  const [answerDraft, setAnswerDraft] = useState({});

  useEffect(() => { setInvited(room.teamIds || []); }, [room.id, room.teamIds.join(',')]);
  useEffect(() => { setActiveAgent(room.mainAgent || 'Atlas'); }, [room.id]);

  const { teams: allTeams, tasks, skills, runs, settings } = state;
  const teams = allTeams.filter((t) => room.teamIds.includes(t.id));
  const roomTasks = tasks.filter((t) => t.roomId === room.id);
  const roomRuns = (runs || []).filter((r) => r.roomId === room.id);
  const activeRun = roomRuns.find((r) => r.status === 'running');
  const lastRun = roomRuns[0];
  const runtime = activeRun ? 'running'
    : lastRun && ['failed', 'timed_out', 'cancelled'].includes(lastRun.status) ? 'paused'
      : 'idle';
  const pendingQuestions = (room.questions || []).filter((q) => !q.answer);

  const send = async (e) => {
    e.preventDefault();
    const content = draft.trim();
    if (!content) return;
    setSending(true);
    try {
      const routed = activeAgent && activeAgent !== room.mainAgent ? `@${activeAgent} ${content}` : content;
      await api(`/api/rooms/${encodeURIComponent(room.id)}/messages`, {
        method: 'POST',
        body: JSON.stringify({ role: 'user', content: routed }),
      });
      setDraft('');
      await refresh();
    } catch (error) {
      notify(error.message);
    } finally {
      setSending(false);
    }
  };

  const answerQuestion = async (question, value) => {
    const answer = String(value || '').trim();
    if (!answer) return;
    try {
      await api(`/api/rooms/${encodeURIComponent(room.id)}/questions/${encodeURIComponent(question.id)}`, {
        method: 'PUT',
        body: JSON.stringify({ answer }),
      });
      setAnswerDraft((prev) => ({ ...prev, [question.id]: '' }));
      await refresh();
      notify('Answer sent to the room');
    } catch (error) {
      notify(error.message);
    }
  };

  const saveTeams = async (ids) => {
    try {
      await api(`/api/rooms/${encodeURIComponent(room.id)}/teams`, {
        method: 'PUT',
        body: JSON.stringify({ teamIds: ids }),
      });
      await refresh();
      notify('Room participants updated');
      return true;
    } catch (error) {
      notify(error.message);
      return false;
    }
  };

  const kickTeam = async (teamId) => {
    const name = allTeams.find((t) => t.id === teamId)?.name || 'team';
    if (!confirm(`Remove ${name} from this room?`)) return;
    await saveTeams((room.teamIds || []).filter((id) => id !== teamId));
  };

  const done = roomTasks.filter((t) => t.status === 'done').length;
  const total = roomTasks.length;
  const pct = total ? Math.round((done / total) * 100) : 0;
  const diffText = roomRuns
    .map((run) => `${run.result || ''}\n${(run.events || []).map((e) => e.message).join('\n')}`)
    .join('\n');
  const diffFiles = parseDiff(diffText);

  const toggleInvite = (id) => setInvited((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));

  return (
    <>
      <div class="workspace">
        <div class="workspace-main">
          <div class="room-toolbar">
            <button class="icon-btn" onClick={onBack} aria-label="Back to rooms"><Icon name="arrow-left" /></button>
            <div style="min-width:0;flex:1">
              <div class="room-title-large">{esc(room.name)}</div>
              <div class="room-desc-small">{esc(room.goal || 'No goal provided.')}</div>
            </div>
            <div class={`runtime-pill ${runtime}`}>
              <span class={`runtime-dot ${runtime}`} />
              {runtime === 'running' ? 'Running' : runtime === 'paused' ? 'Paused' : 'Idle'}
            </div>
            {activeRun ? (
              <button class="btn" onClick={() => cancelRun(activeRun.id)}>
                <Icon name="pause" size={14} />Pause run
              </button>
            ) : lastRun && ['failed', 'timed_out', 'cancelled'].includes(lastRun.status) ? (
              <button class="btn primary" onClick={() => runTask(lastRun.taskId)}>
                <Icon name="activity" size={14} />Continue
              </button>
            ) : null}
            <button class="btn subtle" onClick={() => setInviteOpen(true)}>
              <Icon name="user-plus" size={14} /><span class="hide-sm">Invite</span>
            </button>
          </div>

          <div class="room-tabs">
            <button class={`room-tab ${tab === 'chat' ? 'active' : ''}`} onClick={() => setTab('chat')}>
              <Icon name="message-square" />Chat
            </button>
            <button class={`room-tab ${tab === 'board' ? 'active' : ''}`} onClick={() => setTab('board')}>
              <Icon name="kanban" />Kanban{total ? <span class="tag" style="margin-left:2px">{total}</span> : null}
            </button>
            <button class={`room-tab ${tab === 'plan' ? 'active' : ''}`} onClick={() => setTab('plan')}>
              <Icon name="git-branch" />Plan
            </button>
            <button class={`room-tab ${tab === 'changes' ? 'active' : ''}`} onClick={() => setTab('changes')}>
              <Icon name="git-diff" />Changes{diffFiles.length ? <span class="tag" style="margin-left:2px">{diffFiles.length}</span> : null}
            </button>
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
                          <span>{m.role === 'user' ? 'You' : 'Main agent'} · {timeAgo(m.createdAt)}</span>
                        </div>
                        <div class={`bubble ${m.role === 'user' ? 'user-bubble' : ''}`}>
                          {m.role === 'user' ? esc(m.content) : <Markdown text={m.content} />}
                        </div>
                      </div>
                    </div>
                  ))
                  : (
                    <div class="empty">
                      No messages yet. Describe the goal below to start this room's conversation.
                    </div>
                  )}

                {pendingQuestions.map((question) => (
                  <div class="question-card">
                    <div class="question-head">
                      <Icon name="sparkles" size={15} />
                      Agent needs a decision
                    </div>
                    <div class="question-prompt">{esc(question.prompt)}</div>
                    {question.options.length ? (
                      <div class="question-options">
                        {question.options.map((option) => (
                          <button class="btn" onClick={() => answerQuestion(question, option)}>{esc(option)}</button>
                        ))}
                      </div>
                    ) : null}
                    <form class="question-custom" onSubmit={(e) => {
                      e.preventDefault();
                      answerQuestion(question, answerDraft[question.id]);
                    }}>
                      <input
                        placeholder="Or type your own answer…"
                        value={answerDraft[question.id] || ''}
                        onInput={(e) => setAnswerDraft((prev) => ({ ...prev, [question.id]: e.currentTarget.value }))}
                      />
                      <button class="btn primary" type="submit"><Icon name="arrow-up" size={14} />Send</button>
                    </form>
                  </div>
                ))}
              </div>

              <form class="chat-compose" onSubmit={send}>
                <div class="compose-shell">
                  <textarea
                    rows="2"
                    placeholder="Describe a task or ask for a decision…"
                    value={draft}
                    onInput={(e) => setDraft(e.currentTarget.value)}
                  />
                  <div class="compose-toolbar">
                    <div class="compose-selects">
                      {teams.length ? (
                        <select class="mini-select" value={activeAgent}
                          onChange={(e) => setActiveAgent(e.currentTarget.value)}>
                          {teams.map((t) => <option value={esc(t.mainAgent)}>{esc(t.mainAgent)} · {esc(t.name)}</option>)}
                        </select>
                      ) : (
                        <span class="mini-select muted">No team invited</span>
                      )}
                      <button class="mini-select" type="button" onClick={() => setModal({ kind: 'attach' })}>
                        <Icon name="paperclip" size={13} />Attach
                      </button>
                      <button class="mini-select" type="button" onClick={() => setModal({ kind: 'approval', required: settings.requireRunApproval })}>
                        <Icon name="shield-check" size={13} />Approval {settings.requireRunApproval ? 'on' : 'off'}
                      </button>
                    </div>
                    <button class="send-btn" type="submit" disabled={sending} aria-label="Send message">
                      <Icon name="arrow-up" />
                    </button>
                  </div>
                </div>
                <div class="compose-foot">Messages are saved locally. A task only runs when you start it.</div>
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
              <div class="tab-scroll">
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
              <div class="tab-scroll">
                <div class="section-title">Code changes</div>
                <div class="section-note" style="margin-top:5px">
                  Unified diffs from this room's runs. Expanded by default; collapse a file to focus.
                </div>
                {diffFiles.length
                  ? <div style="margin-top:16px"><DiffView text={diffText} /></div>
                  : (
                    <div class="empty" style="margin-top:16px">
                      No diff yet. When an agent returns a unified diff, it appears here with red/green lines.
                    </div>
                  )}
                <div class="changes-meta">
                  {roomRuns.length ? `${roomRuns.length} run${roomRuns.length === 1 ? '' : 's'} in this room` : 'No runs yet'}
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
                    <div class="agent-name">{esc(t.name)}</div>
                    <div class="agent-role">Main: {esc(t.mainAgent)}</div>
                  </div>
                  <button class="icon-btn" title="Remove team" onClick={() => kickTeam(t.id)}>
                    <Icon name="x" size={13} />
                  </button>
                </div>
              ))
              : <div class="muted" style="font-size:11px;padding:4px 0">No teams invited yet.</div>}
            <button class="btn subtle" style="width:100%;margin-top:8px" onClick={() => setInviteOpen(true)}>
              <Icon name="users" size={14} />Manage participants
            </button>
          </div>

          <div class="rail-section">
            <div class="rail-title">Run status</div>
            <div class={`rail-status ${runtime}`}>
              <span class={`runtime-dot ${runtime}`} />
              <span>{runtime === 'running' ? 'Agent running' : runtime === 'paused' ? 'Paused after a failure' : 'No active run'}</span>
            </div>
            {activeRun ? (
              <button class="btn" style="width:100%;margin-top:8px" onClick={() => cancelRun(activeRun.id)}>
                <Icon name="pause" size={13} />Pause run
              </button>
            ) : lastRun && ['failed', 'timed_out', 'cancelled'].includes(lastRun.status) ? (
              <button class="btn primary" style="width:100%;margin-top:8px" onClick={() => runTask(lastRun.taskId)}>
                <Icon name="activity" size={13} />Continue where it stopped
              </button>
            ) : null}
            {lastRun && (
              <div class="section-note" style="margin-top:8px">
                Last: {esc(lastRun.status)} · {lastRun.tokens || 0} tokens
              </div>
            )}
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
              {roomRuns.slice(0, 3).map((run) => (
                <div class="activity-row">
                  <b>{esc(run.status)}</b>
                  <span>{timeAgo(run.startedAt)}</span>
                </div>
              ))}
              {!roomRuns.length && <span style="color:var(--muted)">No runs yet.</span>}
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
              <h3>Manage room participants</h3>
              <button class="icon-btn" onClick={() => setInviteOpen(false)} aria-label="Close"><Icon name="x" /></button>
            </div>
            <div class="modal-body">
              <div class="field">
                <label>Teams in this room</label>
                <div class="team-picker">
                  {allTeams.length
                    ? allTeams.map((t) => (
                      <label class={`team-pick ${invited.includes(t.id) ? 'on' : ''}`}>
                        <input type="checkbox" checked={invited.includes(t.id)} onChange={() => toggleInvite(t.id)} />
                        <span class="team-pick-body">
                          <span class="team-pick-name">{esc(t.name)}</span>
                          <span class="team-pick-desc">Main: {esc(t.mainAgent)} · {esc(t.purpose)}</span>
                        </span>
                      </label>
                    ))
                    : <div class="empty" style="padding:16px">No teams yet. Create one on the Teams page.</div>}
                </div>
              </div>
              <div class="notice">Inviting a team does not start agents. Tasks decide what runs.</div>
            </div>
            <div class="modal-foot">
              <button class="btn" onClick={() => setInviteOpen(false)}>Cancel</button>
              <button class="btn primary" onClick={async () => {
                const ok = await saveTeams(invited);
                if (ok) setInviteOpen(false);
              }}>Save participants</button>
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

export function TeamsPage({ state, openTeam, onTeamCreate }) {
  const { teams, templates } = state;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Reusable agent teams</div>
          <h1 class="page-title">Teams</h1>
          <div class="page-sub">Build a team once, invite it into any room. Open a team to edit its title, instructions, agents, and main agent.</div>
        </div>
        <button class="btn primary" onClick={onTeamCreate}><Icon name="plus" />Create team</button>
      </div>

      <div class="section-head" style="margin-top:0">
        <div>
          <div class="section-title">Your teams</div>
          <div class="section-note">Click a team to open its detail page</div>
        </div>
        <span class="tag">{teams.length}</span>
      </div>
      <div class="list">
        {teams.length
          ? teams.map((t) => (
            <article class="room-row clickable" role="button" tabindex="0"
              onClick={() => openTeam(t.id)}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openTeam(t.id); } }}>
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
              <span class="row-action"><Icon name="chevron-right" size={14} />Open</span>
            </article>
          ))
          : <div class="empty">No teams yet. Create one from a template to get started.</div>}
      </div>

      <div class="section-head">
        <div>
          <div class="section-title">Team templates</div>
          <div class="section-note">Starting points for a new team</div>
        </div>
      </div>
      <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(280px,1fr));">
        {templates.map((t) => (
          <article class="team-card">
            <div class="team-card-top"><div class="team-icon"><Icon name="users" size={19} /></div></div>
            <div class="team-name">{esc(t.name)}</div>
            <div class="team-desc">{esc(t.purpose)}</div>
            <div class="meta-text" style="margin-bottom:9px">
              Main agent: <b>{esc(t.mainAgent)}</b> · {(t.agents || []).length} roles
            </div>
            <div style="display:flex;gap:5px;flex-wrap:wrap">
              {(t.agents || []).map((a) => (
                <span class="tag">{esc(a.name)}</span>
              ))}
            </div>
            <button class="btn" style="margin-top:14px;width:100%" onClick={onTeamCreate}>
              <Icon name="plus" size={14} />Use this template
            </button>
          </article>
        ))}
      </div>
    </>
  );
}

export function AgentsPage({ state, openAgent }) {
  const agents = state.agents || [];
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Agent library</div>
          <h1 class="page-title">Agent profiles</h1>
          <div class="page-sub">
            A reusable profile defines a role, markdown instructions, model, allowed tools, and skills.
            Teams and rooms reference these profiles by name.
          </div>
        </div>
        <button class="btn primary" onClick={() => openAgent('')}><Icon name="plus" />Create agent</button>
      </div>
      <div class="list">
        {agents.length
          ? agents.map((a) => (
            <article class="room-row clickable" role="button" tabindex="0"
              onClick={() => openAgent(a.id)}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openAgent(a.id); } }}>
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
              <span class="row-action"><Icon name="chevron-right" size={14} />Open</span>
            </article>
          ))
          : <div class="empty">No agent profiles yet. Create one to get started.</div>}
      </div>
    </>
  );
}

export function SkillsPage({ state, setModal, openSkill }) {
  const { skills } = state;
  const active = skills.filter((s) => s.mode === 'active').length;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Reusable capabilities</div>
          <h1 class="page-title">Skills</h1>
          <div class="page-sub">
            Skills are markdown bundles organized in folders. Active skills are injected into an agent's
            context; passive skills stay as reference.
          </div>
        </div>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button class="btn" onClick={() => setModal({ kind: 'skill-import' })}><Icon name="upload" />Import folder</button>
          <button class="btn primary" onClick={() => openSkill('')}><Icon name="plus" />New skill</button>
        </div>
      </div>

      <div class="grid-stats" style="grid-template-columns:repeat(3,minmax(0,1fr))">
        <StatCard label="Skills" icon="folder" num={skills.length} sub="Markdown bundles" />
        <StatCard label="Active" icon="check-circle" num={active} sub="Injected into context" />
        <StatCard label="Passive" icon="archive" num={skills.length - active} sub="Reference only" />
      </div>

      <div class="list">
        {skills.length
          ? skills.map((s) => (
            <article class="skill-row folder-row" role="button" tabindex="0"
              onClick={() => openSkill(s.id)}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openSkill(s.id); } }}>
              <div class="folder-icon"><Icon name="folder" size={20} /></div>
              <div class="skill-body">
                <div class="skill-title">
                  {esc(s.name)}
                  <span class="tag">v{esc(s.version)}</span>
                  <span class={`tag ${s.mode === 'active' ? 'green' : 'muted'}`}>{s.mode === 'active' ? 'Active' : 'Passive'}</span>
                </div>
                <div class="skill-desc">{esc(s.description || 'No description.')}</div>
                <div class="skill-meta">{(s.files || []).length} file{(s.files || []).length === 1 ? '' : 's'} · {(s.files || []).map((f) => f.path).slice(0, 3).join(', ')}</div>
              </div>
              <span class="row-action"><Icon name="chevron-right" size={14} />Open</span>
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

const PROVIDER_PRESETS = [
  {
    id: 'tokenharbor', name: 'TokenHarbor', protocol: 'openai-compatible',
    baseUrl: 'https://tokenharbor.ai/v1',
    models: ['deepseek-v4.1-flash:free', 'claude-sonnet-5', 'gpt-6-astra'],
    note: 'One key for many models. OpenAI-compatible gateway.',
  },
  {
    id: 'opencode-zen', name: 'OpenCode Zen', protocol: 'openai-compatible',
    baseUrl: 'https://opencode.ai/zen/v1',
    models: ['deepseek-v4.1-flash', 'gpt-5.3-codex', 'kimi-k2.6'],
    note: 'Pay-as-you-go model gateway from the OpenCode team.',
  },
  {
    id: 'opencode-go', name: 'OpenCode Go', protocol: 'openai-compatible',
    baseUrl: 'https://opencode.ai/zen/go/v1',
    models: ['kimi-k2.6', 'minimax-m2.7', 'deepseek-v4.1-flash'],
    note: 'Subscription access to open coding models.',
  },
  {
    id: 'deepseek', name: 'DeepSeek', protocol: 'openai-compatible',
    baseUrl: 'https://api.deepseek.com',
    models: ['deepseek-flash', 'deepseek-v4-pro'],
    note: 'Native API, OpenAI- and Anthropic-compatible.',
  },
  {
    id: 'minimax', name: 'MiniMax', protocol: 'openai-compatible',
    baseUrl: 'https://api.minimax.io/v1',
    models: ['MiniMax-M3.1-Flash', 'MiniMax-M3'],
    note: '1M-context coding models, OpenAI-compatible.',
  },
  {
    id: 'anthropic', name: 'Anthropic (Claude)', protocol: 'anthropic',
    baseUrl: 'https://api.anthropic.com',
    models: ['claude-sonnet-5', 'claude-opus-5', 'claude-haiku-4-5'],
    note: 'Claude Messages API.',
  },
  {
    id: 'openai', name: 'OpenAI (GPT)', protocol: 'openai-compatible',
    baseUrl: 'https://api.openai.com/v1',
    models: ['gpt-5.5', 'gpt-5.5-pro', 'gpt-5-codex'],
    note: 'OpenAI Chat Completions.',
  },
  {
    id: 'custom', name: 'Custom provider', protocol: 'openai-compatible',
    baseUrl: '', models: [],
    note: 'Any OpenAI- or Anthropic-compatible endpoint.',
  },
];

const slugify = (value) => String(value || '')
  .toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 80);

const providerInitials = (name) => String(name || '?')
  .split(/[\s()-]+/).filter(Boolean).slice(0, 2).map((part) => part[0]).join('').toUpperCase();

function PresetCard({ preset, configured, onOpen }) {
  return (
    <article class="preset-card" role="button" tabindex="0" onClick={onOpen}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen(); } }}>
      <div class="preset-top">
        <span class="preset-mark">{providerInitials(preset.name)}</span>
        <span class="preset-name">{esc(preset.name)}</span>
        {configured ? <span class="tag green">Configured</span> : <span class="tag">Available</span>}
      </div>
      <div class="preset-note">{esc(preset.note)}</div>
      <div class="preset-url">{esc(preset.baseUrl || 'Custom endpoint')}</div>
      <div class="chip-row">
        {preset.models.slice(0, 3).map((model) => <span class="chip">{esc(model)}</span>)}
        {preset.models.length > 3 ? <span class="chip">+{preset.models.length - 3}</span> : null}
      </div>
    </article>
  );
}

function ProviderModal({ initial, isNew, onClose, notify, refresh }) {
  const ordered = (initial?.models || []).length
    ? [initial.defaultModel, ...(initial.models || []).filter((m) => m !== initial.defaultModel)]
    : [];
  const [form, setForm] = useState({
    id: initial?.id || '',
    name: initial?.name || '',
    protocol: initial?.protocol || 'openai-compatible',
    baseUrl: initial?.baseUrl || '',
    enabled: initial?.enabled ?? true,
  });
  const [modelsText, setModelsText] = useState(ordered.join('\n'));
  const [apiKey, setApiKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [discovering, setDiscovering] = useState(false);
  const [test, setTest] = useState({ state: 'idle', message: '' });

  const set = (field, value) => setForm((prev) => ({ ...prev, [field]: value }));
  const models = modelsText.split(/[\n,]+/).map((m) => m.trim()).filter(Boolean);
  const defaultModel = models[0] || '';

  const config = () => ({
    id: (form.id || slugify(form.name)).trim(),
    name: form.name.trim(),
    protocol: form.protocol,
    baseUrl: form.baseUrl.trim(),
    models,
    defaultModel,
    enabled: form.enabled,
  });

  const runTest = async () => {
    if (!models.length) { setTest({ state: 'error', message: 'Add at least one model first' }); return; }
    setTest({ state: 'running', message: 'Testing connection…' });
    try {
      const result = await api('/api/providers/test', {
        method: 'POST',
        body: JSON.stringify({ config: config(), apiKey }),
      });
      setTest({ state: 'ok', message: `Connected · ${result.model}` });
    } catch (error) {
      setTest({ state: 'error', message: error.message });
    }
  };

  const discover = async () => {
    setDiscovering(true);
    setTest({ state: 'running', message: 'Fetching model list…' });
    try {
      const result = await api('/api/providers/discover', {
        method: 'POST',
        body: JSON.stringify({ config: config(), apiKey }),
      });
      const found = result.models || [];
      if (!found.length) {
        setTest({ state: 'error', message: 'The provider returned no models' });
      } else {
        setModelsText(found.join('\n'));
        setTest({ state: 'ok', message: `Found ${found.length} models · first is the default` });
      }
    } catch (error) {
      setTest({ state: 'error', message: error.message });
    } finally {
      setDiscovering(false);
    }
  };

  const submit = async (event) => {
    event.preventDefault();
    if (!config().id) { notify('Add a provider name or id'); return; }
    if (!models.length) { notify('Add at least one model'); return; }
    setBusy(true);
    try {
      await api('/api/providers', { method: 'POST', body: JSON.stringify({ config: config(), apiKey }) });
      await refresh();
      notify(isNew ? 'Provider added' : 'Provider saved');
      onClose();
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <form class="modal modal-lg" onSubmit={submit}>
        <header class="modal-head">
          <h3>{isNew ? 'Add provider' : `Edit ${form.name || 'provider'}`}</h3>
          <button type="button" class="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </header>
        <div class="modal-body">
          <div class="form-grid">
            <div class="field">
              <label>Display name</label>
              <input required maxlength="120" placeholder="e.g. TokenHarbor" value={form.name}
                onInput={(e) => set('name', e.currentTarget.value)} />
            </div>
            <div class="field">
              <label>Provider ID</label>
              <input required maxlength="80" placeholder="tokenharbor" value={form.id} readOnly={!isNew}
                onInput={(e) => set('id', e.currentTarget.value)} />
              {!isNew ? <div class="md-hint">The ID is fixed after creation; it keys the saved credential.</div> : null}
            </div>
            <div class="field">
              <label>Protocol</label>
              <select value={form.protocol} onChange={(e) => set('protocol', e.currentTarget.value)}>
                <option value="openai-compatible">OpenAI-compatible</option>
                <option value="anthropic">Anthropic Messages</option>
              </select>
            </div>
            <div class="field">
              <label>Base URL</label>
              <input required maxlength="2048" placeholder="https://api.example.com/v1" value={form.baseUrl}
                onInput={(e) => set('baseUrl', e.currentTarget.value)} />
            </div>
          </div>

          <div class="field">
            <label>API key {initial?.hasApiKey ? '(saved — leave blank to keep)' : ''}</label>
            <input type="password" autocomplete="new-password" placeholder="sk-…"
              value={apiKey} onInput={(e) => setApiKey(e.currentTarget.value)} />
          </div>

          <div class="field">
            <div class="field-label-row">
              <label>Models (one per line · first is the default)</label>
              <button type="button" class="btn small" disabled={discovering} onClick={discover}>
                <Icon name="refresh" size={13} />{discovering ? 'Fetching…' : 'Discover models'}
              </button>
            </div>
            <textarea class="code-area" style="min-height:130px" placeholder={'deepseek-v4.1-flash:free\nclaude-sonnet-5'}
              value={modelsText} onInput={(e) => setModelsText(e.currentTarget.value)} />
            <div class="md-hint">
              {defaultModel ? `Default model: ${defaultModel}` : 'Add models to choose the default.'}
            </div>
          </div>

          <label class="check">
            <input type="checkbox" checked={form.enabled} onChange={(e) => set('enabled', e.currentTarget.checked)} />
            <span>Enabled for task execution</span>
          </label>

          {test.state !== 'idle' && (
            <div class={`test-result ${test.state === 'ok' ? 'ok' : test.state === 'error' ? 'error' : ''}`}>
              <Icon name={test.state === 'ok' ? 'check-circle' : test.state === 'error' ? 'x' : 'refresh'} size={14} />
              <span>{esc(test.message)}</span>
            </div>
          )}
        </div>
        <footer class="modal-foot">
          <button type="button" class="btn" onClick={runTest} disabled={busy || test.state === 'running'}>
            <Icon name="activity" size={14} />Test connection
          </button>
          <span style="flex:1" />
          <button type="button" class="btn" onClick={onClose}>Cancel</button>
          <button type="submit" class="btn primary" disabled={busy}>{isNew ? 'Add provider' : 'Save provider'}</button>
        </footer>
      </form>
    </div>
  );
}

function ProvidersTab({ state, onPreset, onEdit, onDelete, onToggle }) {
  const configured = new Set((state.providers || []).map((p) => p.id));
  return (
    <div class="settings-stack">
      <div class="settings-card">
        <div class="settings-card-head">
          <div>
            <h3>Connect a provider</h3>
            <p>Pick a preset or add a custom endpoint. The first model in the list becomes the default.</p>
          </div>
        </div>
        <div class="preset-grid">
          {PROVIDER_PRESETS.map((preset) => (
            <PresetCard key={preset.id} preset={preset} configured={configured.has(preset.id)} onOpen={() => onPreset(preset)} />
          ))}
        </div>
      </div>

      <div class="settings-card">
        <div class="settings-card-head">
          <div>
            <h3>Configured providers</h3>
            <p>Runs use the agent's provider when set, otherwise the first enabled provider.</p>
          </div>
          <span class="tag">{(state.providers || []).length}</span>
        </div>
        <div class="list">
          {(state.providers || []).length
            ? state.providers.map((provider) => (
              <article class="provider-row">
                <span class="preset-mark">{providerInitials(provider.name)}</span>
                <div class="provider-main">
                  <div class="provider-name">
                    {esc(provider.name)}
                    <span class="tag">{esc(provider.id)}</span>
                    {provider.enabled ? <span class="tag green">Enabled</span> : <span class="tag muted">Disabled</span>}
                    {provider.hasApiKey ? <span class="tag blue">Key saved</span> : <span class="tag orange">No key</span>}
                  </div>
                  <div class="provider-url">{esc(provider.protocol)} · {esc(provider.baseUrl)}</div>
                  <div class="chip-row">
                    {(provider.models || []).map((model, index) => (
                      <span class={`chip ${index === 0 ? 'chip-default' : ''}`}>
                        {esc(model)}{index === 0 ? ' · default' : ''}
                      </span>
                    ))}
                  </div>
                </div>
                <div class="provider-actions">
                  <button class={`toggle ${provider.enabled ? 'on' : ''}`} aria-label="Toggle provider"
                    onClick={() => onToggle(provider)} />
                  <button class="btn small" onClick={() => onEdit(provider)}><Icon name="pencil" size={13} />Edit</button>
                  <button class="icon-btn" aria-label="Delete provider" onClick={() => onDelete(provider)}>
                    <Icon name="trash" size={14} />
                  </button>
                </div>
              </article>
            ))
            : <div class="empty">No providers yet. Configure one above to enable runs.</div>}
        </div>
      </div>
    </div>
  );
}

export function SettingsPage({ state, notify, refresh }) {
  const settings = state.settings;
  const [tab, setTab] = useState('providers');
  const [policy, setPolicy] = useState(settings);
  const [providerModal, setProviderModal] = useState(null);

  useEffect(() => { setPolicy(settings); }, [JSON.stringify(settings)]);

  const setField = (field, value, numeric = false) => setPolicy((prev) => ({
    ...prev,
    [field]: numeric ? Number(value) : value,
  }));

  const savePolicy = async () => {
    try {
      await api('/api/settings', { method: 'PUT', body: JSON.stringify(policy) });
      await refresh();
      notify('Settings saved');
    } catch (error) {
      notify(error.message);
    }
  };

  const openPreset = (preset) => {
    const existing = (state.providers || []).find((p) => p.id === preset.id);
    setProviderModal(existing
      ? { initial: existing, isNew: false }
      : { initial: preset, isNew: true });
  };

  const toggleProvider = async (provider) => {
    try {
      await api('/api/providers', {
        method: 'POST',
        body: JSON.stringify({
          config: {
            id: provider.id, name: provider.name, protocol: provider.protocol,
            baseUrl: provider.baseUrl, models: provider.models, defaultModel: provider.defaultModel,
            enabled: !provider.enabled,
          },
          apiKey: '',
        }),
      });
      await refresh();
      notify(`${provider.name} ${provider.enabled ? 'disabled' : 'enabled'}`);
    } catch (error) {
      notify(error.message);
    }
  };

  const deleteProvider = async (provider) => {
    if (!confirm(`Delete provider "${provider.name}"? Agents using it will fall back to the workspace default.`)) return;
    try {
      await api(`/api/providers/${encodeURIComponent(provider.id)}`, { method: 'DELETE' });
      await refresh();
      notify('Provider deleted');
    } catch (error) {
      notify(error.message);
    }
  };

  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Workspace configuration</div>
          <h1 class="page-title">Settings</h1>
          <div class="page-sub">Connect model providers, then set the guardrails Foreman uses for planning and execution.</div>
        </div>
        {tab === 'providers'
          ? <button class="btn primary" onClick={() => setProviderModal({ initial: null, isNew: true })}><Icon name="plus" />Add provider</button>
          : <button class="btn primary" onClick={savePolicy}><Icon name="save" />Save changes</button>}
      </div>

      <div class="settings-tabs">
        <button class={`settings-tab ${tab === 'providers' ? 'active' : ''}`} onClick={() => setTab('providers')}>
          <Icon name="bot" size={15} />Providers
          <span class="tag">{(state.providers || []).length}</span>
        </button>
        <button class={`settings-tab ${tab === 'execution' ? 'active' : ''}`} onClick={() => setTab('execution')}>
          <Icon name="gauge" size={15} />Execution policy
        </button>
        <button class={`settings-tab ${tab === 'appearance' ? 'active' : ''}`} onClick={() => setTab('appearance')}>
          <Icon name="layers" size={15} />Appearance
        </button>
      </div>

      {tab === 'providers' && (
        <ProvidersTab
          state={state}
          onPreset={openPreset}
          onEdit={(provider) => setProviderModal({ initial: provider, isNew: false })}
          onDelete={deleteProvider}
          onToggle={toggleProvider}
        />
      )}

      {tab === 'execution' && (
        <div class="settings-stack">
          <div class="settings-card">
            <div class="settings-card-head">
              <div>
                <h3>Run limits</h3>
                <p>Bound how much work a single run can start. The server enforces these values.</p>
              </div>
            </div>
            <div class="settings-grid">
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
          </div>

          <div class="settings-card">
            <div class="settings-card-head">
              <div>
                <h3>Approvals &amp; safety</h3>
                <p>Human checkpoints and the terminal tool are opt-in.</p>
              </div>
            </div>
            <label class="check">
              <input type="checkbox" checked={policy.requireRunApproval}
                onChange={(e) => setField('requireRunApproval', e.currentTarget.checked)} />
              <span>Require explicit approval before runs</span>
            </label>
            <label class="check">
              <input type="checkbox" checked={policy.allowProjectCommands}
                onChange={(e) => setField('allowProjectCommands', e.currentTarget.checked)} />
              <span>Allow project commands (terminal tool in the engine)</span>
            </label>
          </div>
        </div>
      )}

      {tab === 'appearance' && (
        <div class="settings-stack">
          <div class="settings-card">
            <div class="settings-card-head">
              <div>
                <h3>Appearance</h3>
                <p>Theme applies to the dashboard immediately after saving.</p>
              </div>
            </div>
            <div class="field" style="max-width:320px">
              <label>Theme</label>
              <select value={policy.theme} onChange={(e) => setField('theme', e.currentTarget.value)}>
                <option value="dark">Dark</option>
                <option value="light">Light</option>
                <option value="system">System</option>
              </select>
            </div>
          </div>
          <div class="settings-card">
            <div class="settings-card-head">
              <div>
                <h3>Workspace data</h3>
                <p>Everything is stored locally in your project folder.</p>
              </div>
            </div>
            <div class="kv-list">
              <div class="kv"><span>Workspace</span><code>{esc(state.projectPath)}/.foreman/workspace.json</code></div>
              <div class="kv"><span>Credentials</span><code>.foreman/credentials.json</code><span class="tag">0600</span></div>
              <div class="kv"><span>Schema</span><span class="tag">v{esc(state.schemaVersion || 3)}</span></div>
            </div>
          </div>
        </div>
      )}

      {providerModal && (
        <ProviderModal
          initial={providerModal.initial}
          isNew={providerModal.isNew}
          onClose={() => setProviderModal(null)}
          notify={notify}
          refresh={refresh}
        />
      )}
    </>
  );
}
