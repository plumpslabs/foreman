import { useEffect, useRef, useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';
import { Markdown } from './markdown.jsx';
import { ChangesView, parseDiff, mergeFiles } from './diff.jsx';
import { PlanGraph } from './plan.jsx';
import { AgentAvatar, findAgent } from './avatar.jsx';

const esc = (s) => String(s ?? '');
// Priority is a canonical P0..P3 scale; legacy names are mapped so old cards
// still render. Lower rank sorts first.
const PRIORITY_RANK = { p0: 0, p1: 1, p2: 2, p3: 3 };
const PRIORITY_COLOR = { p0: 'red', p1: 'orange', p2: 'blue', p3: 'muted' };
function normalizePriority(p) {
  switch (String(p ?? '').toLowerCase().trim()) {
    case 'p0': case 'urgent': case 'critical': case 'blocker': return 'p0';
    case 'p1': case 'high': return 'p1';
    case 'p2': case 'normal': case 'medium': case '': return 'p2';
    case 'p3': case 'low': return 'p3';
    default: return 'p2';
  }
}
const priorityRank = (p) => PRIORITY_RANK[normalizePriority(p)] ?? 2;
const priorityLabel = (p) => normalizePriority(p).toUpperCase();
const priorityColor = (p) => PRIORITY_COLOR[normalizePriority(p)] || '';
const fmtTokens = (n) => (Number(n) > 0 ? Number(n).toLocaleString() : '—');
const fmtMoney = (n) => {
  const v = Number(n);
  if (!v) return '—';
  return '$' + v.toFixed(2).replace(/\.00$/, '');
};
const timeAgo = (iso) => {
  const d = new Date(iso);
  const s = Math.floor((Date.now() - d.getTime()) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return d.toLocaleDateString();
};

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
  if (last && ['paused', 'failed', 'timed_out', 'cancelled'].includes(last.status)) return 'paused';
  return 'idle';
}

function RoomRow({ room, onOpen, runs, onDelete, mcpServers }) {
  const statusTag = {
    planning: 'muted', running: 'blue', review: 'orange', cancelled: 'red', completed: 'green',
  }[room.status] || 'muted';
  const teamCount = (room.teamIds || []).length;
  const runtime = roomRuntimeStatus(room, runs);
  const mcpNames = (mcpServers || []).filter((s) => (room.mcpServerIds || []).includes(s.id)).map((s) => s.name);
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
          {mcpNames.map((n) => <span class="tag blue" key={n}>MCP · {esc(n)}</span>)}
        </div>
      </div>
      {onDelete && (
        <button class="icon-btn row-delete" title="Delete room" aria-label={`Delete room ${room.name}`}
          onClick={(e) => { e.stopPropagation(); onDelete(room.id); }}
          onKeyDown={(e) => e.stopPropagation()}>
          <Icon name="trash" size={14} />
        </button>
      )}
      <span class="row-action"><Icon name="chevron-right" size={14} />Open</span>
    </article>
  );
}

function TemplateCard({ template, onUse, state }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
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
      <div class="template-agents">
        {(template.agents || []).map((a) => (
          <span class="template-agent" key={a.name} title={esc(a.role || a.name)}>
            <AgentAvatar agent={a} name={a.name} tone={a.name === template.mainAgent ? 'green' : 'blue'} size={22} radius={5} style="font-size:9px" />
            <span class="template-agent-name">{esc(a.name)}</span>
          </span>
        ))}
      </div>
      <button class="btn" style="width:100%;margin-top:auto" disabled={busy}
        onClick={async () => {
          if (!onUse) {
            setErr('Handler import belum terpasang.');
            return;
          }
          setBusy(true);
          setErr('');
          try {
            const ok = await onUse(template);
            if (!ok) setErr('Import failed — see the notification.');
          } catch (error) {
            setErr(error.message || 'Import failed.');
          } finally {
            setBusy(false);
          }
        }}>
        {busy ? <span class="btn-spin"><Icon name="refresh" size={14} /></span> : <Icon name="plus" size={14} />}
        {busy ? 'Mengimpor…' : 'Use this template'}
      </button>
      {err ? <div class="template-err" style="color:var(--red);font-size:11px;margin-top:6px">{esc(err)}</div> : null}
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

export function HomePage({ state, openRoom, setModal, onTeamCreate, onUseTemplate, openTeamCreate, importTeamTemplate, navigate }) {
  const { rooms, templates, tasks, skills } = state;
  const completedTasks = tasks.filter((t) => t.status === 'done').length;
  const useTemplate = onUseTemplate || importTeamTemplate;
  const createTeam = onTeamCreate || openTeamCreate;
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
        {templates.map((t) => <TemplateCard key={t.id} template={t} onUse={useTemplate} state={state} />)}
      </div>
    </>
  );
}

