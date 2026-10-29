import { useEffect, useState } from 'preact/hooks';
import { api } from './api.js';

const esc = (s) => String(s ?? '');

function RoomCard({ room, recent, onOpen }) {
  return (
    <article
      class="room clickable"
      role="button"
      tabindex="0"
      aria-label={`Open room ${room.name}`}
      onClick={() => onOpen(room.id)}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen(room.id); } }}
    >
      <div class="room-head">
        <h3>{esc(room.name)}</h3>
        <span class="status">{esc(room.status)}</span>
      </div>
      <p>{esc(room.goal || 'No goal provided.')}</p>
      <small>
        {(room.teamIds || []).length} team{(room.teamIds || []).length === 1 ? '' : 's'} ·{' '}
        {recent ? 'Open room →' : `Created ${new Date(room.createdAt).toLocaleString()} · Open room →`}
      </small>
    </article>
  );
}

export function HomePage({ state, openRoom, setModal }) {
  const rooms = state.rooms;
  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Local-first agent workspace</div>
          <h1 class="title">Make the work visible.</h1>
          <div class="sub">
            Create reusable teams, give each room its own goal and context, and keep execution honest.
            Runs execute through the local OpenHands engine with bounded iteration, time, and cost.
          </div>
        </div>
        <button class="button primary" onClick={() => setModal({ kind: 'room' })}>+ New room</button>
      </div>
      <div class="grid">
        <article class="card">
          <h3>Rooms</h3>
          <p>Separate conversations and project goals.</p>
          <strong>{rooms.length}</strong>
          <div class="muted">No demo activity is presented as live work.</div>
        </article>
        <article class="card">
          <h3>Teams</h3>
          <p>Reusable team definitions, independent of rooms.</p>
          <strong>{state.teams.length}</strong>
          <div class="muted">Importing makes an editable copy.</div>
        </article>
        <article class="card">
          <h3>Agent execution</h3>
          <p>Execution status comes from real runs only.</p>
          <span class="tag">OpenHands engine</span>
          <div class="notice">
            Runs need a configured provider and Python with the OpenHands SDK installed.
            No source code leaves this machine unless your provider endpoint does.
          </div>
        </article>
      </div>
      <div class="toolbar">
        <h2>Recent rooms</h2>
        <span class="muted">Rooms are stored in .foreman/workspace.json</span>
      </div>
      <div class="list">
        {rooms.length
          ? rooms.slice(0, 3).map((room) => <RoomCard key={room.id} room={room} recent onOpen={openRoom} />)
          : <div class="empty">No rooms yet. Create one to define a separate goal and invite teams.</div>}
      </div>
    </>
  );
}

export function RoomsPage({ state, openRoom, setModal }) {
  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Isolated work sessions</div>
          <h1 class="title">Rooms</h1>
          <div class="sub">
            A team's reusable definition can be invited into multiple rooms. Each room stores its own
            goal, team membership, and chat.
          </div>
        </div>
        <button class="button primary" onClick={() => setModal({ kind: 'room' })}>+ New room</button>
      </div>
      <div class="toolbar"><h2>All rooms</h2></div>
      <div class="list">
        {state.rooms.length
          ? state.rooms.map((room) => <RoomCard key={room.id} room={room} onOpen={openRoom} />)
          : <div class="empty">No rooms yet. Create one to define a separate goal and invite teams.</div>}
      </div>
    </>
  );
}

