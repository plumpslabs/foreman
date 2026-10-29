import { useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';

const esc = (s) => String(s ?? '');

const TOOL_OPTIONS = [
  ['read', 'Read files'],
  ['search', 'Search code'],
  ['edit', 'Edit files'],
  ['write_tests', 'Write tests'],
  ['run_tests', 'Run tests'],
  ['run_commands', 'Run commands'],
  ['delegate', 'Delegate'],
  ['synthesize', 'Synthesize'],
  ['preview', 'Preview'],
  ['diff_audit', 'Diff audit'],
];

const TEXT_FILE = /\.(md|markdown|txt|json|ya?ml|toml|sh|bash|py|js|ts|tsx|jsx|go|css|html|xml|csv|ini|cfg|rst|adoc)$/i;

function Backdrop({ onClose, title, submitLabel, onSubmit, busy, children, footerLeft, wide }) {
  return (
    <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <form class={`modal ${wide ? 'modal-lg' : ''}`} onSubmit={onSubmit}>
        <header class="modal-head">
          <h3>{esc(title)}</h3>
          <button type="button" class="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </header>
        <div class="modal-body">{children}</div>
        <footer class="modal-foot">
          {footerLeft ? <div style="margin-right:auto">{footerLeft}</div> : null}
          <button type="button" class="btn" onClick={onClose}>Cancel</button>
          <button type="submit" class="btn primary" disabled={!!busy}>{esc(submitLabel)}</button>
        </footer>
      </form>
    </div>
  );
}

export function ModalHost({ modal, setModal, state, notify, refresh, saveSkill, deleteSkill, deleteAgent }) {
  if (!modal) return null;
  const onClose = () => setModal(null);
  const { kind } = modal;
  const common = { onClose, notify, refresh };
  return (
    <>
      {kind === 'room' && <RoomForm teams={state.teams} {...common} />}
      {kind === 'rooms' && <ManageTeamsModal teams={state.teams} onClose={onClose} />}
      {kind === 'task' && (
        <TaskForm room={state.rooms.find((r) => r.id === modal.roomId)} state={state} task={modal.task} {...common} />
      )}
      {kind === 'team' && (
        <TeamImportForm template={state.templates.find((t) => t.id === modal.templateId)} {...common} />
      )}
      {kind === 'team-detail' && <TeamDetailForm team={modal.team} state={state} {...common} />}
      {kind === 'agent' && (
        <AgentForm agent={modal.agent} state={state} deleteAgent={deleteAgent} {...common} />
      )}
      {kind === 'skill-custom' && <SkillForm {...common} />}
      {kind === 'skill' && (
        <SkillDetailForm skill={modal.skill} saveSkill={saveSkill} deleteSkill={deleteSkill} {...common} />
      )}
      {kind === 'skill-import' && <SkillImportForm {...common} />}
      {kind === 'attach' && <AttachContext onClose={onClose} notify={notify} />}
      {kind === 'route' && <RouteAgent agents={modal.agents || []} current={modal.current} onClose={onClose} onPick={modal.onPick} />}
      {kind === 'effort' && <EffortPicker current={modal.current} onClose={onClose} />}
      {kind === 'approval' && <ApprovalInfo required={modal.required} onClose={onClose} />}
    </>
  );
}

/* ---------- Rooms ---------- */

function RoomForm({ teams, onClose, notify, refresh }) {
  const [name, setName] = useState('');
  const [goal, setGoal] = useState('');
  const [teamIds, setTeamIds] = useState([]);
  const [busy, setBusy] = useState(false);

  const toggleTeam = (id) => setTeamIds((prev) => (prev.includes(id) ? prev.filter((t) => t !== id) : [...prev, id]));

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/api/rooms', { method: 'POST', body: JSON.stringify({ name: name.trim(), goal, teamIds }) });
      notify(`Room created: ${name.trim()}`);
      await refresh();
      onClose();
    } catch (error) { notify(error.message); } finally { setBusy(false); }
  };

  return (
    <Backdrop onClose={onClose} title="Create a room" submitLabel="Create room" onSubmit={submit} busy={busy}>
      <div class="field">
        <label for="roomName">Room name</label>
        <input id="roomName" required maxlength="100" placeholder="e.g. Improve API reliability"
          value={name} onInput={(e) => setName(e.currentTarget.value)} />
      </div>
      <div class="field">
        <label for="roomGoal">Goal</label>
        <textarea id="roomGoal" maxlength="4000" placeholder="Describe the work and what a good result looks like"
          value={goal} onInput={(e) => setGoal(e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Invite teams</label>
        <div class="choice-grid">
          {teams.length
            ? teams.map((t) => (
              <label class={`choice ${teamIds.includes(t.id) ? 'selected' : ''}`}>
                <input type="checkbox" checked={teamIds.includes(t.id)} onChange={() => toggleTeam(t.id)} />
                <span><b>{esc(t.name)}</b><br /><small>{esc(t.purpose)}</small></span>
              </label>
            ))
            : <span class="muted">No teams yet. You can invite a team later.</span>}
        </div>
      </div>
      <div class="notice">This creates a local room record only. No agent is started and no code is changed.</div>
    </Backdrop>
  );
}

function ManageTeamsModal({ teams, onClose }) {
  return (
    <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div class="modal modal-lg">
        <div class="modal-head">
          <h3>Manage teams</h3>
          <button class="icon-btn" onClick={onClose} aria-label="Close"><Icon name="x" /></button>
        </div>
        <div class="modal-body">
          <p class="section-note">Imported teams are reusable copies you can invite into any room.</p>
          <div class="list">
            {teams.length
              ? teams.map((t) => (
                <article class="room-row">
                  <div class="agent-avatar blue" style="border-radius:6px">{esc(t.name).slice(0, 2).toUpperCase()}</div>
                  <div class="room-main">
                    <div class="room-title">{esc(t.name)}<span class="tag">{esc(t.templateId)} template</span></div>
                    <div class="room-sub">{esc(t.purpose)}</div>
                    <div class="room-foot"><span class="meta-text">Main: {esc(t.mainAgent)} · {(t.agents || []).length} agents</span></div>
                  </div>
                </article>
              ))
              : <div class="muted">No teams yet. Go to Teams to import a template.</div>}
          </div>
        </div>
        <div class="modal-foot"><button class="btn" onClick={onClose}>Close</button></div>
      </div>
    </div>
  );
}

function TaskForm({ room, state, onClose, notify, refresh, task }) {
  const [form, setForm] = useState({
    title: task?.title || '',
    description: task?.description || '',
    priority: task?.priority || 'normal',
    effort: task?.effort || state.settings.defaultEffort || 'medium',
    teamId: task?.teamId || '',
    dependencies: task?.dependencies || [],
  });
  const [busy, setBusy] = useState(false);
  if (!room) return null;
  const available = state.tasks.filter((t) => t.roomId === room.id && t.id !== (task?.id || ''));
  const teams = state.teams.filter((t) => room.teamIds.includes(t.id));

  const set = (f, v) => setForm((p) => ({ ...p, [f]: v }));

  const submit = async (e) => {
    e.preventDefault();
    if (task) { onClose(); return; }
    setBusy(true);
    try {
      await api(`/api/rooms/${encodeURIComponent(room.id)}/tasks`, {
        method: 'POST',
        body: JSON.stringify({
          title: form.title,
          description: form.description,
          teamId: form.teamId,
          priority: form.priority,
          effort: form.effort,
          dependencies: form.dependencies,
        }),
      });
      notify('Task created');
      await refresh();
      onClose();
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };

  const toggleDependency = (id) => set('dependencies', form.dependencies.includes(id)
    ? form.dependencies.filter((x) => x !== id) : [...form.dependencies, id]);

  return (
    <Backdrop onClose={onClose} title={task ? 'Task details' : 'Create room task'}
      submitLabel={task ? 'Close' : 'Create task'} onSubmit={submit} busy={busy}>
      <div class="field">
        <label>Task title</label>
        <input required maxlength="200" placeholder="Implement a bounded change" disabled={!!task}
          value={form.title} onInput={(e) => set('title', e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Description</label>
        <textarea maxlength="16000" placeholder="What should be done and verified?" disabled={!!task}
          value={form.description} onInput={(e) => set('description', e.currentTarget.value)} />
      </div>
      <div class="settings-grid">
        <div class="field">
          <label>Priority</label>
          <select value={form.priority} disabled={!!task} onChange={(e) => set('priority', e.currentTarget.value)}>
            <option>normal</option><option>low</option><option>high</option><option>urgent</option>
          </select>
        </div>
        <div class="field">
          <label>Effort</label>
          <select value={form.effort} disabled={!!task} onChange={(e) => set('effort', e.currentTarget.value)}>
            <option>low</option><option>medium</option><option>high</option>
          </select>
        </div>
        <div class="field">
          <label>Team</label>
          <select value={form.teamId} disabled={!!task} onChange={(e) => set('teamId', e.currentTarget.value)}>
            <option value="">Room default</option>
            {teams.map((t) => <option value={esc(t.id)}>{esc(t.name)}</option>)}
          </select>
        </div>
        <div class="field">
          <label>Dependencies</label>
          <select multiple size="3" value={form.dependencies} disabled={!!task}
            onChange={(e) => set('dependencies', Array.from(e.currentTarget.selectedOptions).map((o) => o.value))}>
            {available.map((t) => <option value={esc(t.id)}>{esc(t.title)}</option>)}
          </select>
        </div>
      </div>
      <div class="notice">
        {task
          ? 'Task fields are immutable after creation; move work through status transitions.'
          : 'Tasks and dependencies are persisted in this room. Work starts when you click Run.'}
      </div>
    </Backdrop>
  );
}

/* ---------- Teams ---------- */

function TeamImportForm({ template, onClose, notify, refresh }) {
  const [name, setName] = useState(template ? template.name : '');
  const [busy, setBusy] = useState(false);
  if (!template) return null;
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/api/teams/import', { method: 'POST', body: JSON.stringify({ templateId: template.id, name: name.trim() }) });
      notify(`Imported ${name.trim()}`);
      await refresh();
      onClose();
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };
  return (
    <Backdrop onClose={onClose} title="Import team template" submitLabel="Import editable copy" onSubmit={submit} busy={busy}>
      <div class="field">
        <label for="teamName">Team name</label>
        <input id="teamName" required maxlength="100" value={name} onInput={(e) => setName(e.currentTarget.value)} />
      </div>
      <p><b>{esc(template.purpose)}</b></p>
      <p class="muted">
        Main agent: {esc(template.mainAgent)}<br />
        {(template.agents || []).map((a, i) => (
          <span key={a.name}>{i > 0 ? <br /> : null}{esc(a.name)} — {esc(a.role)}</span>
        ))}<br /><br />
        {esc(template.instructions)}
      </p>
      <div class="notice">Template imports are configuration only. They do not run code or configure providers.</div>
    </Backdrop>
  );
}

function TeamDetailForm({ team, state, onClose, notify, refresh }) {
  const [form, setForm] = useState({
    id: team.id,
    templateId: team.templateId,
    name: team.name,
    purpose: team.purpose,
    mainAgent: team.mainAgent,
    instructions: team.instructions || '',
    agents: team.agents || [],
    createdAt: team.createdAt,
  });
  const [busy, setBusy] = useState(false);
  const set = (f, v) => setForm((p) => ({ ...p, [f]: v }));
  const skillName = (ref) => {
    const match = (state.skills || []).find((s) => s.id === ref || s.name === ref);
    return match ? match.name : ref;
  };

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api(`/api/teams/${encodeURIComponent(team.id)}`, { method: 'PUT', body: JSON.stringify(form) });
      notify('Team updated');
      await refresh();
      onClose();
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };

  return (
    <Backdrop onClose={onClose} title="Team details" submitLabel="Save team" onSubmit={submit} busy={busy} wide>
      <div class="form-grid">
        <div class="field">
          <label>Team name</label>
          <input required maxlength="100" value={form.name} onInput={(e) => set('name', e.currentTarget.value)} />
        </div>
        <div class="field">
          <label>Main agent</label>
          <select value={form.mainAgent} onChange={(e) => set('mainAgent', e.currentTarget.value)}>
            {(form.agents || []).map((a) => <option value={esc(a.name)}>{esc(a.name)}</option>)}
          </select>
        </div>
      </div>
      <div class="field">
        <label>Purpose</label>
        <input maxlength="400" value={form.purpose} onInput={(e) => set('purpose', e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Team instructions (markdown)</label>
        <textarea class="code-area" maxlength="16000" value={form.instructions}
          onInput={(e) => set('instructions', e.currentTarget.value)} />
        <div class="md-hint"><Icon name="file-text" size={12} />Markdown is supported: headings, lists, and code blocks.</div>
      </div>

      <div class="form-section">
        <div class="form-section-title">Agents on this team ({form.agents.length})</div>
        <div class="form-section-note">Each agent below is a full profile copy owned by this team.</div>
        {(form.agents || []).map((a) => (
          <div class="agent-detail-row" style="flex-direction:column;gap:6px">
            <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">
              <div class="agent-avatar green" style="border-radius:6px">{esc(a.name).slice(0, 2).toUpperCase()}</div>
              <b>{esc(a.name)}</b>
              {a.name === form.mainAgent ? <span class="tag green">Main agent</span> : <span class="tag">Worker</span>}
              {a.requireReview ? <span class="tag orange">Requires review</span> : null}
            </div>
            <div class="agent-detail-label">{esc(a.role)}</div>
            {a.description ? <div class="agent-detail-value">{esc(a.description)}</div> : null}
            {a.instructions ? <div class="agent-detail-value" style="color:var(--muted)">{esc(a.instructions)}</div> : null}
            <div style="display:flex;gap:6px;flex-wrap:wrap;margin-top:2px">
              {(a.tools || []).map((t) => <span class="chip">{esc(t)}</span>)}
              {(a.skills || []).map((s) => <span class="chip">{esc(skillName(s))}</span>)}
            </div>
          </div>
        ))}
      </div>
    </Backdrop>
  );
}

/* ---------- Agents ---------- */

function AgentForm({ agent, state, deleteAgent, onClose, notify, refresh }) {
  const editing = Boolean(agent && agent.id);
  const initialSkills = (agent?.skills || [])
    .map((ref) => {
      const match = (state.skills || []).find((s) => s.id === ref || s.name === ref);
      return match ? match.id : null;
    })
    .filter(Boolean);
  const [form, setForm] = useState({
    id: agent?.id || '',
    name: agent?.name || '',
    role: agent?.role || '',
    description: agent?.description || '',
    instructions: agent?.instructions || '',
    model: agent?.model || '',
    providerId: agent?.providerId || '',
    effort: agent?.effort || '',
    skills: initialSkills,
    tools: agent?.tools || ['read', 'search'],
    requireReview: Boolean(agent?.requireReview),
  });
  const [busy, setBusy] = useState(false);
  const set = (f, v) => setForm((p) => ({ ...p, [f]: v }));
  const toggle = (f, v) => set(f, form[f].includes(v) ? form[f].filter((x) => x !== v) : [...form[f], v]);

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body = JSON.stringify({ ...form, name: form.name.trim(), role: form.role.trim() });
      if (editing) await api(`/api/agents/${encodeURIComponent(agent.id)}`, { method: 'PUT', body });
      else await api('/api/agents', { method: 'POST', body });
      notify(editing ? 'Agent updated' : 'Agent created');
      await refresh();
      onClose();
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };

  const remove = async () => {
    if (!confirm(`Delete agent "${agent.name}"? Teams keep their own copies.`)) return;
    await deleteAgent(agent.id);
    onClose();
  };

  const footerLeft = editing ? (
    <button type="button" class="btn" style="color:var(--red);border-color:#523030" onClick={remove}>
      <Icon name="trash" size={14} />Delete
    </button>
  ) : null;

  return (
    <Backdrop onClose={onClose} title={editing ? 'Agent profile' : 'Create agent profile'}
      submitLabel={editing ? 'Save agent' : 'Create agent'} onSubmit={submit} busy={busy} wide footerLeft={footerLeft}>
      {editing && (
        <div class="detail-hero">
          <div class="agent-avatar green">{esc(form.name).slice(0, 2).toUpperCase()}</div>
          <div>
            <div class="detail-hero-title">{esc(form.name || 'Unnamed agent')}</div>
            <div class="detail-hero-sub">{esc(form.role || 'No role yet')}</div>
          </div>
        </div>
      )}

      <div class="form-grid">
        <div class="field">
          <label>Agent name</label>
          <input required maxlength="80" placeholder="e.g. Sentinel" value={form.name}
            onInput={(e) => set('name', e.currentTarget.value)} />
        </div>
        <div class="field">
          <label>Role</label>
          <input required maxlength="160" placeholder="e.g. Security reviewer" value={form.role}
            onInput={(e) => set('role', e.currentTarget.value)} />
        </div>
      </div>

      <div class="field">
        <label>Short description</label>
        <input maxlength="500" placeholder="What this agent is responsible for" value={form.description}
          onInput={(e) => set('description', e.currentTarget.value)} />
      </div>

      <div class="field">
        <label>Instructions (markdown)</label>
        <textarea class="code-area" maxlength="16000" placeholder="# Responsibilities&#10;&#10;- Inspect before editing&#10;- Report evidence&#10;- Never touch secrets"
          value={form.instructions} onInput={(e) => set('instructions', e.currentTarget.value)} />
        <div class="md-hint"><Icon name="file-text" size={12} />Markdown is stored as-is and shown to the agent at run time.</div>
      </div>

      <div class="form-grid">
        <div class="field">
          <label>Provider</label>
          <select value={form.providerId} onChange={(e) => set('providerId', e.currentTarget.value)}>
            <option value="">Auto / workspace default</option>
            {(state.providers || []).map((p) => <option value={esc(p.id)}>{esc(p.name)}</option>)}
          </select>
        </div>
        <div class="field">
          <label>Model override</label>
          <input placeholder="e.g. claude-sonnet" value={form.model}
            onInput={(e) => set('model', e.currentTarget.value)} />
        </div>
      </div>

      <div class="field">
        <label>Default effort</label>
        <select value={form.effort} onChange={(e) => set('effort', e.currentTarget.value)}>
          <option value="">Workspace default</option>
          <option value="low">low</option>
          <option value="medium">medium</option>
          <option value="high">high</option>
        </select>
      </div>

      <div class="form-section">
        <div class="form-section-title">Allowed tools</div>
        <div class="form-section-note">Only these tools are exposed to the agent during a run.</div>
        <div class="tool-grid">
          {TOOL_OPTIONS.map(([value, label]) => (
            <label class={`tool-check ${form.tools.includes(value) ? 'on' : ''}`}>
              <input type="checkbox" checked={form.tools.includes(value)} onChange={() => toggle('tools', value)} />
              <span>{esc(label)}</span>
            </label>
          ))}
        </div>
      </div>

      <div class="form-section">
        <div class="form-section-title">Skills</div>
        <div class="form-section-note">
          Attach skills created on the Skills page. Active skills are injected; passive skills stay as reference.
        </div>
        <div class="skill-pick">
          {(state.skills || []).length
            ? state.skills.map((s) => (
              <label class={`tool-check ${form.skills.includes(s.id) ? 'on' : ''}`}>
                <input type="checkbox" checked={form.skills.includes(s.id)} onChange={() => toggle('skills', s.id)} />
                <span>{esc(s.name)} <small class="muted">· {s.mode === 'active' ? 'active' : 'passive'}</small></span>
              </label>
            ))
            : <span class="muted">No skills yet. Create one on the Skills page.</span>}
        </div>
      </div>

      <label class="check">
        <input type="checkbox" checked={form.requireReview}
          onChange={(e) => set('requireReview', e.currentTarget.checked)} />
        <span>Require review before this agent's output is accepted</span>
      </label>
    </Backdrop>
  );
}

/* ---------- Skills ---------- */

function SkillForm({ onClose, notify, refresh }) {
  const [form, setForm] = useState({ name: '', description: '', instructions: '', mode: 'passive' });
  const [busy, setBusy] = useState(false);
  const set = (f, v) => setForm((p) => ({ ...p, [f]: v }));
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/api/skills', {
        method: 'POST',
        body: JSON.stringify({
          name: form.name,
          description: form.description,
          instructions: form.instructions,
          mode: form.mode,
          enabled: form.mode === 'active',
          files: [{ path: 'SKILL.md', content: form.instructions }],
        }),
      });
      notify('Skill saved');
      await refresh();
      onClose();
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };
  return (
    <Backdrop onClose={onClose} title="Create skill" submitLabel="Save skill" onSubmit={submit} busy={busy} wide>
      <div class="form-grid">
        <div class="field">
          <label>Name</label>
          <input required maxlength="100" value={form.name} onInput={(e) => set('name', e.currentTarget.value)} />
        </div>
        <div class="field">
          <label>Mode</label>
          <select value={form.mode} onChange={(e) => set('mode', e.currentTarget.value)}>
            <option value="passive">Passive — reference only</option>
            <option value="active">Active — injected into context</option>
          </select>
        </div>
      </div>
      <div class="field">
        <label>Description</label>
        <input maxlength="300" value={form.description} onInput={(e) => set('description', e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Instructions (markdown)</label>
        <textarea class="code-area" required maxlength="16000" value={form.instructions}
          onInput={(e) => set('instructions', e.currentTarget.value)} />
      </div>
    </Backdrop>
  );
}

function SkillDetailForm({ skill, saveSkill, deleteSkill, onClose, notify }) {
  const [form, setForm] = useState({
    id: skill.id,
    name: skill.name,
    description: skill.description || '',
    mode: skill.mode || (skill.enabled ? 'active' : 'passive'),
    instructions: skill.instructions || '',
    files: (skill.files && skill.files.length)
      ? skill.files.map((f) => ({ ...f }))
      : [{ path: 'SKILL.md', content: skill.instructions || '' }],
  });
  const [busy, setBusy] = useState(false);
  const set = (f, v) => setForm((p) => ({ ...p, [f]: v }));
  const setFile = (index, field, value) => set('files', form.files.map((f, i) => (i === index ? { ...f, [field]: value } : f)));
  const addFile = () => set('files', [...form.files, { path: `notes-${form.files.length + 1}.md`, content: '' }]);
  const removeFile = (index) => set('files', form.files.filter((_, i) => i !== index));

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    const ok = await saveSkill({
      ...form,
      enabled: form.mode === 'active',
      files: form.files.filter((f) => f.path.trim()),
    }, 'Skill saved');
    setBusy(false);
    if (ok) onClose();
  };

  const remove = async () => {
    if (!confirm(`Delete skill "${skill.name}"?`)) return;
    await deleteSkill(skill.id);
    onClose();
  };

  const footerLeft = (
    <button type="button" class="btn" style="color:var(--red);border-color:#523030" onClick={remove}>
      <Icon name="trash" size={14} />Delete
    </button>
  );

  return (
    <Backdrop onClose={onClose} title="Skill details" submitLabel="Save skill" onSubmit={submit} busy={busy} wide footerLeft={footerLeft}>
      <div class="detail-hero">
        <div class="team-icon"><Icon name={form.mode === 'active' ? 'check-circle' : 'archive'} size={20} /></div>
        <div>
          <div class="detail-hero-title">{esc(form.name)}</div>
          <div class="detail-hero-sub">Version {esc(skill.version)} · {form.files.length} files · {form.mode}</div>
        </div>
      </div>

      <div class="form-grid">
        <div class="field">
          <label>Name</label>
          <input required maxlength="100" value={form.name} onInput={(e) => set('name', e.currentTarget.value)} />
        </div>
        <div class="field">
          <label>Mode</label>
          <select value={form.mode} onChange={(e) => set('mode', e.currentTarget.value)}>
            <option value="passive">Passive — reference only</option>
            <option value="active">Active — injected into context</option>
          </select>
        </div>
      </div>
      <div class="field">
        <label>Description</label>
        <input maxlength="300" value={form.description} onInput={(e) => set('description', e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Primary instructions (SKILL.md)</label>
        <textarea class="code-area" value={form.instructions}
          onInput={(e) => set('instructions', e.currentTarget.value)} />
      </div>

      <div class="form-section">
        <div class="form-section-title" style="display:flex;justify-content:space-between;align-items:center">
          Bundle files
          <button type="button" class="btn small" onClick={addFile}><Icon name="plus" size={13} />Add file</button>
        </div>
        <div class="form-section-note">Store notes, references, and scripts as relative paths (sub-folders allowed).</div>
        {form.files.map((file, index) => (
          <div style="border:1px solid var(--line);border-radius:9px;padding:10px;margin-bottom:10px;background:var(--panel2)">
            <div style="display:flex;gap:8px;align-items:center;margin-bottom:8px">
              <input value={file.path} placeholder="references/guide.md"
                style="flex:1;background:#0d1014;border:1px solid #343b46;border-radius:7px;color:var(--text);padding:7px 9px;font-size:11px"
                onInput={(e) => setFile(index, 'path', e.currentTarget.value)} />
              <button type="button" class="icon-btn" aria-label="Remove file" onClick={() => removeFile(index)}>
                <Icon name="trash" size={14} />
              </button>
            </div>
            <textarea class="code-area" style="min-height:110px" value={file.content}
              onInput={(e) => setFile(index, 'content', e.currentTarget.value)} />
          </div>
        ))}
      </div>
    </Backdrop>
  );
}

async function readEntry(entry, prefix, out) {
  if (!entry) return;
  if (entry.isFile) {
    const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
    if (TEXT_FILE.test(file.name)) {
      out.push({ path: `${prefix}${file.name}`, content: await file.text() });
    }
    return;
  }
  if (entry.isDirectory) {
    const reader = entry.createReader();
    const children = await new Promise((resolve) => {
      const all = [];
      const read = () => reader.readEntries((batch) => {
        if (!batch.length) { resolve(all); return; }
        all.push(...batch);
        read();
      }, () => resolve(all));
      read();
    });
    for (const child of children) {
      await readEntry(child, `${prefix}${entry.name}/`, out);
    }
  }
}

function stripCommonRoot(files) {
  if (!files.length) return files;
  const segments = files.map((f) => f.path.split('/'));
  const first = segments[0][0];
  const shared = segments.every((parts) => parts.length > 1 && parts[0] === first);
  if (!shared) return files;
  return files.map((f) => ({ ...f, path: f.path.slice(first.length + 1) }));
}

function SkillImportForm({ onClose, notify, refresh }) {
  const [name, setName] = useState('');
  const [mode, setMode] = useState('passive');
  const [files, setFiles] = useState([]);
  const [dragOver, setDragOver] = useState(false);
  const [busy, setBusy] = useState(false);
  const [busyFiles, setBusyFiles] = useState(false);

  const addFiles = async (incoming) => {
    setBusyFiles(true);
    try {
      const firstPath = incoming[0]?.path || '';
      const rootSeg = firstPath.split('/')[0];
      const folderName = rootSeg && !rootSeg.includes('.') ? rootSeg : '';
      const bundle = stripCommonRoot(incoming.filter((f) => f.path && TEXT_FILE.test(f.path)));
      if (!bundle.length) {
        notify('No markdown or text files found');
        return;
      }
      const total = bundle.reduce((n, f) => n + f.content.length, 0);
      if (bundle.length > 500 || total > 1048576) {
        notify('Bundle is too large (500 files / 1 MiB max)');
        return;
      }
      setFiles((prev) => {
        const seen = new Set(prev.map((f) => f.path));
        return [...prev, ...bundle.filter((f) => !seen.has(f.path))];
      });
      if (!name) setName(folderName || 'Imported skill');
    } finally {
      setBusyFiles(false);
    }
  };

  const onPick = async (e) => {
    const list = Array.from(e.currentTarget.files || []);
    const bundle = [];
    for (const file of list) {
      if (!TEXT_FILE.test(file.name)) continue;
      const path = (file.webkitRelativePath || file.name).replace(/^\.?\//, '');
      bundle.push({ path, content: await file.text() });
    }
    await addFiles(bundle);
    e.currentTarget.value = '';
  };

  const onDrop = async (e) => {
    e.preventDefault();
    setDragOver(false);
    const items = Array.from(e.dataTransfer.items || []);
    const entries = items.map((item) => item.webkitGetAsEntry && item.webkitGetAsEntry()).filter(Boolean);
    if (!entries.length) {
      const list = Array.from(e.dataTransfer.files || []);
      const bundle = [];
      for (const file of list) {
        if (!TEXT_FILE.test(file.name)) continue;
        bundle.push({ path: file.name, content: await file.text() });
      }
      await addFiles(bundle);
      return;
    }
    const out = [];
    for (const entry of entries) {
      await readEntry(entry, '', out);
    }
    await addFiles(out);
  };

  const submit = async (e) => {
    e.preventDefault();
    if (!files.length) { notify('Add at least one file'); return; }
    setBusy(true);
    try {
      await api('/api/skills', {
        method: 'POST',
        body: JSON.stringify({
          name: name.trim() || files[0].path.split('/')[0] || 'Imported skill',
          description: `Imported ${files.length} file${files.length === 1 ? '' : 's'}.`,
          mode,
          enabled: mode === 'active',
          files,
        }),
      });
      notify('Skill imported');
      await refresh();
      onClose();
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };

  return (
    <Backdrop onClose={onClose} title="Import skill" submitLabel={busyFiles ? 'Reading…' : 'Import skill'}
      onSubmit={submit} busy={busy || busyFiles} wide>
      <div class="form-grid">
        <div class="field">
          <label>Skill name</label>
          <input maxlength="100" placeholder="e.g. Release checklist" value={name}
            onInput={(e) => setName(e.currentTarget.value)} />
        </div>
        <div class="field">
          <label>Mode</label>
          <select value={mode} onChange={(e) => setMode(e.currentTarget.value)}>
            <option value="passive">Passive — reference only</option>
            <option value="active">Active — injected into context</option>
          </select>
        </div>
      </div>

      <div
        class={`dropzone ${dragOver ? 'over' : ''}`}
        onDragOver={(e) => { e.preventDefault(); setDragOver(true); }}
        onDragLeave={() => setDragOver(false)}
        onDrop={onDrop}
      >
        <Icon name="upload" size={26} />
        <strong>Drag a folder or markdown files here</strong>
        <small>Nested folders are preserved. Only text and .md files are read.</small>
        <div class="import-actions">
          <label class="btn small">
            <Icon name="folder" size={14} />Choose folder
            <input type="file" multiple webkitdirectory style="display:none" onChange={onPick} />
          </label>
          <label class="btn small">
            <Icon name="file-text" size={14} />Choose files
            <input type="file" multiple accept=".md,.markdown,.txt,.json,.yaml,.yml,.toml,.sh,.py,.js,.ts,.go,.css,.html" style="display:none" onChange={onPick} />
          </label>
        </div>
      </div>

      {files.length > 0 && (
        <div class="file-list">
          {files.map((f) => (
            <div class="file-item">
              <Icon name="file-text" size={13} />
              <span class="file-path">{esc(f.path)}</span>
              <span class="file-size">{f.content.length} B</span>
              <button type="button" class="icon-btn" aria-label="Remove"
                onClick={() => setFiles((prev) => prev.filter((x) => x.path !== f.path))}>
                <Icon name="x" size={13} />
              </button>
            </div>
          ))}
        </div>
      )}

      <div class="notice">Imported files are stored locally with the skill. Paths cannot escape the skill root.</div>
    </Backdrop>
  );
}

/* ---------- Misc ---------- */

function AttachContext({ onClose, notify }) {
  const [ref, setRef] = useState('');
  const [why, setWhy] = useState('');
  const submit = (e) => {
    e.preventDefault();
    if (!ref.trim()) { notify('Add a path or reference'); return; }
    notify(`Context reference attached: ${ref.trim()}`);
    onClose();
  };
  return (
    <Backdrop onClose={onClose} title="Attach context" submitLabel="Attach" onSubmit={submit} busy={false}>
      <div class="field">
        <label>Path, URL, or reference</label>
        <input required placeholder="e.g. src/checkout or issue #42" value={ref} onInput={(e) => setRef(e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Why is this relevant?</label>
        <input placeholder="Optional context for the agent" value={why} onInput={(e) => setWhy(e.currentTarget.value)} />
      </div>
      <div class="notice">Context references are noted on the room. A run validates access before using them.</div>
    </Backdrop>
  );
}

function RouteAgent({ agents, current, onClose, onPick }) {
  return (
    <Backdrop onClose={onClose} title="Route your message" submitLabel="Apply"
      onSubmit={(e) => { e.preventDefault(); onClose(); }} busy={false}>
      <div class="field">
        <label>Who should receive this message?</label>
        <div class="choice-grid">
          {agents.length
            ? agents.map((t) => (
              <label class={`choice ${t.mainAgent === current ? 'selected' : ''}`}>
                <input type="radio" name="routeAgent" checked={t.mainAgent === current}
                  onChange={() => { if (onPick) onPick(t.mainAgent); onClose(); }} />
                <span><b>{esc(t.mainAgent)}</b><br /><small>{esc(t.purpose)}</small></span>
              </label>
            ))
            : <span class="muted">Invite a team to this room to route messages to its agents.</span>}
        </div>
      </div>
      <small>Agents coordinate by default. Selecting a specialist routes the message to that team's context.</small>
    </Backdrop>
  );
}

function EffortPicker({ current, onClose }) {
  const options = [
    ['low', 'Single agent first; minimal planning.'],
    ['adaptive', 'Scale up only when complexity warrants it.'],
    ['high', 'Plan, delegate, verify, and review.'],
    ['manual', 'Wait for explicit task and agent choices.'],
  ];
  return (
    <Backdrop onClose={onClose} title="Execution effort" submitLabel="Done"
      onSubmit={(e) => { e.preventDefault(); onClose(); }} busy={false}>
      <div class="field">
        <label>Scheduling policy</label>
        <p class="section-note">Set the workspace-wide default in Settings. Per-room overrides arrive with the scheduler.</p>
        <div class="choice-grid">
          {options.map(([label, sub]) => (
            <label class={`choice ${String(current).toLowerCase().includes(label) ? 'selected' : ''}`}>
              <input type="radio" name="effort" checked={String(current).toLowerCase().includes(label)} readOnly />
              <span><b>{esc(label)}</b><br /><small>{esc(sub)}</small></span>
            </label>
          ))}
        </div>
      </div>
    </Backdrop>
  );
}

function ApprovalInfo({ required, onClose }) {
  return (
    <Backdrop onClose={onClose} title="Approval policy" submitLabel="Done"
      onSubmit={(e) => { e.preventDefault(); onClose(); }} busy={false}>
      <div class="field">
        <label>Require approval before execution</label>
        <p class="section-note">
          {required
            ? 'This workspace requires explicit approval before a run starts. Change it in Settings → Execution policy.'
            : 'Runs start without an extra approval step. Enable it in Settings → Execution policy.'}
        </p>
        <span class={`tag ${required ? 'orange' : 'green'}`}>{required ? 'Approval required' : 'Auto-run enabled'}</span>
      </div>
    </Backdrop>
  );
}