export function RoomsPage({ state, openRoom, setModal, deleteRoom }) {
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
          ? rooms.map((r) => <RoomRow key={r.id} room={r} onOpen={openRoom} runs={state.runs} onDelete={deleteRoom} mcpServers={state.mcpServers || []} />)
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
  // Right rail visibility — persisted so a collapsed workspace stays collapsed.
  const [railOpen, setRailOpen] = useState(() => {
    try { return window.localStorage.getItem('foreman.roomRail') !== '0'; } catch { return true; }
  });
  const toggleRail = () => setRailOpen((v) => {
    const next = !v;
    try { window.localStorage.setItem('foreman.roomRail', next ? '1' : '0'); } catch { /* ignore */ }
    return next;
  });
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [invited, setInvited] = useState(room.teamIds || []);
  const [answerDraft, setAnswerDraft] = useState({});
  const sendQueueRef = useRef([]);
  const sendingRef = useRef(false);
  const refreshRef = useRef(refresh);
  useEffect(() => { refreshRef.current = refresh; });
  const [mentions, setMentions] = useState([]);
  const [mentionTrigger, setMentionTrigger] = useState('@');
  const [mentionOpen, setMentionOpen] = useState(false);
  const [mentionQuery, setMentionQuery] = useState('');
  const [mentionIdx, setMentionIdx] = useState(0);

  const addMention = (item) => {
    setMentions((prev) => {
      if (prev.some((m) => m.type === item.type && m.name.toLowerCase() === item.name.toLowerCase())) return prev;
      return [...prev, item];
    });
  };
  const removeMention = (name, type) => {
    setMentions((prev) => prev.filter((m) => !(m.name.toLowerCase() === name.toLowerCase() && (!type || m.type === type))));
  };
  const clearMentions = () => setMentions([]);
  const setMentionAgent = (name) => {
    if (!name) {
      setMentions((prev) => prev.filter((m) => m.type !== 'agent'));
    } else {
      addMention({ type: 'agent', name });
    }
  };
  const mentionAgent = mentions.find((m) => m.type === 'agent')?.name || '';
  const [sdkHintDismissed, setSdkHintDismissed] = useState(false);
  const [engineBusy, setEngineBusy] = useState(false);
  const [engineReason, setEngineReason] = useState('');
  const [engineHint, setEngineHint] = useState('');
  const [engineLog, setEngineLog] = useState('');
  const [engineStatus, setEngineStatus] = useState(null);
  const [kickBlock, setKickBlock] = useState(null);

  const loadEngineStatus = async () => {
    try {
      const d = await api('/api/engine/status');
      setEngineStatus(d);
    } catch {
      setEngineStatus(null);
    }
  };
  useEffect(() => { loadEngineStatus(); }, [room.id]);

  useEffect(() => { setInvited(room.teamIds || []); }, [room.id, room.teamIds.join(',')]);

  const { teams: allTeams, tasks, skills, runs, settings, agents: allAgents = [] } = state;
  const teams = (room.teamIds || [])
    .map((id) => allTeams.find((t) => t.id === id))
    .filter(Boolean);

  const roomAgentsList = [];
  const seenAgentNames = new Set();
  const leadTeamId = teams[0]?.id;
  for (const t of teams) {
    const isLeadTeam = t.id === leadTeamId;
    if (t.mainAgent && !seenAgentNames.has(t.mainAgent)) {
      seenAgentNames.add(t.mainAgent);
      roomAgentsList.push({
        name: t.mainAgent,
        teamName: t.name,
        isLead: isLeadTeam,
        isMain: true,
        role: isLeadTeam ? 'Room lead' : `${t.name} lead`,
      });
    }
    for (const a of t.agents || []) {
      if (!seenAgentNames.has(a.name)) {
        seenAgentNames.add(a.name);
        const isAgentMain = a.name === t.mainAgent;
        roomAgentsList.push({
          name: a.name,
          teamName: t.name,
          isLead: isLeadTeam && isAgentMain,
          isMain: isAgentMain,
          role: isLeadTeam && isAgentMain
            ? 'Room lead'
            : isAgentMain
              ? `${t.name} lead`
              : (a.role || 'Specialist'),
        });
      }
    }
  }
  if (!roomAgentsList.length && allAgents.length) {
    for (const a of allAgents) {
      if (!seenAgentNames.has(a.name)) {
        seenAgentNames.add(a.name);
        roomAgentsList.push({
          name: a.name,
          teamName: 'Library',
          isMain: false,
          role: a.role || 'Agent',
        });
      }
    }
  }

  const composeAgents = roomAgentsList.map((a) => [a.name, a.teamName]);
  const allRoomTasks = tasks.filter((t) => t.roomId === room.id);
  const roomTasks = allRoomTasks.filter((t) => !t.hidden);
  const roomRuns = (runs || []).filter((r) => r.roomId === room.id);
  const activeRun = roomRuns.find((r) => r.status === 'running');
  const lastRun = roomRuns[0];
  const runtime = activeRun ? 'running'
    : lastRun && ['paused', 'failed', 'timed_out', 'cancelled'].includes(lastRun.status) ? 'paused'
      : 'idle';
  const runningRuns = roomRuns.filter((r) => r.status === 'running');
  const pausedRuns = roomRuns.filter((r) => r.status === 'paused').slice(0, 2);
  const agentNameForRun = (run) => {
    const task = allRoomTasks.find((t) => t.id === run.taskId);
    if (task && task.agentName) return task.agentName;
    const team = teams[0];
    return team ? team.mainAgent : 'Agent';
  };
  const activeRunForAgent = (agentName) => runningRuns.find((run) => agentNameForRun(run) === agentName);
  const activeAgentsCount = roomAgentsList.filter((a) => activeRunForAgent(a.name)).length;
  const pendingQuestions = (room.questions || []).filter((q) => !q.answer);

  const chatScrollRef = useRef(null);

  // Auto-scroll chat to bottom smoothly when messages, questions, or runs update
  useEffect(() => {
    if (tab === 'chat' && chatScrollRef.current) {
      chatScrollRef.current.scrollTop = chatScrollRef.current.scrollHeight;
    }
  }, [tab, room.id, (room.messages || []).length, (room.questions || []).length, runningRuns.length, runningRuns.map((r) => (r.events || []).length).join(',')]);

  // Realtime: while any chat run is active, poll so replies land in the chat
  // without manual refreshes.
  const runningCount = roomRuns.filter((r) => r.status === 'running').length;
  useEffect(() => {
    if (!runningCount) return undefined;
    const id = setInterval(() => refreshRef.current(), 1200);
    return () => clearInterval(id);
  }, [room.id, runningCount]);

  // Tick every second while agents run so the live bubble can show elapsed time
  // (makes a slow first model call visibly "alive" instead of frozen).
  const [runTick, setRunTick] = useState(() => Date.now());
  useEffect(() => {
    if (!runningCount) return undefined;
    const id = setInterval(() => setRunTick(Date.now()), 1000);
    return () => clearInterval(id);
  }, [runningCount]);
  const elapsedLabel = (startedAt) => {
    if (!startedAt) return '';
    const secs = Math.max(0, Math.round((runTick - new Date(startedAt).getTime()) / 1000));
    if (secs < 60) return `${secs}s`;
    return `${Math.floor(secs / 60)}m ${String(secs % 60).padStart(2, '0')}s`;
  };

  const mentionCandidates = (() => {
    const q = mentionQuery.toLowerCase();
    if (mentionTrigger === '#') {
      return teams
        .filter((t) => t.name.toLowerCase().includes(q) || t.id.toLowerCase().includes(q))
        .map((t) => ({ type: 'team', name: t.name, sub: `Team lead: ${t.mainAgent}` }));
    }
    const agentCands = composeAgents
      .filter(([name]) => name.toLowerCase().includes(q))
      .map(([name, teamName]) => ({ type: 'agent', name, sub: teamName }));
    const teamCands = teams
      .filter((t) => t.name.toLowerCase().includes(q))
      .map((t) => ({ type: 'team', name: t.name, sub: `Team lead: ${t.mainAgent}` }));
    return [...agentCands, ...teamCands];
  })();

  const pickMention = (cand) => {
    if (!cand) return;
    const name = Array.isArray(cand) ? cand[0] : cand.name;
    const type = Array.isArray(cand) ? 'agent' : (cand.type || 'agent');
    setDraft((d) => d.replace(/(?:^|\s)[@#][^\s]*\s*$/, ''));
    addMention({ type, name });
    setMentionOpen(false);
    setMentionQuery('');
  };

  const clearMention = () => clearMentions();

  const installEngine = async () => {
    setEngineBusy(true);
    setEngineReason('');
    setEngineHint('');
    setEngineLog('');
    try {
      const data = await api('/api/engine/install', { method: 'POST', body: '{}' });
      setEngineStatus({ installed: data && data.installed, pythonVersion: data && data.pythonVersion, pipAvailable: data && data.pipAvailable });
      if (data && data.ok && data.installed) {
        notify('OpenHands engine installed — send a message again');
        setSdkHintDismissed(true);
        await refresh();
      } else {
        setEngineReason((data && data.error) || 'Installation failed — see the log below.');
        setEngineHint((data && data.hint) || '');
        setEngineLog((data && data.output) || '');
        notify('Engine install tidak berhasil');
      }
    } catch (error) {
      setEngineReason(error.message);
      notify(error.message);
    } finally {
      setEngineBusy(false);
    }
  };

  const flushSendQueue = async () => {
    if (sendingRef.current) return;
    sendingRef.current = true;
    try {
      while (sendQueueRef.current.length) {
        const content = sendQueueRef.current.shift();
        setSending(true);
        try {
          const data = await api(`/api/rooms/${encodeURIComponent(room.id)}/messages`, {
            method: 'POST',
            body: JSON.stringify({ role: 'user', content }),
          });
          if (data && data.warning) notify(data.warning);
          await refresh();
        } catch (error) {
          notify(error.message);
        }
      }
    } finally {
      setSending(false);
      sendingRef.current = false;
    }
  };

  const send = (e) => {
    if (e) e.preventDefault();
    const content = draft.trim();
    if ((!content && !mentions.length) || sendingRef.current) return;
    const mentionPrefix = mentions
      .map((m) => (m.type === 'team' ? `#${m.name}` : `@${m.name}`))
      .filter((tag) => !content.includes(tag))
      .join(' ');
    const finalContent = mentionPrefix ? `${mentionPrefix} ${content}`.trim() : content;
    setDraft('');
    setMentions([]);
    setMentionOpen(false);
    sendQueueRef.current.push(finalContent);
    flushSendQueue();
  };

  const onComposeInput = (e) => {
    const value = e.currentTarget.value;
    setDraft(value);
    const m = value.match(/(?:^|\s)([@#])([^\s]*)\s*$/);
    if (m) {
      setMentionTrigger(m[1]);
      setMentionQuery(m[2]);
      setMentionIdx(0);
      setMentionOpen(true);
    } else {
      setMentionOpen(false);
    }
  };

  const onComposeKeyDown = (e) => {
    if (mentionOpen && mentionCandidates.length) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setMentionIdx((i) => (i + 1) % mentionCandidates.length);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setMentionIdx((i) => (i - 1 + mentionCandidates.length) % mentionCandidates.length);
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        pickMention(mentionCandidates[mentionIdx]);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        setMentionOpen(false);
        return;
      }
    }
    if (e.key === 'Backspace' && !draft && mentions.length) {
      setMentions((prev) => prev.slice(0, -1));
      return;
    }
    // Enter sends; Shift+Enter inserts a newline.
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
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
    const running = runningRuns.find((run) => {
      const task = allRoomTasks.find((x) => x.id === run.taskId);
      return task && task.teamId === teamId;
    });
    if (running) {
      setKickBlock({ teamId, teamName: name, run: running });
      return;
    }
    if (!confirm(`Remove ${name} from this room?`)) return;
    await saveTeams((room.teamIds || []).filter((id) => id !== teamId));
  };

  const done = roomTasks.filter((t) => t.status === 'done').length;
  const total = roomTasks.length;
  const pct = total ? Math.round((done / total) * 100) : 0;
  const diffText = roomRuns
    .map((run) => (run.events || []).filter((e) => e.type === 'diff').map((e) => e.message).join('\n'))
    .filter(Boolean)
    .join('\n');
  const diffFiles = mergeFiles(parseDiff(diffText));

  const roomPlan = (state.plans || []).find((p) => p.roomId === room.id);
  const roomArtifacts = (state.artifacts || []).filter((a) => a.roomId === room.id);
  const approvedPanels = (state.panels || []).filter((p) => p.roomId === room.id && p.status === 'approved');

  const toggleInvite = (id) => setInvited((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  const roomMcpNames = (state.mcpServers || []).filter((s) => (room.mcpServerIds || []).includes(s.id)).map((s) => s.name);

  return (
    <>
      <div class={`workspace ${railOpen ? '' : 'rail-collapsed'}`}>
        <div class="workspace-main">
          <div class="room-toolbar">
            <button class="icon-btn" onClick={onBack} aria-label="Back to rooms"><Icon name="arrow-left" /></button>
            <div style="min-width:0;flex:1">
              <div class="room-title-large">{esc(room.name)}</div>
              <div class="room-desc-small">{esc(room.goal || 'No goal provided.')}</div>
              {roomMcpNames.length ? (
                <div style="display:flex;gap:5px;flex-wrap:wrap;margin-top:6px">
                  {roomMcpNames.map((n) => <span class="tag blue" key={n}>MCP · {esc(n)}</span>)}
                </div>
              ) : null}
            </div>
            <div class={`runtime-pill ${runtime}`}>
              <span class={`runtime-dot ${runtime}`} />
              {runtime === 'running' ? 'Running' : runtime === 'paused' ? 'Paused' : 'Idle'}
            </div>
            {null}
            <button class="btn subtle" onClick={toggleRail} title={railOpen ? 'Hide side panel' : 'Show side panel'} aria-label="Toggle side panel">
              <Icon name="layers" size={14} /><span class="hide-sm">{railOpen ? 'Hide panel' : 'Panel'}</span>
            </button>
            <button class="btn subtle" onClick={() => setInviteOpen(true)}>
              <Icon name="user-plus" size={14} /><span class="hide-sm">Invite</span>
            </button>
            <button class="btn subtle" onClick={() => setMcpOpen(true)}>
              <Icon name="settings-2" size={14} /><span class="hide-sm">MCP</span>
            </button>
          </div>

          <div class="room-tabs">
            <button class={`room-tab ${tab === 'chat' ? 'active' : ''}`} onClick={() => setTab('chat')}>
              <Icon name="message-square" />Chat
            </button>
            <button class={`room-tab ${tab === 'board' ? 'active' : ''}`} onClick={() => setTab('board')}>
              <Icon name="kanban" />Kanban{roomTasks.length ? <span class="tag" style="margin-left:4px">{roomTasks.length}</span> : null}
            </button>
            <button class={`room-tab ${tab === 'plan' ? 'active' : ''}`} onClick={() => setTab('plan')}>
              <Icon name="git-branch" />Plan
            </button>
            <button class={`room-tab ${tab === 'changes' ? 'active' : ''}`} onClick={() => setTab('changes')}>
              <Icon name="git-diff" />Changes{diffFiles.length ? <span class="tag" style="margin-left:4px">{diffFiles.length}</span> : null}
            </button>
            <button class={`room-tab ${tab === 'artifacts' ? 'active' : ''}`} onClick={() => setTab('artifacts')}>
              <Icon name="file" />Artifacts{roomArtifacts.length ? <span class="tag" style="margin-left:4px">{roomArtifacts.length}</span> : null}
            </button>
            {approvedPanels.map((p) => (
              <button key={p.id} class={`room-tab ${tab === 'panel-' + p.id ? 'active' : ''}`} onClick={() => setTab('panel-' + p.id)}>
                <Icon name={p.icon || 'layout'} />{esc(p.title)}
              </button>
            ))}
          </div>

          {tab === 'chat' && (
            <div class="room-view active">
              <div class="chat-scroll" ref={chatScrollRef}>
                {teams.length ? (
                  <div class="invite-card">
                    <span class="invite-icon"><Icon name="users" size={15} /></span>
                    <div class="invite-body">
                      <div class="invite-title">Teams invited to this room</div>
                      <div class="invite-members">
                        {teams.map((t) => (
                          <span key={t.id} class="invite-team">
                            <b>{esc(t.name)}</b>
                            <span>{(t.agents || []).map((a) => esc(a.name)).join(', ') || 'no members'}</span>
                          </span>
                        ))}
                      </div>
                    </div>
                  </div>
                ) : null}
                {(room.messages || []).map((m) => {
                  if (m.role === 'system' || m.role === 'activity') {
                    const who = m.agentName || '';
                    const at = m.activityType || '';
                    const body = String(m.content || '');
                    const sevClass = body.includes('🛑')
                      ? 'sev-blocker'
                      : (/escalat|blocked|rework|failed/.test(at) ? 'sev-warning' : '');
                    return (
                      <div class="notice-row" key={m.id}>
                        <span class={`notice-chip ${sevClass}`}>
                          {who ? <b>{esc(who)}</b> : null}{' '}
                          {esc(m.content)}
                        </span>
                      </div>
                    );
                  }
                  const name = m.role === 'user' ? 'You' : (m.agentName || room.mainAgent || 'Main agent');
                  return (
                    <div class={`msg ${m.role === 'user' ? 'user' : ''}`} key={m.id}>
                      {m.role === 'user'
                        ? <div class="msg-avatar user">ME</div>
                        : <AgentAvatar agent={findAgent(state, name)} name={name} className="msg-avatar main-agent" />}
                      <div class="msg-content">
                        <div class="msg-meta">
                          <strong>{esc(name)}</strong>
                          <span>{m.role === 'user' ? 'You' : esc(name)} · {timeAgo(m.createdAt)}</span>
                        </div>
                        <div class={`bubble ${m.role === 'user' ? 'user-bubble' : ''}`}>
                          {m.role === 'user' ? esc(m.content) : <Markdown text={m.content} />}
                        </div>
                      </div>
                    </div>
                  );
                })}
                {!(room.messages || []).length ? (
                  <div class="empty chat-welcome">
                    <div class="chat-welcome-title">No messages yet. Describe the goal below to start this room's conversation.</div>
                    <div class="chat-welcome-actions">
                      {['Draft a plan for this room\'s goal', 'Summarize open work', 'What changed so far?'].map((s) => (
                        <button type="button" class="chip chip-add" key={s} onClick={() => setDraft(s)}>
                          <Icon name="plus" size={11} />{esc(s)}
                        </button>
                      ))}
                    </div>
                  </div>
                ) : null}

                {runningRuns.map((run) => {
                  const aname = agentNameForRun(run);
                  const events = run.events || [];
                  const statusEvents = events.filter((e) => e.type !== 'diff');
                  const compactSteps = [];
                  for (const e of statusEvents) {
                    const text = e.message || '';
                    const last = compactSteps[compactSteps.length - 1];
                    if (last && last.text === text) last.n = (last.n || 1) + 1;
                    else compactSteps.push({ text, n: 1 });
                  }
                  const lastStep = compactSteps[compactSteps.length - 1];
                  const earlierSteps = compactSteps.slice(-4, compactSteps.length - 1);
                  const lastMsg = statusEvents[statusEvents.length - 1]?.message || '';
                  const label = lastMsg.includes(' → ') ? 'Running tools…' : lastMsg.includes('engine:') ? 'Reasoning' : 'Working';
                  // The first event is always the runtime-start marker; while the model
                  // is still composing its first turn there are no step events yet.
                  const starting = compactSteps.length <= 1 && /runtime started/i.test(lastStep?.text || '');
                  const currentText = starting
                    ? 'Starting the agent…'
                    : lastStep
                      ? (lastStep.n > 1 ? `${lastStep.text} (×${lastStep.n})` : lastStep.text)
                      : `${aname} is working…`;
                  const elapsed = elapsedLabel(run.startedAt);
                  return (
                    <div class="msg live-agent-turn" key={`live-turn-${run.id}`}>
                      <AgentAvatar agent={findAgent(state, aname)} name={aname} className="msg-avatar main-agent live-pulsing-avatar" />
                      <div class="msg-content">
                        <div class="msg-meta">
                          <strong>{esc(aname)}</strong>
                          <span class="live-tag"><span class="live-dot pulsing" /> {label}</span>
                        </div>
                        <div class="bubble live-bubble">
                          <div class="live-bubble-status">
                            <span class="live-spinner" />
                            <span class="live-current-text">{esc(currentText)}</span>
                            {elapsed ? <span class="live-elapsed">{elapsed}</span> : null}
                          </div>
                          {earlierSteps.length ? (
                            <div class="live-steps-trace">
                              {earlierSteps.map((ev, idx) => (
                                <div class="live-step-item" key={idx}>
                                  <span class="live-step-dot" />
                                  <span class="live-step-msg">{esc(ev.n > 1 ? `${ev.text} (×${ev.n})` : ev.text)}</span>
                                </div>
                              ))}
                            </div>
                          ) : null}
                          <div class="live-actions-strip">
                            {cancelRun ? (
                              <button type="button" class="btn small subtle" style="color:var(--orange)" onClick={() => cancelRun(run.id)}>
                                <Icon name="x" size={11} />Stop
                              </button>
                            ) : null}
                          </div>
                        </div>
                      </div>
                    </div>
                  );
                })}

                {pendingQuestions.map((question) => (
                  <div class="question-card">
                    <div class="question-head" style="color:var(--orange)">
                      <Icon name="sparkles" size={15} />
                      Agent needs a decision
                    </div>
                    <div style="font-size:12px;color:var(--orange);margin-top:4px">
                      ⚠️ Ambiguity confirmation: Agent paused execution to avoid assumption bias.
                    </div>
                    <div class="question-prompt" style="margin-top:8px">{esc(question.prompt)}</div>
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

              {engineStatus && !engineStatus.installed && !sdkHintDismissed ? (
                <div class="sdk-hint">
                  <Icon name="info" size={15} class="icon" />
                  <div class="sdk-hint-body">
                    <b>OpenHands engine belum terpasang di Python environment.</b>
                    {!engineReason ? (
                      <>
                        <span>Klik Install — Foreman menjalankan:</span>
                        <code>python3 -m pip install openhands-sdk openhands-tools</code>
                      </>
                    ) : (
                      <>
                        <div class="sdk-hint-reason">{esc(engineReason)}</div>
                        {engineHint ? <div class="sdk-hint-hint">{esc(engineHint)}</div> : null}
                        {engineLog ? (
                          <details class="sdk-log">
                            <summary>Lihat log instalasi</summary>
                            <pre>{esc(engineLog)}</pre>
                          </details>
                        ) : null}
                      </>
                    )}
                  </div>
                  <div class="sdk-hint-actions">
                    <button type="button" class="btn small primary" disabled={engineBusy} onClick={installEngine}>
                      {engineBusy ? <span class="btn-spin"><Icon name="refresh" size={13} /></span> : <Icon name="refresh" size={13} />}
                      {engineBusy ? 'Installing…' : engineReason ? 'Retry' : 'Install engine'}
                    </button>
                    <button type="button" class="btn small" onClick={() => setSdkHintDismissed(true)}>Hide</button>
                  </div>
                </div>
              ) : null}

              {runningRuns.length ? (
                <div class="live-agents">
                  <span class="live-label"><Icon name="activity" size={12} />Working</span>
                  {runningRuns.map((run) => {
                    const lastEvent = (run.events || []).slice(-1)[0];
                    const label = lastEvent?.type === 'tool' ? 'acting'
                      : (lastEvent?.message || '').includes('engine:') ? 'reasoning'
                        : 'working';
                    const aname = agentNameForRun(run);
                    return (
                      <span class="live-agent live-on" key={run.id} title={`${esc(aname)} · ${label}`}>
                        <span class="live-dot" />
                        <span class="live-agent-name">{esc(aname)}</span>
                        <em>{label}</em>
                      </span>
                    );
                  })}
                </div>
              ) : null}

              <form class="chat-compose" onSubmit={send}>
                <div class="compose-shell">
                  <div class="compose-input-row">
                    {mentions.length > 0 && (
                      <div class="compose-mention">
                        {mentions.map((m) => (
                          <span class={`mention-badge ${m.type === 'team' ? 'team' : 'agent'}`} key={`${m.type}-${m.name}`}>
                            <Icon name={m.type === 'team' ? 'users' : 'bot'} size={11} />
                            <span>{m.type === 'team' ? `#${esc(m.name)}` : `@${esc(m.name)}`}</span>
                            <button
                              type="button"
                              class="icon-btn mention-remove"
                              title={`Hapus mention ${esc(m.name)}`}
                              onClick={() => removeMention(m.name, m.type)}
                            >
                              <Icon name="x" size={10} />
                            </button>
                          </span>
                        ))}
                      </div>
                    )}
                    <textarea
                      rows="2"
                      placeholder={mentions.length ? "Tulis prompt atau instruksi..." : teams.length > 1 ? "Ask an agent or team… (ketik @ untuk agent, # untuk team)" : "Ask an agent… (type @ for suggestions)"}
                      value={draft}
                      onInput={onComposeInput}
                      onKeyDown={onComposeKeyDown}
                    />
                  </div>

                  {mentionOpen && mentionCandidates.length ? (
                    <div class="mention-suggest">
                      {mentionCandidates.map((cand, i) => (
                        <button type="button" class={`mention-option ${i === mentionIdx ? 'on' : ''}`}
                          key={`${cand.type}-${cand.name}`}
                          onMouseDown={(e) => e.preventDefault()}
                          onClick={() => pickMention(cand)}>
                          {cand.type === 'team'
                            ? <span class="agent-avatar blue" style="border-radius:5px;width:20px;height:20px;font-size:9px"><Icon name="users" size={11} /></span>
                            : <AgentAvatar agent={findAgent(state, cand.name)} name={cand.name} tone="green" size={20} radius={5} style="font-size:9px" />}
                          <span class="mention-name">{cand.type === 'team' ? `#${esc(cand.name)}` : esc(cand.name)}</span>
                          <span class="mention-team">{esc(cand.sub || '')}</span>
                        </button>
                      ))}
                    </div>
                  ) : null}

                  <div class="compose-toolbar">
                    <div class="compose-selects">
                      <button class="mini-select" type="button" onClick={() => setModal({ kind: 'attach' })}>
                        <Icon name="paperclip" size={13} />Attach
                      </button>
                      <button class="mini-select" type="button" onClick={() => setModal({ kind: 'approval', required: settings.requireRunApproval })}>
                        <Icon name="shield-check" size={13} />Approval {settings.requireRunApproval ? 'on' : 'off'}
                      </button>
                    </div>
                    <span class="compose-route" title="Messages run the room's main agent unless you pick an agent above or type @name">
                      <Icon name="bot" size={12} />
                      {mentions.length
                        ? `Target: ${mentions.map((m) => (m.type === 'team' ? `#${m.name}` : `@${m.name}`)).join(', ')}`
                        : 'Default: main agent · @ agent · # team'}
                    </span>
                    <button class="send-btn" type="submit" disabled={sending} aria-label="Send message">
                      <Icon name="arrow-up" />
                    </button>
                  </div>
                </div>
                <div class="compose-foot">Sending runs the agent now — the reply lands back here in chat.</div>
              </form>
            </div>
          )}

          {tab === 'board' && (
            <div class="room-view active">
              <Board roomId={room.id} tasks={roomTasks} runTask={runTask} setModal={setModal} refresh={refresh} />
            </div>
          )}

          {tab === 'plan' && (
            <div class="room-view active">
              <div class="tab-scroll">
                <div style="display:flex;justify-content:space-between;align-items:center">
                  <div>
                    <div class="section-title">{roomPlan ? esc(roomPlan.title || 'Plan') : 'Task graph'}</div>
                    <div class="section-note" style="margin-top:5px">
                      Dependencies determine when workers can be spawned. Parallel work is scheduled only when safe.
                    </div>
                  </div>
                  {roomPlan && roomPlan.tier && (
                    <span class="tag blue" style="text-transform:uppercase">{esc(roomPlan.tier)} tier</span>
                  )}
                </div>

                {roomPlan && roomPlan.markdown && (
                  <div class="panel" style="margin-top:16px;padding:16px">
                    <Markdown text={roomPlan.markdown} />
                  </div>
                )}

                <div class="panel plan-graph-panel">
                  {roomTasks.length
                    ? <PlanGraph tasks={roomTasks} onOpenTask={(t) => setModal({ kind: 'task-detail', roomId: room.id, task: t })} />
                    : <div class="empty" style="border:0;margin:14px">No tasks yet. Add tasks from the Kanban tab.</div>}
                </div>
                <div class="panel" style="margin-top:14px">
                  <div class="section-title">Scheduling rationale</div>
                  <p class="section-note" style="line-height:1.8;margin-top:8px">
                    The main agent handles coordination and final synthesis. Independent read-only analysis can run in
                    parallel. Code changes wait for findings to reduce conflicts and unnecessary work. An arrow means
                    the target card waits for the source card to be done.
                  </p>
                </div>
              </div>
            </div>
          )}

          {tab === 'changes' && (
            <div class="room-view active">
              <div class="tab-scroll wide-changes">
                <div class="section-title">Code changes</div>
                <div class="section-note" style="margin-top:5px">
                  Unified diffs from this room's runs, grouped like a pull request review. Click a file in the tree to see its diff.
                </div>
                {diffFiles.length
                  ? <ChangesView text={diffText} />
                  : (
                    <div class="empty" style="margin-top:16px">
                      No diff yet. When an agent edits files, the changes appear here grouped by folder.
                    </div>
                  )}
                <div class="changes-meta">
                  {roomRuns.length ? `${roomRuns.length} run${roomRuns.length === 1 ? '' : 's'} in this room` : 'No runs yet'}
                </div>
              </div>
            </div>
          )}

          {tab === 'artifacts' && (
            <div class="room-view active">
              <div class="tab-scroll">
                <div class="section-title">Artifacts & Outputs</div>
                <div class="section-note" style="margin-top:5px">
                  Reports, result summaries, verification evidence, and generated files produced by agents.
                </div>
                <div style="display:flex;flex-direction:column;gap:14px;margin-top:16px">
                  {roomArtifacts.map((art) => (
                    <article key={art.id} class="panel" style="padding:16px">
                      <div style="display:flex;justify-content:space-between;align-items:center">
                        <div style="display:flex;align-items:center;gap:8px">
                          <Icon name="file" size={16} />
                          <h4 style="margin:0;font-size:15px">{esc(art.title)}</h4>
                        </div>
                        <span class="tag blue">{esc(art.type)}</span>
                      </div>
                      {art.summary && (
                        <div style="margin-top:10px;font-size:13px;line-height:1.6">
                          <b>Summary:</b> {esc(art.summary)}
                        </div>
                      )}
                      {art.content && (
                        <div style="margin-top:10px">
                          <Markdown text={art.content} />
                        </div>
                      )}
                      {art.path && art.path !== '[]' && (
                        <div class="meta-text" style="margin-top:10px">
                          Files: <code>{esc(art.path)}</code>
                        </div>
                      )}
                    </article>
                  ))}
                </div>
              </div>
            </div>
          )}

          {tab.startsWith('panel-') && (() => {
            const p = approvedPanels.find((x) => 'panel-' + x.id === tab);
            if (!p) return null;
            return (
              <div class="room-view active">
                <div class="tab-scroll">
                  <div class="section-title">{esc(p.title)}</div>
                  <div class="section-note" style="margin-top:5px">Custom Agent Panel ({esc(p.renderer)})</div>
                  <div class="panel" style="margin-top:16px;padding:16px">
                    {p.renderer === 'markdown' && <Markdown text={p.manifestJson || p.dataSource || 'No content.'} />}
                    {p.renderer === 'table' && (
                      <pre style="overflow:auto;font-size:12px">{esc(p.manifestJson || p.dataSource)}</pre>
                    )}
                    {p.renderer !== 'markdown' && p.renderer !== 'table' && (
                      <div>
                        <div class="tag blue">{esc(p.renderer)} view</div>
                        <pre style="margin-top:10px;overflow:auto">{esc(p.manifestJson || p.dataSource)}</pre>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            );
          })()}
        </div>

        <aside class="right-rail">
          <div class="rail-section">
            <div class="rail-title">
              Room team
              <button class="dots" onClick={() => setInviteOpen(true)} aria-label="Invite team"><Icon name="plus" size={14} /></button>
            </div>
            {teams.length
              ? teams.map((t, idx) => (
                <div class="team-rail" key={t.id}>
                  <div class="agent-line">
                    <AgentAvatar agent={(t.agents || []).find((a) => a.name === t.mainAgent) || findAgent(state, t.mainAgent)} name={t.mainAgent} tone="green" radius={6} />
                    <div class="agent-info" style="cursor:pointer" onClick={() => addMention({ type: 'team', name: t.name })} title="Click to mention this team (#team)">
                      <div class="agent-name-row">
                        <span class="agent-name">{esc(t.name)}</span>
                        {idx === 0 ? (
                          <span class="member-badge main mini-tag" title="Lead Team · Default responder for unmentioned chat">Lead</span>
                        ) : (
                          <span class="member-badge secondary-lead mini-tag" title="Specialist Team">Specialist</span>
                        )}
                      </div>
                      <div class="agent-role">Main: {esc(t.mainAgent)}</div>
                    </div>
                    {teams.length > 1 && idx !== 0 && (
                      <button class="icon-btn" title="Set as Lead Team" onClick={() => {
                        const newIds = [t.id, ...room.teamIds.filter((id) => id !== t.id)];
                        saveTeams(newIds);
                      }}>
                        <Icon name="arrow-up" size={13} />
                      </button>
                    )}
                    <button class="icon-btn" title="Remove team" onClick={() => kickTeam(t.id)}>
                      <Icon name="x" size={13} />
                    </button>
                  </div>
                </div>
              ))
              : <div class="muted" style="font-size:11px;padding:4px 0">No teams invited yet.</div>}
            <button class="btn subtle" style="width:100%;margin-top:8px" onClick={() => setInviteOpen(true)}>
              <Icon name="users" size={14} />Manage participants
            </button>
          </div>

          <div class="rail-section">
            <div class="rail-title">
              <Icon name="bot" size={13} />
              <span>Live agents</span>
              {activeAgentsCount ? (
                <span class="rail-counter active"><span class="live-dot pulsing" style="width:6px;height:6px" />{activeAgentsCount} active</span>
              ) : (
                <span class="rail-counter">{roomAgentsList.length}</span>
              )}
            </div>
            <div class="rail-agents-list compact">
              {roomAgentsList.map((agent) => {
                const run = activeRunForAgent(agent.name);
                const isRunning = Boolean(run);
                const lastEv = run ? (run.events || []).slice(-1)[0] : null;
                const action = isRunning
                  ? (lastEv?.type === 'tool' ? 'acting' : (lastEv?.message || '').includes('engine:') ? 'reasoning' : 'working')
                  : '';
                return (
                  <div
                    class={`rail-agent-row compact ${isRunning ? 'active' : ''}`}
                    key={agent.name}
                    onClick={() => {
                      addMention({ type: 'agent', name: agent.name });
                    }}
                    title={isRunning ? `@${esc(agent.name)} is ${action}` : `Click to mention @${esc(agent.name)}`}
                  >
                    <AgentAvatar agent={findAgent(state, agent.name)} name={agent.name} tone={agent.isMain ? 'green' : 'blue'} size={22} radius={5} style="font-size:9.5px" />
                    <div class="agent-info">
                      <div class="agent-name-row">
                        <span class="agent-name">{esc(agent.name)}</span>
                        {agent.isLead ? (
                          <span class="member-badge main mini-tag" title="Room Lead · Primary coordinator">main</span>
                        ) : agent.isMain ? (
                          <span class="member-badge secondary-lead mini-tag" title={`${esc(agent.teamName)} Team Lead`}>lead</span>
                        ) : null}
                      </div>
                      <div class="agent-role">{esc(agent.role ? `${agent.role} · ${agent.teamName}` : agent.teamName)}</div>
                    </div>
                    <div class="agent-status-indicator">
                      {isRunning ? (
                        <span class="active-indicator-pill">
                          <span class="live-dot pulsing" style="width:5px;height:5px" />
                          <span class="indicator-action">{action}</span>
                        </span>
                      ) : (
                        <span class="idle-dot-indicator" title="Standby" />
                      )}
                    </div>
                  </div>
                );
              })}
              {!roomAgentsList.length && (
                <div class="muted" style="font-size:11px;padding:4px 0">Invite a team to see active agents.</div>
              )}
            </div>
          </div>

          <div class="rail-section">
            <div class="rail-title">Run status</div>
            <div class={`rail-status ${runtime}`}>
              <span class={`runtime-dot ${runtime}`} />
              <span>{runtime === 'running' ? 'Agent running'
                : runtime === 'paused' && lastRun && lastRun.status === 'paused' ? 'Paused — state saved, ready to continue'
                  : runtime === 'paused' ? 'Paused after a failure'
                    : 'No active run'}</span>
            </div>
          </div>

          <div class="rail-section">
            <div class="rail-title">Task progress <span style="margin-left:auto;font-weight:400;color:var(--muted)">{total ? `${done} / ${total}` : 'no tasks'}</span></div>
            <div class="progress-track"><div class="progress-fill" style={`width:${pct}%`} /></div>
            <div style="margin-top:9px">
              {roomTasks.filter((t) => t.status !== 'done').slice(0, 4).map((t) => (
                <div class="rail-task" key={t.id}>
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

      {mcpOpen && (
        <RoomMCPModal room={room} servers={state.mcpServers || []} onClose={() => setMcpOpen(false)} notify={notify} refresh={refresh} />
      )}
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

      {kickBlock && (
        <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) setKickBlock(null); }}>
          <div class="modal">
            <div class="modal-head">
              <h3>Team still working</h3>
              <button class="icon-btn" onClick={() => setKickBlock(null)} aria-label="Close"><Icon name="x" /></button>
            </div>
            <div class="modal-body">
              <p style="font-size:13px;color:var(--text);line-height:1.6;margin:0">
                Team <b>{esc(kickBlock.teamName)}</b> still has work running in this room
                (<b>{esc(agentNameForRun(kickBlock.run))}</b>). Stop the run first before removing the team.
              </p>
            </div>
            <div class="modal-foot">
              <button class="btn" onClick={() => setKickBlock(null)}>Cancel</button>
              <button class="btn primary" onClick={() => setKickBlock(null)}>Close</button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

const BOARD_COLUMNS = [
  ['todo', 'Backlog'],
  ['in_progress', 'In progress'],
  ['review', 'Review'],
  ['blocked', 'Blocked'],
  ['done', 'Done'],
];

function Board({ roomId, tasks, runTask, setModal, refresh }) {
  return (
    <div class="board">
      {BOARD_COLUMNS.map(([key, title]) => {
        const columnTasks = tasks
          .filter((t) => (key === 'todo' ? (t.status === 'todo' || t.status === 'backlog') : t.status === key))
          .sort((a, b) => priorityRank(a.priority) - priorityRank(b.priority) || String(a.title).localeCompare(String(b.title)));
        return (
          <div class="board-col">
            <div class="board-col-head">{title}<span>{columnTasks.length}</span></div>
            <div class="board-tasks">
              {columnTasks.length
                ? columnTasks.map((t) => (
                  <article class={`board-task sev-${esc(t.severity || 'none')}`} onClick={() => setModal({ kind: 'task-detail', roomId, task: t })} title="Click for details">
                    <div style="display:flex;justify-content:space-between;align-items:flex-start;gap:6px">
                      <div class="board-task-title">{esc(t.title)}</div>
                      {t.agentName && <span class="tag blue small">@{esc(t.agentName)}</span>}
                    </div>
                    <div class="board-task-desc">{esc(t.description || 'No description.')}</div>
                    <div class="board-task-foot" style="margin-top:8px;flex-wrap:wrap">
                      {t.severity === 'blocker' && <span class="tag sev-badge sev-blocker">Blocker</span>}
                      {t.severity === 'warning' && <span class="tag sev-badge sev-warning">Warning</span>}
                      <span class={`tag ${priorityColor(t.priority)}`}>{esc(priorityLabel(t.priority))}</span>
                      <span class="tag">effort {esc(t.effort)}</span>
                      {t.status === 'review' && <span class="tag blue">Review</span>}
                      {t.status === 'blocked' && <span class="tag orange">Blocked</span>}
                      {t.dependencies && t.dependencies.length > 0 && <span class="tag">deps {t.dependencies.length}</span>}
                      {t.retryCount > 0 && <span class="tag orange">rework ×{t.retryCount}</span>}
                    </div>
                    {(t.status === 'todo' || t.status === 'backlog') && (
                      <button class="btn primary small" style="margin-top:9px"
                        onClick={(e) => { e.stopPropagation(); runTask(t.id, e.currentTarget); }}>Run</button>
                    )}
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

export function TeamsPage({ state, openTeam, onTeamCreate, onUseTemplate, openTeamCreate, importTeamTemplate }) {
  const { teams, templates } = state;
  const useTemplate = onUseTemplate || importTeamTemplate;
  const createTeam = onTeamCreate || openTeamCreate;
  return (
    <>
      <div class="page-head">
        <div>
          <div class="eyebrow">Reusable agent teams</div>
          <h1 class="page-title">Teams</h1>
          <div class="page-sub">Build a team once, invite it into any room. Open a team to edit its title, instructions, agents, and main agent.</div>
        </div>
        <button class="btn primary" onClick={createTeam}><Icon name="plus" />Create team</button>
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
              <AgentAvatar agent={(t.agents || []).find((a) => a.name === t.mainAgent) || findAgent(state, t.mainAgent)} name={t.mainAgent} tone="blue" radius={6} />
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
                  {(t.agents || []).map((a) => <span class="chip" key={a.name}>{esc(a.name)}</span>)}
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
        {templates.map((t) => <TemplateCard key={t.id} template={t} onUse={useTemplate} state={state} />)}
      </div>
    </>
  );
}

export function AgentsPage({ state, openAgent, deleteAgent }) {
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
              <AgentAvatar agent={a} name={a.name} tone={a.requireReview ? 'orange' : 'green'} radius={6} />
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
              {deleteAgent && (
                <button class="icon-btn row-delete" title="Hapus agent" aria-label={`Hapus agent ${a.name}`}
                  onClick={(e) => { e.stopPropagation(); deleteAgent(a); }}
                  onKeyDown={(e) => e.stopPropagation()}>
                  <Icon name="trash" size={14} />
                </button>
              )}
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
                  <div class="room-title">{esc(run.status)} · {esc(run.model)}{task?.hidden ? ' · chat' : ''}</div>
                  <div class="room-sub">{esc(task ? task.title : 'Unknown task')} · {esc(run.result || run.error || 'Run executing…')}</div>
                  <div class="room-foot">
                    <span class="tag">{esc(run.provider)}</span>
                    <span class="meta-text">Started {new Date(run.startedAt).toLocaleString()} · {fmtTokens(run.tokens)} tokens · cost {fmtMoney(run.costUsd)}</span>
                  </div>
                </div>
                {run.status === 'running' && (
                  <button class="btn small" onClick={() => cancelRun(run.id)}>Stop</button>
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
    id: 'mercury', name: 'Mercury (Inception)', protocol: 'openai-compatible',
    baseUrl: 'https://api.inceptionlabs.ai/v1',
    models: ['mercury-3', 'mercury-2.5', 'mercury-2'],
    note: 'Diffusion LLM, 1M context, tool calling. Env: INCEPTION_API_KEY.',
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
  const hasSavedModels = !isNew && (initial?.models || []).length > 0;
  const ordered = hasSavedModels
    ? [initial.defaultModel, ...(initial.models || []).filter((m) => m !== initial.defaultModel)]
    : [];
  const suggestions = isNew ? (initial?.models || []) : [];
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
  const addModel = (model) => {
    if (models.includes(model)) return;
    setModelsText([...models, model].join('\n'));
  };

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
            {suggestions.length ? (
              <div class="chip-row" style="margin-bottom:8px">
                <span class="meta-text" style="margin-right:2px">Suggested:</span>
                {suggestions.map((model) => (
                  <button type="button" class={`chip chip-add ${models.includes(model) ? 'on' : ''}`}
                    onClick={() => addModel(model)}>
                    <Icon name="plus" size={11} />{esc(model)}
                  </button>
                ))}
              </div>
            ) : null}
            <textarea class="code-area" style="min-height:130px"
              placeholder={'One model id per line, e.g.\ndeepseek-v4.1-flash:free\nclaude-sonnet-5'}
              value={modelsText} onInput={(e) => setModelsText(e.currentTarget.value)} />
            <div class="md-hint">
              {defaultModel ? `Default model: ${defaultModel}` : 'Add models or pick a suggestion, then save.'}
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

function EngineCard({ notify }) {  const [status, setStatus] = useState(null);
  const [busy, setBusy] = useState(false);
  const [reason, setReason] = useState('');
  const [log, setLog] = useState('');

  const load = async () => {
    try {
      const d = await api('/api/engine/status');
      setStatus(d);
    } catch {
      setStatus(null);
    }
  };
  useEffect(() => { load(); }, []);

  const install = async () => {
    setBusy(true);
    setReason('');
    setLog('');
    try {
      const d = await api('/api/engine/install', { method: 'POST', body: '{}' });
      setStatus({ installed: d.installed, pythonVersion: d.pythonVersion, pipAvailable: d.pipAvailable });
      if (d.ok && d.installed) {
        notify('OpenHands engine installed — send a message again');
      } else {
        setReason(d.error || 'Installation failed — see the log below.');
        setLog(d.output || '');
      }
    } catch (error) {
      setReason(error.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="settings-card">
      <div class="settings-card-head">
        <div>
          <h3>Agent engine</h3>
          <p>
            {status
              ? `Python ${esc(status.pythonVersion || '?')} · pip ${status.pipAvailable ? 'tersedia' : 'tidak tersedia'}`
              : 'Memeriksa environment…'}
          </p>
        </div>
        <span class={`tag ${status?.installed ? 'green' : 'orange'}`}>
          {status?.installed ? 'SDK terpasang' : 'SDK belum terpasang'}
        </span>
      </div>
      {!status?.installed && status !== null && (
        <div style="margin-top:10px">
          <button class="btn primary small" disabled={busy} onClick={install}>
            {busy ? <span class="btn-spin"><Icon name="refresh" size={13} /></span> : <Icon name="refresh" size={13} />}
            {busy ? 'Installing…' : reason ? 'Retry' : 'Install runtime'}
          </button>
          {reason ? <div class="md-hint" style="color:#ffce9e;margin-top:8px">{esc(reason)}</div> : null}
          {log ? (
            <details class="sdk-log">
              <summary>Lihat log instalasi</summary>
              <pre>{esc(log)}</pre>
            </details>
          ) : null}
        </div>
      )}
    </div>
  );
}

function GlobalCleanCard({ notify, refresh }) {
  const [busy, setBusy] = useState('');
  const clean = async (kind) => {
    if (!confirm(kind === 'agents'
      ? 'Hapus SEMUA agent dari library global?'
      : 'Hapus SEMUA team dari library global?')) return;
    setBusy(kind);
    try {
      await api('/api/global/reset', {
        method: 'POST',
        body: JSON.stringify(kind === 'agents' ? { agents: true } : { teams: true }),
      });
      await refresh();
      notify(kind === 'agents' ? 'Library agent dibersihkan' : 'Team dibersihkan');
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy('');
    }
  };
  return (
    <div class="settings-card">
      <div class="settings-card-head">
        <div>
          <h3>Bersihkan library global</h3>
          <p>Agents &amp; teams are stored globally and shared across all projects.</p>
        </div>
      </div>
      <div style="display:flex;gap:8px">
        <button class="btn" disabled={!!busy} onClick={() => clean('agents')}>
          {busy === 'agents' ? 'Menghapus…' : 'Hapus semua agent'}
        </button>
        <button class="btn" disabled={!!busy} onClick={() => clean('teams')}>
          {busy === 'teams' ? 'Menghapus…' : 'Hapus semua team'}
        </button>
      </div>
      <div class="md-hint" style="margin-top:10px">
        Templates, provider, dan settings tidak terpengaruh. Team yang sudah ada menyimpan salinan agentnya sendiri.
      </div>
    </div>
  );
}

function RoomMCPModal({ room, servers, onClose, notify, refresh }) {
  const [ids, setIds] = useState(room.mcpServerIds || []);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState({});
  useEffect(() => {
    const run = async () => {
      const next = {};
      for (const srv of servers) {
        if (!srv.enabled) continue;
        next[srv.id] = { state: 'testing' };
        setStatus({ ...next });
        try {
          const r = await api(`/api/mcp/${encodeURIComponent(srv.id)}/test`, { method: 'POST' });
          next[srv.id] = { state: 'ok', tools: r.tools };
        } catch {
          next[srv.id] = { state: 'error' };
        }
        setStatus({ ...next });
      }
    };
    run();
  }, [servers]);
  const toggle = (id) => setIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  const save = async () => {
    setBusy(true);
    try {
      await api(`/api/rooms/${encodeURIComponent(room.id)}/mcp`, { method: 'PUT', body: JSON.stringify({ serverIds: ids }) });
      notify('MCP tools updated');
      await refresh();
      onClose();
    } catch (e) { notify(e.message); } finally { setBusy(false); }
  };
  const show = servers.filter((s2) => s2.enabled || ids.includes(s2.id));
  return (
    <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div class="modal">
        <header class="modal-head">
          <h3>MCP tools for this room</h3>
          <button type="button" class="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </header>
        <div class="modal-body">
          <p class="section-note">Enabled servers inject their tools into agents in this room.</p>
          {show.length ? show.map((srv) => {
            const st = status[srv.id];
            return (
            <label class="team-pick" style="display:flex;align-items:center;gap:10px;margin-bottom:8px">
              <input type="checkbox" checked={ids.includes(srv.id)} onChange={() => toggle(srv.id)} />
              <span class="team-pick-body">
                <span class="team-pick-name">
                  {esc(srv.name)}
                  {!srv.enabled ? <span class="tag muted" style="margin-left:6px">Disabled</span>
                    : st && st.state === 'ok' ? <span class="tag green" style="margin-left:6px">Connected · {st.tools} tools</span>
                    : st && st.state === 'error' ? <span class="tag orange" style="margin-left:6px">Failed</span>
                    : <span class="tag" style="margin-left:6px">Checking…</span>}
                </span>
                <span class="team-pick-desc">{esc(srv.type)} · {esc(srv.type === 'http' ? srv.url : srv.command)}</span>
              </span>
            </label>
            );
          }) : <div class="empty">No MCP servers configured. Add them in Settings → MCP Servers.</div>}
        </div>
        <footer class="modal-foot">
          <button type="button" class="btn" onClick={onClose}>Cancel</button>
          <button type="button" class="btn primary" disabled={busy} onClick={save}>Save</button>
        </footer>
      </div>
    </div>
  );
}

function MCPCard({ state, notify, refresh }) {
  const servers = state.mcpServers || [];
  const [form, setForm] = useState(null);
  const [busy, setBusy] = useState(false);
  const [busyId, setBusyId] = useState(null);
  const [status, setStatus] = useState({});
  useEffect(() => {
    const run = async () => {
      const next = {};
      for (const srv of servers) {
        if (!srv.enabled) continue;
        next[srv.id] = { state: 'testing' };
        setStatus({ ...next });
        try {
          const r = await api(`/api/mcp/${encodeURIComponent(srv.id)}/test`, { method: 'POST' });
          next[srv.id] = { state: 'ok', tools: r.tools };
        } catch {
          next[srv.id] = { state: 'error' };
        }
        setStatus({ ...next });
      }
    };
    run();
  }, [servers]);

  const parseKV = (text) => Object.fromEntries(
    String(text || '').split('\n').map((l) => l.split(/[=:]/)).filter((a) => a.length === 2).map(([k, v]) => [k.trim(), v.trim()])
  );
  const fmtKV = (obj) => Object.entries(obj || {}).map(([k, v]) => k + '=' + v).join('\n');
  const set = (f, v) => setForm((prev) => ({ ...prev, [f]: v }));

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body = { ...form };
      if (body.type === 'stdio') {
        body.args = (body.argsText || '').split('\n').map((x) => x.trim()).filter(Boolean);
        body.env = parseKV(body.envText);
        delete body.argsText; delete body.envText;
      } else {
        body.headers = parseKV(body.headersText);
        delete body.headersText;
      }
      const method = body.id ? 'PUT' : 'POST';
      const url = body.id ? `/api/mcp/${encodeURIComponent(body.id)}` : '/api/mcp';
      await api(url, { method, body: JSON.stringify(body) });
      notify('MCP server saved');
      await refresh();
      setForm(null);
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };

  const test = async (srv) => {
    setBusyId(srv.id);
    setStatus((prev) => ({ ...prev, [srv.id]: { state: 'testing' } }));
    try {
      const r = await api(`/api/mcp/${encodeURIComponent(srv.id)}/test`, { method: 'POST' });
      setStatus((prev) => ({ ...prev, [srv.id]: { state: 'ok', tools: r.tools } }));
      notify(`Connection OK — ${r.tools} tool(s) exposed`);
    } catch (e) {
      setStatus((prev) => ({ ...prev, [srv.id]: { state: 'error' } }));
      notify('Test failed: ' + e.message);
    } finally { setBusyId(null); }
  };

  const remove = async (srv) => {
    if (!confirm(`Delete MCP server "${srv.name}"?`)) return;
    try {
      await api(`/api/mcp/${encodeURIComponent(srv.id)}`, { method: 'DELETE' });
      notify('Deleted');
      await refresh();
    } catch (e) { notify(e.message); }
  };

  return (
    <div class="settings-card">
      <div class="settings-card-head">
        <div>
          <h3>MCP Servers</h3>
          <p>Global server definitions. Rooms opt in to individual servers.</p>
        </div>
        <span class="tag">{(servers).length}</span>
        <button class="btn small" onClick={() => setForm({ type: 'stdio', enabled: true })}>
          <Icon name="plus" size={13} />Add server
        </button>
      </div>
      <div style="margin-top:12px;display:flex;flex-direction:column;gap:10px">
        {servers.length ? servers.map((srv) => (
          <article class="provider-row">
            <span class="preset-mark">MCP</span>
            <div class="provider-main">
              <div class="provider-name">
                {esc(srv.name)}
                <span class="tag">{esc(srv.type)}</span>
                {!srv.enabled ? <span class="tag muted">Disabled</span>
                  : status[srv.id] && status[srv.id].state === 'ok' ? <span class="tag green">Connected · {status[srv.id].tools} tools</span>
                  : status[srv.id] && status[srv.id].state === 'error' ? <span class="tag orange">Failed</span>
                  : <span class="tag">Checking…</span>}
              </div>
              <div class="provider-url">{esc(srv.type === 'http' ? srv.url : srv.command + (srv.args && srv.args.length ? ' ' + srv.args.join(' ') : ''))}</div>
            </div>
            <div class="provider-actions">
              <button class="btn small" disabled={busyId === srv.id} onClick={() => test(srv)}>
                {busyId === srv.id ? 'Testing…' : 'Test'}
              </button>
              <button class="btn small" onClick={() => setForm({ ...srv, argsText: (srv.args || []).join('\n'), envText: fmtKV(srv.env), headersText: fmtKV(srv.headers) })}>
                <Icon name="pencil" size={13} />Edit
              </button>
              <button class="icon-btn" aria-label="Delete" onClick={() => remove(srv)}><Icon name="trash" size={14} /></button>
            </div>
          </article>
        )) : <div class="empty">No MCP servers yet. Add one above.</div>}
      </div>

      {form && (
        <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) setForm(null); }}>
          <form class="modal modal-lg" onSubmit={submit}>
            <header class="modal-head">
              <h3>{form.id ? 'Edit MCP server' : 'Add MCP server'}</h3>
              <button type="button" class="icon-btn" aria-label="Close" onClick={() => setForm(null)}><Icon name="x" /></button>
            </header>
            <div class="modal-body">
              <div class="field">
                <label>Name</label>
                <input required maxlength="120" value={form.name || ''} onInput={(e) => set('name', e.currentTarget.value)} placeholder="e.g. GitHub MCP" />
              </div>
              <div class="field">
                <label>Type</label>
                <select value={form.type} onChange={(e) => set('type', e.currentTarget.value)}>
                  <option value="stdio">Local command (stdio)</option>
                  <option value="http">Remote URL (Streamable HTTP)</option>
                </select>
              </div>
              {form.type === 'stdio' ? (
                <>
                  <div class="field">
                    <label>Command</label>
                    <input required value={form.command || ''} onInput={(e) => set('command', e.currentTarget.value)} placeholder="e.g. npx" />
                  </div>
                  <div class="field">
                    <label>Arguments (one per line)</label>
                    <textarea rows="3" value={form.argsText || ''} onInput={(e) => set('argsText', e.currentTarget.value)} placeholder="e.g. @modelcontextprotocol/server-github" />
                  </div>
                  <div class="field">
                    <label>Working directory</label>
                    <input value={form.cwd || ''} onInput={(e) => set('cwd', e.currentTarget.value)} />
                  </div>
                  <div class="field">
                    <label>Environment (KEY=VALUE per line)</label>
                    <textarea rows="3" value={form.envText || ''} onInput={(e) => set('envText', e.currentTarget.value)} placeholder="GITHUB_TOKEN=…" />
                  </div>
                </>
              ) : (
                <>
                  <div class="field">
                    <label>URL</label>
                    <input required type="url" value={form.url || ''} onInput={(e) => set('url', e.currentTarget.value)} placeholder="https://…/mcp" />
                  </div>
                  <div class="field">
                    <label>Bearer token (optional)</label>
                    <input type="password" value={form.token || ''} onInput={(e) => set('token', e.currentTarget.value)} />
                  </div>
                  <div class="field">
                    <label>Extra headers (KEY: VALUE per line)</label>
                    <textarea rows="3" value={form.headersText || ''} onInput={(e) => set('headersText', e.currentTarget.value)} />
                  </div>
                </>
              )}
              <label class="choice" style="margin-top:8px">
                <input type="checkbox" checked={!!form.enabled} onChange={(e) => set('enabled', e.currentTarget.checked)} />
                <span>Enabled (visible to rooms)</span>
              </label>
            </div>
            <footer class="modal-foot">
              <button type="button" class="btn" onClick={() => setForm(null)}>Cancel</button>
              <button type="submit" class="btn primary" disabled={busy}>Save</button>
            </footer>
          </form>
        </div>
      )}
    </div>
  );
}

export function MCPServersPage({ state, notify, refresh }) {
  return (
    <>
      <div class="page-head">
        <div>
          <h1 class="page-title">MCP Servers</h1>
          <div class="page-sub">Global server definitions. Rooms opt in to individual servers.</div>
        </div>
      </div>
      <MCPCard state={state} notify={notify} refresh={refresh} />
    </>
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
        <>
          <EngineCard notify={notify} />
          <GlobalCleanCard notify={notify} refresh={refresh} />
          <ProvidersTab
            state={state}
            onPreset={openPreset}
            onEdit={(provider) => setProviderModal({ initial: provider, isNew: false })}
            onDelete={deleteProvider}
            onToggle={toggleProvider}
          />
        </>
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
                <label>Action rounds per run</label>
                <input type="number" min="1" max="200" value={policy.maxIterations || 30}
                  onInput={(e) => setField('maxIterations', e.currentTarget.value, true)} />
              </div>
              <div class="field">
                <label>Read-only budget (rounds)</label>
                <input type="number" min="1" max="500" value={policy.readOnlyBudget || 40}
                  onInput={(e) => setField('readOnlyBudget', e.currentTarget.value, true)} />
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
              <div class="field">
                <label>Context compaction</label>
                <label class="check" style="margin:0">
                  <input type="checkbox" checked={!policy.disableCompaction}
                    onChange={(e) => setField('disableCompaction', !e.currentTarget.checked)} />
                  <span>Summarize old context on long runs</span>
                </label>
              </div>
              <div class="field">
                <label>Compaction token threshold</label>
                <input type="number" min="1000" max="1000000" step="1000" value={policy.compactionThreshold || 120000}
                  onInput={(e) => setField('compactionThreshold', e.currentTarget.value, true)} />
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
            <label class="check">
              <input type="checkbox" checked={Boolean(policy.disableQuickReplies)}
                onChange={(e) => setField('disableQuickReplies', e.currentTarget.checked)} />
              <span>Matikan quick-reply sapaan (semua pesan dijalankan agent)</span>
            </label>
            <label class="check">
              <input type="checkbox" checked={Boolean(policy.routeIntentsWithModel)}
                onChange={(e) => setField('routeIntentsWithModel', e.currentTarget.checked)} />
              <span>Routing intent dengan model (opsional — lebih akurat, tambah 1 request kecil per pesan)</span>
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