export function RoomDetailHost({ room, state, refresh, notify, setModal, runTask, setActiveRoomId }) {
  const teams = state.teams.filter((team) => room.teamIds.includes(team.id));
  const teamNames = teams.length ? teams.map((team) => team.name).join(' + ') : 'No teams invited';
  const roomTasks = state.tasks.filter((task) => task.roomId === room.id);
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);

  const send = async (event) => {
    event.preventDefault();
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

  return (
    <div class="detail active">
      <div class="toolbar">
        <button class="button" onClick={() => setActiveRoomId('')}>← All rooms</button>
        <span class="status">{esc(room.status)} · No run active</span>
      </div>
      <div class="panel">
        <div class="room-head">
          <h2 class="title" style="font-size:21px">{esc(room.name)}</h2>
          <span class="tag">{esc(teamNames)}</span>
        </div>
        <p>{esc(room.goal || 'No goal provided.')}</p>
        <div class="notice">
          Room chat is saved locally. Sending a chat message does not start an agent; create a task
          and choose Run agent to execute it.
        </div>
        <div class="toolbar">
          <h3>Room tasks ({roomTasks.length})</h3>
          <button class="button" onClick={() => setModal({ kind: 'task', roomId: room.id })}>+ Task</button>
        </div>
        <div class="list">
          {roomTasks.length
            ? roomTasks.map((task) => (
              <div class="task-card">
                <b>{esc(task.title)}</b> · <span class="status">{esc(task.status)}</span>
                <p>{esc(task.description)}</p>
                {task.status === 'todo' && (
                  <div class="inline-actions">
                    <button class="button small-button" onClick={() => runTask(task.id)}>Run agent</button>
                  </div>
                )}
              </div>
            ))
            : <div class="muted">No tasks yet.</div>}
        </div>
        <div class="chat-list">
          {(room.messages || []).length
            ? room.messages.map((message) => (
              <article class={`chat-message ${message.role === 'user' ? 'user' : ''}`}>
                <div class="chat-meta">
                  {message.role === 'user' ? 'You' : 'Foreman'} · {new Date(message.createdAt).toLocaleString()}
                </div>
                {esc(message.content)}
              </article>
            ))
            : <div class="empty">No messages yet. Write the room goal or a request below to start its saved conversation.</div>}
        </div>
        <form class="chat-compose" onSubmit={send}>
          <textarea
            maxlength="16000"
            required
            placeholder="Write a request for this room… (saved locally; no agent runs yet)"
            value={draft}
            onInput={(e) => setDraft(e.currentTarget.value)}
          />
          <button class="button primary" type="submit" disabled={sending}>Save message</button>
        </form>
      </div>
    </div>
  );
}

export function TeamsPage({ state, setModal }) {
  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Reusable agent teams</div>
          <h1 class="title">Teams</h1>
          <div class="sub">
            Start from a built-in template. Imports create independent, editable team copies; the
            source template stays unchanged.
          </div>
        </div>
      </div>
      <div class="grid">
        {state.templates.map((template) => (
          <article class="card">
            <span class="tag green">Template v{template.version}</span>
            <h3 style="margin-top:12px">{esc(template.name)}</h3>
            <p>{esc(template.purpose)}</p>
            <div class="muted">Main agent: {esc(template.mainAgent)} · {template.agents.length} roles</div>
            <button
              class="button primary"
              style="margin-top:14px"
              onClick={() => setModal({ kind: 'team', templateId: template.id })}
            >
              Import editable copy
            </button>
          </article>
        ))}
      </div>
      <div class="toolbar"><h2>Your teams</h2></div>
      <div class="list">
        {state.teams.length
          ? state.teams.map((team) => (
            <article class="room">
              <div class="room-head">
                <h3>{esc(team.name)}</h3>
                <span class="tag">{esc(team.templateId)} template</span>
              </div>
              <p>{esc(team.purpose)}</p>
              <small>
                Main: {esc(team.mainAgent)} · {team.agents.length} agent roles · Instructions included
              </small>
            </article>
          ))
          : <div class="empty">No teams imported. Choose a template above to create an editable copy.</div>}
      </div>
    </>
  );
}

const KANBAN_COLUMNS = [
  ['todo', 'To do'],
  ['blocked', 'Blocked'],
  ['in_progress', 'In progress'],
  ['review', 'Review'],
  ['done', 'Done'],
];

