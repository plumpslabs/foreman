import { useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';

const esc = (s) => String(s ?? '');

const TEXT_FILE = /\.(md|markdown|txt|json|ya?ml|toml|sh|bash|py|js|ts|tsx|jsx|go|css|html|xml|csv|ini|cfg|rst|adoc)$/i;

function Backdrop({ onClose, title, submitLabel, onSubmit, busy, children, wide }) {
  return (
    <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <form class={`modal ${wide ? 'modal-lg' : ''}`} onSubmit={onSubmit}>
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
      {kind === 'task' && (
        <TaskForm room={state.rooms.find((r) => r.id === modal.roomId)} state={state} task={modal.task}
          onClose={onClose} notify={notify} refresh={refresh} />
      )}
      {kind === 'skill-import' && <SkillImportForm onClose={onClose} notify={notify} refresh={refresh} />}
      {kind === 'attach' && <AttachContext onClose={onClose} notify={notify} />}
      {kind === 'route' && <RouteAgent agents={modal.agents || []} current={modal.current} onClose={onClose} onPick={modal.onPick} />}
      {kind === 'effort' && <EffortPicker current={modal.current} onClose={onClose} />}
      {kind === 'approval' && <ApprovalInfo required={modal.required} onClose={onClose} />}
    </>
  );
}

/* ---------- Room ---------- */

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
        <div class="team-picker">
          {teams.length
            ? teams.map((t) => (
              <label class={`team-pick ${teamIds.includes(t.id) ? 'on' : ''}`}>
                <input type="checkbox" checked={teamIds.includes(t.id)} onChange={() => toggleTeam(t.id)} />
                <span class="team-pick-body">
                  <span class="team-pick-name">{esc(t.name)}</span>
                  <span class="team-pick-desc">{esc(t.purpose)}</span>
                </span>
              </label>
            ))
            : <div class="empty" style="padding:16px">No teams yet. You can invite a team after creating one.</div>}
        </div>
      </div>
      <div class="notice">This creates a local room record only. No agent is started and no code is changed.</div>
    </Backdrop>
  );
}

/* ---------- Task ---------- */

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

/* ---------- Skill import ---------- */

async function readEntry(entry, prefix, out) {
  if (!entry) return;
  if (entry.isFile) {
    const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
    if (TEXT_FILE.test(file.name)) out.push({ path: `${prefix}${file.name}`, content: await file.text() });
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
    for (const child of children) await readEntry(child, `${prefix}${entry.name}/`, out);
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
      if (!bundle.length) { notify('No markdown or text files found'); return; }
      const total = bundle.reduce((n, f) => n + f.content.length, 0);
      if (bundle.length > 500 || total > 1048576) { notify('Bundle is too large (500 files / 1 MiB max)'); return; }
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
    for (const entry of entries) await readEntry(entry, '', out);
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

      <div class={`dropzone ${dragOver ? 'over' : ''}`}
        onDragOver={(e) => { e.preventDefault(); setDragOver(true); }}
        onDragLeave={() => setDragOver(false)}
        onDrop={onDrop}>
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