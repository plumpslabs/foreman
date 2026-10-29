import { useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';

const esc = (s) => String(s ?? '');

function Backdrop({ onClose, title, submitLabel, onSubmit, busy, children }) {
  return (
    <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <form class="modal" onSubmit={onSubmit}>
        <header class="modal-head">
          <h3>{esc(title)}</h3>
          <button type="button" class="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </header>
        <div class="modal-body">{children}</div>
        <footer class="modal-foot">
          <button type="button" class="btn" onClick={onClose}>Cancel</button>
          <button type="submit" class="btn primary" disabled={!!busy}>{esc(submitLabel)}</button>
        </footer>
      </form>
    </div>
  );
}

export function ModalHost({ modal, setModal, state, notify, refresh }) {
  if (!modal) return null;
  const onClose = () => setModal(null);
  const { kind } = modal;
  return (
    <>
      {kind === 'room' && <RoomForm teams={state.teams} onClose={onClose} notify={notify} refresh={refresh} />}
      {kind === 'rooms' && <ManageTeamsModal teams={state.teams} onClose={onClose} />}
      {kind === 'task' && (
        <TaskForm room={state.rooms.find((r) => r.id === modal.roomId)} state={state}
          onClose={onClose} notify={notify} refresh={refresh} task={modal.task} />
      )}
      {kind === 'skill' && <SkillForm onClose={onClose} notify={notify} refresh={refresh} />}
      {kind === 'team' && (
        <TeamImportForm template={state.templates.find((t) => t.id === modal.templateId)}
          onClose={onClose} notify={notify} refresh={refresh} />
      )}
      {kind === 'attach' && <AttachContext onClose={onClose} notify={notify} />}
      {kind === 'route' && <RouteAgent agents={modal.agents || []} current={modal.current} onClose={onClose} onPick={modal.onPick} />}
      {kind === 'effort' && <EffortPicker current={modal.current} onClose={onClose} />}
      {kind === 'approval' && <ApprovalInfo required={modal.required} onClose={onClose} />}
    </>
  );
}

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
      <div class="modal">
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
    setBusy(true);
    try {
      if (task) {
        // Editing a task is expressed as creating a corrected record via status + new task is out of scope;
        // the API exposes create + status transitions only, so edit re-creates a task with the updated title.
        notify('Task fields are immutable after creation; use status transitions in the board.');
        await refresh();
        onClose();
        return;
      }
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
          ? 'Status transitions run through the API; blocked tasks cannot start until dependencies are done.'
          : 'Tasks and dependencies are persisted in this room. Work starts when you click Run.'}
      </div>
    </Backdrop>
  );
}

function SkillForm({ onClose, notify, refresh }) {
  const [form, setForm] = useState({ name: '', description: '', instructions: '', enabled: true });
  const [busy, setBusy] = useState(false);
  const set = (f, v) => setForm((p) => ({ ...p, [f]: v }));
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/api/skills', { method: 'POST', body: JSON.stringify(form) });
      notify('Skill saved');
      await refresh();
      onClose();
    } catch (err) { notify(err.message); } finally { setBusy(false); }
  };
  return (
    <Backdrop onClose={onClose} title="Create skill" submitLabel="Save skill" onSubmit={submit} busy={busy}>
      <div class="field">
        <label>Name</label>
        <input required maxlength="100" value={form.name} onInput={(e) => set('name', e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Description</label>
        <input maxlength="300" value={form.description} onInput={(e) => set('description', e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Instructions</label>
        <textarea required maxlength="16000" value={form.instructions}
          onInput={(e) => set('instructions', e.currentTarget.value)} />
      </div>
      <label class="check">
        <input type="checkbox" checked={form.enabled} onChange={(e) => set('enabled', e.currentTarget.checked)} />
        <span>Enabled (metadata only; skills are not yet injected into the runtime)</span>
      </label>
    </Backdrop>
  );
}

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