export function KanbanPage({ state, runTask, setTaskStatus }) {
  const tasks = state.tasks;
  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Persistent task board</div>
          <h1 class="title">Kanban</h1>
          <div class="sub">
            Tasks are scoped to rooms. Dependencies block execution until every prerequisite is done.
          </div>
        </div>
      </div>
      <div class="kanban">
        {KANBAN_COLUMNS.map(([key, title]) => {
          const columnTasks = tasks.filter((task) => task.status === key);
          return (
            <div class="kanban-col">
              <h3>{title} · {columnTasks.length}</h3>
              {columnTasks.length
                ? columnTasks.map((task) => {
                  const room = state.rooms.find((item) => item.id === task.roomId);
                  const depNames = (task.dependencies || [])
                    .map((id) => (tasks.find((item) => item.id === id) || {}).title || id);
                  return (
                    <article class="task-card">
                      <h4>{esc(task.title)}</h4>
                      <p>{esc(room ? room.name : 'Unknown room')} · {esc(task.priority)} · effort {esc(task.effort)}</p>
                      <p>{esc(task.description || '')}</p>
                      {depNames.length ? <small class="muted">Depends on: {depNames.map(esc).join(', ')}</small> : null}
                      {task.status === 'blocked' ? <div><span class="tag">Waiting for dependencies</span></div> : null}
                      <div class="inline-actions">
                        {task.status === 'todo' && (
                          <>
                            <button class="button small-button" onClick={() => setTaskStatus(task.id, 'review')}>Mark done</button>
                            <button class="button primary small-button" onClick={(e) => runTask(task.id, e.currentTarget)}>Run agent</button>
                          </>
                        )}
                        {task.status === 'review' && (
                          <button class="button primary small-button" onClick={() => setTaskStatus(task.id, 'done')}>Approve complete</button>
                        )}
                      </div>
                    </article>
                  );
                })
                : <div class="muted">No tasks</div>}
            </div>
          );
        })}
      </div>
    </>
  );
}

export function AgentsPage({ state }) {
  const profiles = [];
  for (const team of state.teams) {
    for (const agent of team.agents || []) {
      profiles.push({ ...agent, teamName: team.name, main: agent.name === team.mainAgent });
    }
  }
  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Agent library</div>
          <h1 class="title">Agents</h1>
          <div class="sub">
            Imported team agents and their roles are listed below. They are configuration profiles,
            not running processes.
          </div>
        </div>
      </div>
      <div class="toolbar"><h2>Configured team roles</h2></div>
      <div class="list">
        {profiles.length
          ? profiles.map((agent) => (
            <article class="room">
              <div class="room-head">
                <h3>
                  {esc(agent.name)}{' '}
                  {agent.main ? <span class="tag green">Main agent</span> : <span class="tag">Worker role</span>}
                </h3>
                <span class="tag">{esc(agent.teamName)}</span>
              </div>
              <p>{esc(agent.role)}</p>
              <small>Profile imported with team · Runs when its task starts</small>
            </article>
          ))
          : <div class="empty">No agent profiles yet. Import a team template; its roles will appear here.</div>}
      </div>
    </>
  );
}

export function SkillsPage({ state, setModal, deleteSkill }) {
  const skills = state.skills;
  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Reusable capabilities</div>
          <h1 class="title">Skills</h1>
          <div class="sub">
            Versioned instruction bundles are stored locally. Skills do not grant additional tools
            or permissions.
          </div>
        </div>
        <button class="button primary" onClick={() => setModal({ kind: 'skill' })}>+ New skill</button>
      </div>
      <div class="list">
        {skills.length
          ? skills.map((skill) => (
            <article class="room skill-card">
              <div class="room-head">
                <h3>{esc(skill.name)} <span class="tag">v{skill.version}</span></h3>
                <button class="button small-button" onClick={() => deleteSkill(skill.id)}>Delete</button>
              </div>
              <p>{esc(skill.description)}</p>
              <div class="muted">{esc(skill.instructions)}</div>
            </article>
          ))
          : <div class="empty">No workspace skills configured.</div>}
      </div>
    </>
  );
}

export function RunsPage({ state, cancelRun }) {
  const runs = state.runs;
  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Execution history</div>
          <h1 class="title">Runs</h1>
          <div class="sub">
            Status, event logs, duration, token count, and result are persisted from actual local
            engine runs.
          </div>
        </div>
      </div>
      <div class="list">
        {runs.length
          ? runs.map((run) => (
            <article class="room">
              <div class="room-head">
                <h3>{esc(run.status)} · {esc(run.model)}</h3>
                <span class="tag">{esc(run.provider)}</span>
              </div>
              <p>{esc(run.result || run.error || 'Run executing…')}</p>
              <small>
                Started {new Date(run.startedAt).toLocaleString()} ·{' '}
                {(run.tokens || 0).toLocaleString()} tokens · ${Number(run.costUsd || 0).toFixed(4)}
              </small>
              {run.status === 'running' && (
                <div class="inline-actions">
                  <button class="button small-button" onClick={() => cancelRun(run.id)}>Cancel run</button>
                </div>
              )}
              <details style="margin-top:8px">
                <summary class="muted">{(run.events || []).length} run events</summary>
                <div class="list" style="margin-top:8px">
                  {(run.events || []).map((event) => (
                    <div class="muted">
                      {new Date(event.createdAt).toLocaleTimeString()} · {esc(event.type)} · {esc(event.message)}
                    </div>
                  ))}
                </div>
              </details>
            </article>
          ))
          : <div class="empty">No runs have been executed in this workspace.</div>}
      </div>
    </>
  );
}

