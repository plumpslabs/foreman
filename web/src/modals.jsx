import { useState } from 'preact/hooks';
import { api } from './api.js';

const esc = (s) => String(s ?? '');

function Backdrop({ onClose, title, submitLabel, onSubmit, busy, children }) {
  return (
    <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <form class="modal" onSubmit={onSubmit}>
        <header class="modal-head">
          <h2>{title}</h2>
          <button type="button" class="button" aria-label="Close" onClick={onClose}>×</button>
        </header>
        <div class="modal-body">{children}</div>
        <footer class="modal-foot">
          <button type="button" class="button" onClick={onClose}>Cancel</button>
          <button type="submit" class="button primary" disabled={!!busy}>{submitLabel}</button>
        </footer>
      </form>
    </div>
  );
}

function RoomForm({ teams, onClose, notify, refresh }) {
  const [name, setName] = useState('');
  const [goal, setGoal] = useState('');
  const [teamIds, setTeamIds] = useState([]);
  const [busy, setBusy] = useState(false);

  const toggleTeam = (id) => {
    setTeamIds((prev) => (prev.includes(id) ? prev.filter((item) => item !== id) : [...prev, id]));
  };

  const submit = async (event) => {
    event.preventDefault();
    setBusy(true);
    try {
      await api('/api/rooms', { method: 'POST', body: JSON.stringify({ name: name.trim(), goal, teamIds }) });
      notify(`Room created: ${name.trim()}`);
      await refresh();
      onClose();
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
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
        <div class="checks">
          {teams.length
            ? teams.map((team) => (
              <label class="check">
                <input type="checkbox" checked={teamIds.includes(team.id)} onChange={() => toggleTeam(team.id)} />
                <span><b>{esc(team.name)}</b><br />{esc(team.purpose)}</span>
              </label>
            ))
            : <span class="muted">No teams yet. You can invite a team later.</span>}
        </div>
      </div>
      <div class="notice">This creates a local room record only. No agent is started and no code is changed.</div>
    </Backdrop>
  );
}

export function ModalHost({ modal, setModal, state, notify, refresh }) {
  if (!modal) return null;
  const onClose = () => setModal(null);
  return (
    <>
      {modal.kind === 'room' && <RoomForm teams={state.teams} onClose={onClose} notify={notify} refresh={refresh} />}
      {modal.kind === 'task' && (
        <TaskForm room={state.rooms.find((room) => room.id === modal.roomId)} state={state}
          onClose={onClose} notify={notify} refresh={refresh} />
      )}
      {modal.kind === 'skill' && <SkillForm onClose={onClose} notify={notify} refresh={refresh} />}
      {modal.kind === 'team' && (
        <TeamImportForm template={state.templates.find((template) => template.id === modal.templateId)}
          onClose={onClose} notify={notify} refresh={refresh} />
      )}
    </>
  );
}

function TaskForm({ room, state, onClose, notify, refresh }) {
  const [form, setForm] = useState({
    title: '', description: '', priority: 'normal', effort: 'medium', teamId: '', dependencies: [],
  });
  const [busy, setBusy] = useState(false);
  if (!room) return null;
  const available = state.tasks.filter((task) => task.roomId === room.id);
  const teams = state.teams.filter((team) => room.teamIds.includes(team.id));

  const set = (field, value) => setForm((prev) => ({ ...prev, [field]: value }));

  const submit = async (event) => {
    event.preventDefault();
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
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
  };

  const toggleDependency = (id) => {
    set('dependencies', form.dependencies.includes(id)
      ? form.dependencies.filter((item) => item !== id)
      : [...form.dependencies, id]);
  };

  return (
    <Backdrop onClose={onClose} title="Create room task" submitLabel="Create task" onSubmit={submit} busy={busy}>
      <div class="field">
        <label>Task title</label>
        <input required maxlength="200" placeholder="Implement a bounded change"
          value={form.title} onInput={(e) => set('title', e.currentTarget.value)} />
      </div>
      <div class="field">
        <label>Description</label>
        <textarea maxlength="16000" placeholder="What should be done and verified?"
          value={form.description} onInput={(e) => set('description', e.currentTarget.value)} />
      </div>
      <div class="settings-grid">
        <div class="field">
          <label>Priority</label>
          <select value={form.priority} onChange={(e) => set('priority', e.currentTarget.value)}>
            <option>normal</option><option>low</option><option>high</option><option>urgent</option>
          </select>
        </div>
        <div class="field">
          <label>Effort</label>
          <select value={form.effort} onChange={(e) => set('effort', e.currentTarget.value)}>
            <option>low</option><option>medium</option><option>high</option>
          </select>
        </div>
        <div class="field">
          <label>Team</label>
          <select value={form.teamId} onChange={(e) => set('teamId', e.currentTarget.value)}>
            <option value="">Workspace default</option>
            {teams.map((team) => <option value={esc(team.id)}>{esc(team.name)}</option>)}
          </select>
        </div>
        <div class="field">
          <label>Dependencies</label>
          <select multiple size="3" value={form.dependencies}
            onChange={(e) => set('dependencies', Array.from(e.currentTarget.selectedOptions).map((o) => o.value))}>
            {available.map((task) => <option value={esc(task.id)}>{esc(task.title)}</option>)}
          </select>
        </div>
      </div>
      <div class="notice">
        Task and dependencies are persisted in this room. It will not start until you click Run agent;
        approval policy and provider setup apply.
      </div>
    </Backdrop>
  );
}

function SkillForm({ onClose, notify, refresh }) {
  const [form, setForm] = useState({ name: '', description: '', instructions: '', enabled: true });
  const [busy, setBusy] = useState(false);
  const set = (field, value) => setForm((prev) => ({ ...prev, [field]: value }));

  const submit = async (event) => {
    event.preventDefault();
    setBusy(true);
    try {
      await api('/api/skills', { method: 'POST', body: JSON.stringify(form) });
      notify('Skill saved');
      await refresh();
      onClose();
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
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
        <span>Enabled (currently metadata only; skills are not injected into the runtime)</span>
      </label>
    </Backdrop>
  );
}

function TeamImportForm({ template, onClose, notify, refresh }) {
  const [name, setName] = useState(template ? template.name : '');
  const [busy, setBusy] = useState(false);
  if (!template) return null;

  const submit = async (event) => {
    event.preventDefault();
    setBusy(true);
    try {
      await api('/api/teams/import', { method: 'POST', body: JSON.stringify({ templateId: template.id, name: name.trim() }) });
      notify(`Imported ${name.trim()}`);
      await refresh();
      onClose();
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
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
        {template.agents.map((agent, index) => (
          <span>{index > 0 ? <br /> : null}{esc(agent.name)} — {esc(agent.role)}</span>
        ))}<br /><br />
        {esc(template.instructions)}
      </p>
      <div class="notice">
        Template imports are plain configuration data. They do not run code, start agents, or
        configure a provider.
      </div>
    </Backdrop>
  );
}