export function SettingsPage({ state, notify, refresh }) {
  const settings = state.settings;
  const [provider, setProvider] = useState({
    id: '', name: '', protocol: 'openai-compatible', baseUrl: '', models: '', defaultModel: '', enabled: true,
  });
  const [apiKey, setApiKey] = useState('');
  const [policy, setPolicy] = useState(settings);

  useEffect(() => { setPolicy(settings); }, [JSON.stringify(settings)]);

  const providerConfig = () => ({
    id: provider.id.trim(),
    name: provider.name.trim(),
    protocol: provider.protocol,
    baseUrl: provider.baseUrl.trim(),
    models: provider.models.split(',').map((s) => s.trim()).filter(Boolean),
    defaultModel: provider.defaultModel.trim(),
    enabled: provider.enabled,
  });

  const saveProvider = async (event) => {
    event.preventDefault();
    try {
      await api('/api/providers', { method: 'POST', body: JSON.stringify({ config: providerConfig(), apiKey }) });
      setApiKey('');
      await refresh();
      notify('Provider saved');
    } catch (error) {
      notify(error.message);
    }
  };

  const testProvider = async () => {
    try {
      const result = await api('/api/providers/test', {
        method: 'POST',
        body: JSON.stringify({ config: providerConfig(), apiKey }),
      });
      notify(`Connected: ${result.model}`);
    } catch (error) {
      notify(error.message);
    }
  };

  const savePolicy = async (event) => {
    event.preventDefault();
    try {
      await api('/api/settings', { method: 'PUT', body: JSON.stringify(policy) });
      await refresh();
      notify('Policy saved');
    } catch (error) {
      notify(error.message);
    }
  };

  const setPolicyField = (field, value, numeric = false) => {
    setPolicy((prev) => ({ ...prev, [field]: numeric ? Number(value) : value }));
  };

  return (
    <>
      <div class="head">
        <div>
          <div class="eyebrow">Workspace configuration</div>
          <h1 class="title">Settings</h1>
          <div class="sub">
            Provider keys are stored separately with local file permissions. Runs use bounded
            iteration, time, cost, approval, and tool-access policies.
          </div>
        </div>
      </div>
      <div class="panel">
        <h3>Providers and models</h3>
        <p>Configure OpenAI-compatible or Anthropic endpoints. The connection test makes a real provider request.</p>
        <form onSubmit={saveProvider}>
          <div class="settings-grid">
            <div class="field">
              <label>Provider ID</label>
              <input required maxlength="80" placeholder="openai-compatible"
                value={provider.id} onInput={(e) => setProvider({ ...provider, id: e.currentTarget.value })} />
            </div>
            <div class="field">
              <label>Display name</label>
              <input required maxlength="120" placeholder="Local gateway"
                value={provider.name} onInput={(e) => setProvider({ ...provider, name: e.currentTarget.value })} />
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
              <input required placeholder="http://127.0.0.1:11434/v1"
                value={provider.baseUrl} onInput={(e) => setProvider({ ...provider, baseUrl: e.currentTarget.value })} />
            </div>
            <div class="field">
              <label>Model IDs (comma-separated)</label>
              <input required placeholder="model-id"
                value={provider.models} onInput={(e) => setProvider({ ...provider, models: e.currentTarget.value })} />
            </div>
            <div class="field">
              <label>Default model</label>
              <input required placeholder="model-id"
                value={provider.defaultModel} onInput={(e) => setProvider({ ...provider, defaultModel: e.currentTarget.value })} />
            </div>
          </div>
          <div class="field">
            <label>API key (optional for local models)</label>
            <input type="password" autocomplete="new-password" placeholder="Leave blank to retain existing saved key"
              value={apiKey} onInput={(e) => setApiKey(e.currentTarget.value)} />
          </div>
          <label class="check">
            <input type="checkbox" checked={provider.enabled}
              onChange={(e) => setProvider({ ...provider, enabled: e.currentTarget.checked })} />
            <span>Enabled for task execution</span>
          </label>
          <div style="display:flex;gap:8px;margin-top:12px">
            <button class="button" type="button" onClick={testProvider}>Test connection</button>
            <button class="button primary" type="submit">Save provider</button>
          </div>
        </form>
        <div class="list" style="margin-top:16px">
          {state.providers.length
            ? state.providers.map((item) => (
              <article class="room">
                <div class="room-head">
                  <h3>
                    {esc(item.name)} <span class={`tag ${item.enabled ? 'green' : ''}`}>{item.enabled ? 'Enabled' : 'Disabled'}</span>
                  </h3>
                  <span class="tag">{esc(item.id)}</span>
                </div>
                <p>{esc(item.protocol)} · {esc(item.baseUrl)}</p>
                <small>
                  {(item.models || []).map(esc).join(', ')} · default {esc(item.defaultModel)} ·{' '}
                  {item.hasApiKey ? 'credential saved' : 'no credential'}
                </small>
              </article>
            ))
            : <div class="empty">No providers configured.</div>}
        </div>
      </div>

      <div class="panel" style="margin-top:12px">
        <h3>Execution policy and appearance</h3>
        <p>
          Maximum run concurrency is 1–8. Project commands (terminal) are only enabled when the
          policy allows them; the engine otherwise works on files and tasks alone.
        </p>
        <form onSubmit={savePolicy}>
          <div class="settings-grid">
            <div class="field">
              <label>Theme</label>
              <select value={policy.theme} onChange={(e) => setPolicyField('theme', e.currentTarget.value)}>
                <option>dark</option>
                <option>light</option>
                <option>system</option>
              </select>
            </div>
            <div class="field">
              <label>Effort mode</label>
              <select value={policy.effortMode} onChange={(e) => setPolicyField('effortMode', e.currentTarget.value)}>
                <option>automatic</option>
                <option>manual</option>
              </select>
            </div>
            <div class="field">
              <label>Default effort</label>
              <select value={policy.defaultEffort} onChange={(e) => setPolicyField('defaultEffort', e.currentTarget.value)}>
                <option>low</option>
                <option>medium</option>
                <option>high</option>
              </select>
            </div>
            <div class="field">
              <label>Concurrent runs</label>
              <input type="number" min="1" max="8" value={policy.maxConcurrentRuns}
                onInput={(e) => setPolicyField('maxConcurrentRuns', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Max workers per task</label>
              <input type="number" min="1" max="8" value={policy.maxWorkersPerTask}
                onInput={(e) => setPolicyField('maxWorkersPerTask', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Max delegation depth</label>
              <input type="number" min="0" max="5" value={policy.maxDelegationDepth}
                onInput={(e) => setPolicyField('maxDelegationDepth', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Max retries</label>
              <input type="number" min="0" max="5" value={policy.maxRetries}
                onInput={(e) => setPolicyField('maxRetries', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Run timeout (seconds)</label>
              <input type="number" min="10" max="3600" value={policy.maxExecutionSeconds}
                onInput={(e) => setPolicyField('maxExecutionSeconds', e.currentTarget.value, true)} />
            </div>
            <div class="field">
              <label>Budget (USD, 0 = unlimited)</label>
              <input type="number" min="0" step="0.01" value={policy.budgetUsd}
                onInput={(e) => setPolicyField('budgetUsd', e.currentTarget.value, true)} />
            </div>
          </div>
          <label class="check">
            <input type="checkbox" checked={policy.requireRunApproval}
              onChange={(e) => setPolicyField('requireRunApproval', e.currentTarget.checked)} />
            <span>Require explicit approval before runs</span>
          </label>
          <br />
          <label class="check">
            <input type="checkbox" checked={policy.allowProjectCommands}
              onChange={(e) => setPolicyField('allowProjectCommands', e.currentTarget.checked)} />
            <span>Allow project commands (terminal tool in the engine)</span>
          </label>
          <button class="button primary" style="margin-top:12px">Save policy</button>
        </form>
        <div class="notice">
          Runs are bounded by configured timeout, budget, and a 30-iteration engine cap. Delegation,
          retries, and worker fan-out are not yet implemented.
        </div>
        <p style="margin-top:14px">
          Workspace data: <code>.foreman/workspace.json</code> · API keys: <code>.foreman/credentials.json</code> (0600).
        </p>
      </div>
    </>
  );
}
