import { useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';
import { Markdown, STARTER_INSTRUCTIONS } from './markdown.jsx';

const esc = (s) => String(s ?? '');

/* Complete OpenHands tool catalog (researched from openhands-tools).
   `wired` marks tools Foreman's bridge actually loads today. */
export const TOOL_CATALOG = [
  {
    id: 'file_editor', label: 'File editor', sdk: 'FileEditorTool', group: 'Core', wired: true,
    desc: 'Create, view, and modify files in the workspace.',
  },
  {
    id: 'task_tracker', label: 'Task tracker', sdk: 'TaskTrackerTool', group: 'Core', wired: true,
    desc: 'Break work into tracked steps and update their status.',
  },
  {
    id: 'terminal', label: 'Terminal', sdk: 'TerminalTool', group: 'Core', wired: true, gated: 'allowProjectCommands',
    desc: 'Run shell commands, builds, and tests. Gated by the workspace "project commands" policy.',
  },
  {
    id: 'glob', label: 'Glob', sdk: 'GlobTool', group: 'Search', wired: false,
    desc: 'Find files by glob pattern.',
  },
  {
    id: 'grep', label: 'Grep', sdk: 'GrepTool', group: 'Search', wired: false,
    desc: 'Search file contents with regular expressions.',
  },
  {
    id: 'apply_patch', label: 'Apply patch', sdk: 'ApplyPatchTool', group: 'Editing', wired: false,
    desc: 'Apply unified diffs with exact line matching.',
  },
  {
    id: 'planning_editor', label: 'Planning file editor', sdk: 'PlanningFileEditorTool', group: 'Editing', wired: false,
    desc: 'Maintain a plan file across steps.',
  },
  {
    id: 'browser', label: 'Browser', sdk: 'BrowserToolSet', group: 'Optional', wired: false,
    desc: 'Navigate pages, read content, and interact with web UIs.',
  },
  {
    id: 'subagents', label: 'Sub-agents', sdk: 'TaskToolSet', group: 'Orchestration', wired: false,
    desc: 'Delegate a bounded task to a child agent.',
  },
  {
    id: 'workflow', label: 'Workflow', sdk: 'WorkflowToolSet', group: 'Orchestration', wired: false,
    desc: 'Run multi-step workflow definitions.',
  },
  {
    id: 'ask_oracle', label: 'Ask oracle', sdk: 'AskOracleTool', group: 'Orchestration', wired: false,
    desc: 'Ask a second model for a focused opinion.',
  },
  {
    id: 'tom_consult', label: 'Consult (Tom)', sdk: 'TomConsultTool', group: 'Orchestration', wired: false,
    desc: 'Consult a deliberation model before acting.',
  },
  {
    id: 'sleeptime', label: 'Sleeptime compute', sdk: 'SleeptimeComputeTool', group: 'Orchestration', wired: false,
    desc: 'Run background reasoning between steps.',
  },
];

const LEGACY_TOOL_ALIAS = {
  read: 'file_editor', edit: 'file_editor', write_tests: 'file_editor', diff_audit: 'apply_patch',
  search: 'grep', run_tests: 'terminal', run_commands: 'terminal',
  delegate: 'subagents', synthesize: 'task_tracker', preview: 'browser',
};

function normalizeTools(tools) {
  const out = [];
  for (const tool of tools || []) {
    const id = LEGACY_TOOL_ALIAS[tool] || tool;
    if (TOOL_CATALOG.some((t) => t.id === id) && !out.includes(id)) out.push(id);
  }
  return out;
}

/* ---------- Shared pieces ---------- */

function DetailShell({ kicker, title, subtitle, tabs, tab, onTab, onBack, actions, children }) {
  return (
    <div class="detail-page">
      <div class="detail-head">
        <div style="display:flex;align-items:flex-start;gap:10px;min-width:0">
          <button class="icon-btn" onClick={onBack} aria-label="Back"><Icon name="arrow-left" /></button>
          <div style="min-width:0">
            <div class="eyebrow">{esc(kicker)}</div>
            <h1 class="detail-title">{esc(title)}</h1>
            <div class="detail-sub">{esc(subtitle)}</div>
          </div>
        </div>
        <div class="detail-actions">{actions}</div>
      </div>
      <div class="detail-tabs">
        {tabs.map(([key, label, icon]) => (
          <button class={`detail-tab ${tab === key ? 'active' : ''}`} onClick={() => onTab(key)}>
            <Icon name={icon} size={15} />{label}
          </button>
        ))}
      </div>
      <div class="detail-body">{children}</div>
    </div>
  );
}

function MarkdownEditor({ value, onInput, placeholder, minHeight = 260 }) {
  const [mode, setMode] = useState('write');
  return (
    <div class="md-editor">
      <div class="md-editor-bar">
        <span class="md-editor-label"><Icon name="file-text" size={12} />Markdown</span>
        <div class="seg">
          <button type="button" class={`seg-btn ${mode === 'write' ? 'on' : ''}`} onClick={() => setMode('write')}>Write</button>
          <button type="button" class={`seg-btn ${mode === 'preview' ? 'on' : ''}`} onClick={() => setMode('preview')}>Preview</button>
        </div>
      </div>
      {mode === 'preview'
        ? <div class="md-preview" style={`min-height:${minHeight}px`}><Markdown text={value} /></div>
        : (
          <textarea
            class="code-area md-textarea"
            style={`min-height:${minHeight}px`}
            placeholder={placeholder}
            value={value}
            onInput={(e) => onInput(e.currentTarget.value)}
          />
        )}
    </div>
  );
}

function Field({ label, hint, children }) {
  return (
    <div class="field">
      <label>{esc(label)}</label>
      {children}
      {hint ? <div class="md-hint">{esc(hint)}</div> : null}
    </div>
  );
}

/* ---------- Agent ---------- */

export function AgentDetailPage({ agentId, state, notify, refresh, onBack }) {
  const existing = (state.agents || []).find((a) => a.id === agentId);
  const editing = Boolean(existing);
  const [form, setForm] = useState(() => ({
    id: existing?.id || '',
    name: existing?.name || '',
    role: existing?.role || '',
    description: existing?.description || '',
    instructions: existing?.instructions || '',
    model: existing?.model || '',
    providerId: existing?.providerId || '',
    effort: existing?.effort || '',
    skills: (existing?.skills || [])
      .map((ref) => {
        const match = (state.skills || []).find((s) => s.id === ref || s.name === ref);
        return match ? match.id : null;
      })
      .filter(Boolean),
    tools: normalizeTools(existing?.tools),
    requireReview: Boolean(existing?.requireReview),
  }));
  const [tab, setTab] = useState('overview');
  const [busy, setBusy] = useState(false);

  const set = (field, value) => setForm((prev) => ({ ...prev, [field]: value }));
  const toggle = (field, value) => set(field, form[field].includes(value)
    ? form[field].filter((x) => x !== value)
    : [...form[field], value]);

  const save = async () => {
    setBusy(true);
    try {
      const body = JSON.stringify({ ...form, name: form.name.trim(), role: form.role.trim() });
      if (editing) await api(`/api/agents/${encodeURIComponent(form.id)}`, { method: 'PUT', body });
      else await api('/api/agents', { method: 'POST', body });
      await refresh();
      notify(editing ? 'Agent saved' : 'Agent created');
      onBack();
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm(`Delete agent "${form.name}"? Teams keep their own copies.`)) return;
    try {
      await api(`/api/agents/${encodeURIComponent(form.id)}`, { method: 'DELETE' });
      await refresh();
      notify('Agent deleted');
      onBack();
    } catch (error) { notify(error.message); }
  };

  const groups = [...new Set(TOOL_CATALOG.map((t) => t.group))];

  return (
    <DetailShell
      kicker="Agent library"
      title={form.name || 'New agent'}
      subtitle={form.role || 'Define a role, instructions, tools, and skills.'}
      tabs={[
        ['overview', 'Overview', 'bot'],
        ['instructions', 'Instructions', 'file-text'],
        ['tools', 'Permissions', 'shield-check'],
        ['skills', 'Skills', 'puzzle'],
      ]}
      tab={tab}
      onTab={setTab}
      onBack={onBack}
      actions={(
        <>
          {editing && (
            <button class="btn" style="color:var(--red);border-color:#523030" onClick={remove}>
              <Icon name="trash" size={14} />Delete
            </button>
          )}
          <button class="btn primary" disabled={busy} onClick={save}>
            <Icon name="save" size={14} />{editing ? 'Save agent' : 'Create agent'}
          </button>
        </>
      )}
    >
      {tab === 'overview' && (
        <div class="detail-grid">
          <div class="field">
            <label>Agent name</label>
            <input maxlength="80" placeholder="e.g. Sentinel" value={form.name}
              onInput={(e) => set('name', e.currentTarget.value)} />
          </div>
          <div class="field">
            <label>Role</label>
            <input maxlength="160" placeholder="e.g. Security reviewer" value={form.role}
              onInput={(e) => set('role', e.currentTarget.value)} />
          </div>
          <div class="field" style="grid-column:1/-1">
            <label>Short description</label>
            <input maxlength="500" placeholder="What this agent is responsible for" value={form.description}
              onInput={(e) => set('description', e.currentTarget.value)} />
          </div>
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
          <div class="field">
            <label>Default effort</label>
            <select value={form.effort} onChange={(e) => set('effort', e.currentTarget.value)}>
              <option value="">Workspace default</option>
              <option value="low">low</option>
              <option value="medium">medium</option>
              <option value="high">high</option>
            </select>
          </div>
          <div class="field">
            <label>Review policy</label>
            <label class="check" style="margin:0">
              <input type="checkbox" checked={form.requireReview}
                onChange={(e) => set('requireReview', e.currentTarget.checked)} />
              <span>Require review of this agent's output</span>
            </label>
          </div>
        </div>
      )}

      {tab === 'instructions' && (
        <div class="detail-stack">
          <div class="detail-hint-row">
            <div>
              <div class="detail-section-title">System instructions</div>
              <div class="section-note">Stored as markdown and shown to the agent at run time.</div>
            </div>
            <button class="btn small" onClick={() => set('instructions', STARTER_INSTRUCTIONS)}>
              <Icon name="refresh" size={13} />Load starter template
            </button>
          </div>
          <MarkdownEditor
            value={form.instructions}
            onInput={(value) => set('instructions', value)}
            placeholder="# Role&#10;&#10;Describe responsibilities, workflow, guardrails, and output format."
          />
        </div>
      )}

      {tab === 'tools' && (
        <div class="detail-stack">
          <div class="detail-hint-row">
            <div>
              <div class="detail-section-title">Allowed tools</div>
              <div class="section-note">
                The OpenHands SDK ships these tools. A badge shows which ones Foreman's bridge wires today;
                the rest are recorded as intent for the agent.
              </div>
            </div>
          </div>
          {groups.map((group) => (
            <div class="form-section" key={group}>
              <div class="form-section-title">{group}</div>
              <div class="tool-list">
                {TOOL_CATALOG.filter((t) => t.group === group).map((tool) => (
                  <label class={`tool-row ${form.tools.includes(tool.id) ? 'on' : ''}`}>
                    <input type="checkbox" checked={form.tools.includes(tool.id)}
                      onChange={() => toggle('tools', tool.id)} />
                    <span class="tool-info">
                      <span class="tool-name">
                        {esc(tool.label)}
                        <code>{esc(tool.sdk)}</code>
                        {tool.wired
                          ? <span class="tag green">Wired</span>
                          : <span class="tag muted">SDK available</span>}
                        {tool.gated ? <span class="tag orange">Policy gated</span> : null}
                      </span>
                      <span class="tool-desc">{esc(tool.desc)}</span>
                    </span>
                  </label>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      {tab === 'skills' && (
        <div class="detail-stack">
          <div class="detail-hint-row">
            <div>
              <div class="detail-section-title">Attached skills</div>
              <div class="section-note">Active skills are intended for context injection; passive skills stay as reference.</div>
            </div>
          </div>
          <div class="tool-list">
            {(state.skills || []).length
              ? state.skills.map((skill) => (
                <label class={`tool-row ${form.skills.includes(skill.id) ? 'on' : ''}`}>
                  <input type="checkbox" checked={form.skills.includes(skill.id)}
                    onChange={() => toggle('skills', skill.id)} />
                  <span class="tool-info">
                    <span class="tool-name">
                      {esc(skill.name)}
                      <span class={`tag ${skill.mode === 'active' ? 'green' : 'muted'}`}>
                        {skill.mode === 'active' ? 'Active' : 'Passive'}
                      </span>
                      <span class="tag">v{esc(skill.version)}</span>
                    </span>
                    <span class="tool-desc">{esc(skill.description || 'No description.')}</span>
                  </span>
                </label>
              ))
              : <div class="empty">No skills yet. Create one on the Skills page.</div>}
          </div>
        </div>
      )}
    </DetailShell>
  );
}

/* ---------- Team ---------- */

export function TeamDetailPage({ teamId, state, notify, refresh, onBack, onOpenAgent }) {
  const existing = (state.teams || []).find((t) => t.id === teamId);
  if (!existing) return null;
  const [form, setForm] = useState({
    id: existing.id,
    templateId: existing.templateId,
    name: existing.name,
    purpose: existing.purpose,
    mainAgent: existing.mainAgent,
    instructions: existing.instructions || '',
    agents: (existing.agents || []).map((a) => ({ ...a })),
    createdAt: existing.createdAt,
  });
  const [tab, setTab] = useState('overview');
  const [busy, setBusy] = useState(false);
  const [pickAgent, setPickAgent] = useState('');
  const set = (field, value) => setForm((prev) => ({ ...prev, [field]: value }));
  const skillName = (ref) => {
    const match = (state.skills || []).find((s) => s.id === ref || s.name === ref);
    return match ? match.name : ref;
  };

  const save = async () => {
    setBusy(true);
    try {
      await api(`/api/teams/${encodeURIComponent(form.id)}`, { method: 'PUT', body: JSON.stringify(form) });
      await refresh();
      notify('Team saved');
      onBack();
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
  };

  const addAgent = () => {
    const source = (state.agents || []).find((a) => a.id === pickAgent);
    if (!source) return;
    if (form.agents.some((a) => a.name === source.name)) { notify('That agent is already on this team'); return; }
    set('agents', [...form.agents, { ...source, id: undefined }]);
    setPickAgent('');
  };

  const removeAgent = (name) => {
    const next = form.agents.filter((a) => a.name !== name);
    set('agents', next);
    if (form.mainAgent === name && next.length) set('mainAgent', next[0].name);
  };

  return (
    <DetailShell
      kicker="Team"
      title={form.name || 'Team'}
      subtitle={`${esc(form.templateId)} template · ${form.agents.length} agents`}
      tabs={[
        ['overview', 'Overview', 'users'],
        ['instructions', 'Instructions', 'file-text'],
        ['agents', `Agents (${form.agents.length})`, 'bot'],
      ]}
      tab={tab}
      onTab={setTab}
      onBack={onBack}
      actions={<button class="btn primary" disabled={busy} onClick={save}><Icon name="save" size={14} />Save team</button>}
    >
      {tab === 'overview' && (
        <div class="detail-grid">
          <div class="field">
            <label>Team name</label>
            <input maxlength="100" value={form.name} onInput={(e) => set('name', e.currentTarget.value)} />
          </div>
          <div class="field">
            <label>Main agent</label>
            <select value={form.mainAgent} onChange={(e) => set('mainAgent', e.currentTarget.value)}>
              {form.agents.map((a) => <option value={esc(a.name)}>{esc(a.name)}</option>)}
            </select>
          </div>
          <div class="field" style="grid-column:1/-1">
            <label>Purpose</label>
            <input maxlength="400" value={form.purpose} onInput={(e) => set('purpose', e.currentTarget.value)} />
          </div>
          <div class="field" style="grid-column:1/-1">
            <label>Template</label>
            <input value={form.templateId} readOnly />
          </div>
        </div>
      )}

      {tab === 'instructions' && (
        <div class="detail-stack">
          <div class="detail-hint-row">
            <div>
              <div class="detail-section-title">Team instructions</div>
              <div class="section-note">Shared conventions the whole team should follow.</div>
            </div>
          </div>
          <MarkdownEditor
            value={form.instructions}
            onInput={(value) => set('instructions', value)}
            placeholder="How should this team plan, delegate, and verify work?"
          />
        </div>
      )}

      {tab === 'agents' && (
        <div class="detail-stack">
          <div class="detail-hint-row">
            <div>
              <div class="detail-section-title">Agents on this team</div>
              <div class="section-note">Each entry is a full profile copy owned by this team.</div>
            </div>
            <div style="display:flex;gap:6px">
              <select value={pickAgent} onChange={(e) => setPickAgent(e.currentTarget.value)} style="max-width:200px">
                <option value="">Add from library…</option>
                {(state.agents || []).map((a) => <option value={esc(a.id)}>{esc(a.name)}</option>)}
              </select>
              <button class="btn small" onClick={addAgent}><Icon name="plus" size={13} />Add</button>
            </div>
          </div>
          {form.agents.map((agent) => (
            <div class="team-agent">
              <div class="team-agent-head">
                <div class="agent-avatar green" style="border-radius:6px">{esc(agent.name).slice(0, 2).toUpperCase()}</div>
                <div style="flex:1;min-width:0">
                  <div class="tool-name">
                    {esc(agent.name)}
                    {agent.name === form.mainAgent ? <span class="tag green">Main agent</span> : <span class="tag">Worker</span>}
                    {agent.requireReview ? <span class="tag orange">Requires review</span> : null}
                  </div>
                  <div class="tool-desc">{esc(agent.role)}</div>
                </div>
                <button class="icon-btn" aria-label="Remove agent" onClick={() => removeAgent(agent.name)}>
                  <Icon name="x" size={14} />
                </button>
              </div>
              {agent.description ? <div class="agent-detail-value">{esc(agent.description)}</div> : null}
              {agent.instructions ? (
                <details class="team-agent-details">
                  <summary>Instructions</summary>
                  <Markdown text={agent.instructions} />
                </details>
              ) : null}
              <div style="display:flex;gap:6px;flex-wrap:wrap;margin-top:8px">
                {(agent.tools || []).map((t) => <span class="chip">{esc(t)}</span>)}
                {(agent.skills || []).map((s) => <span class="chip">{esc(skillName(s))}</span>)}
              </div>
            </div>
          ))}
        </div>
      )}
    </DetailShell>
  );
}

/* ---------- Team create ---------- */

export function TeamCreatePage({ state, notify, refresh, onCreated, onBack }) {
  const [templateId, setTemplateId] = useState(state.templates[0]?.id || '');
  const [name, setName] = useState(state.templates[0]?.name || '');
  const [busy, setBusy] = useState(false);
  const template = state.templates.find((t) => t.id === templateId);

  const pick = (id) => {
    setTemplateId(id);
    const found = state.templates.find((t) => t.id === id);
    if (found) setName(found.name);
  };

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      const team = await api('/api/teams/import', {
        method: 'POST',
        body: JSON.stringify({ templateId, name: name.trim() }),
      });
      await refresh();
      notify(`Team created: ${name.trim()}`);
      onCreated(team.id);
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form class="detail-page" onSubmit={submit}>
      <div class="detail-head">
        <div style="display:flex;align-items:flex-start;gap:10px;min-width:0">
          <button type="button" class="icon-btn" onClick={onBack} aria-label="Back"><Icon name="arrow-left" /></button>
          <div>
            <div class="eyebrow">Reusable crew</div>
            <h1 class="detail-title">Create a team</h1>
            <div class="detail-sub">Start from a template, then customize agents and instructions on the team page.</div>
          </div>
        </div>
        <button class="btn primary" type="submit" disabled={busy || !templateId}>
          <Icon name="plus" size={14} />Create team
        </button>
      </div>

      <div class="detail-body">
        <Field label="Team name">
          <input required maxlength="100" value={name} onInput={(e) => setName(e.currentTarget.value)} />
        </Field>

        <div class="detail-section-title" style="margin-top:18px">Choose a template</div>
        <div class="template-pick">
          {state.templates.map((t) => (
            <label class={`template-option ${templateId === t.id ? 'on' : ''}`}>
              <input type="radio" name="template" checked={templateId === t.id} onChange={() => pick(t.id)} />
              <span>
                <b>{esc(t.name)}</b>
                <small>{esc(t.purpose)}</small>
                <span class="chip-row" style="margin-top:6px">
                  <span class="chip">Main: {esc(t.mainAgent)}</span>
                  <span class="chip">{t.agents.length} agents</span>
                </span>
              </span>
            </label>
          ))}
        </div>

        {template && (
          <div class="form-section" style="margin-top:16px">
            <div class="form-section-title">Agents in this template</div>
            <div class="chip-row">
              {template.agents.map((a) => <span class="chip">{esc(a.name)} · {esc(a.role)}</span>)}
            </div>
          </div>
        )}
      </div>
    </form>
  );
}

/* ---------- Skill ---------- */

function buildTree(files) {
  const root = { name: '', dirs: new Map(), files: [] };
  for (const file of files) {
    const parts = file.path.split('/').filter(Boolean);
    let node = root;
    for (let i = 0; i < parts.length - 1; i += 1) {
      if (!node.dirs.has(parts[i])) node.dirs.set(parts[i], { name: parts[i], dirs: new Map(), files: [] });
      node = node.dirs.get(parts[i]);
    }
    node.files.push({ name: parts[parts.length - 1] || file.path, path: file.path });
  }
  return root;
}

function TreeView({ node, prefix, expanded, onToggle, selected, onSelect }) {
  const dirs = [...node.dirs.values()].sort((a, b) => a.name.localeCompare(b.name));
  const files = [...node.files].sort((a, b) => a.name.localeCompare(b.name));
  return (
    <div class="tree">
      {dirs.map((dir) => {
        const path = prefix ? `${prefix}/${dir.name}` : dir.name;
        const open = expanded.has(path);
        return (
          <div class="tree-group">
            <button type="button" class={`tree-row tree-dir ${open ? 'open' : ''}`} onClick={() => onToggle(path)}>
              <Icon name="chevron-right" size={13} class="tree-caret" />
              <Icon name="folder" size={14} />
              <span class="tree-name">{esc(dir.name)}</span>
              <span class="tree-count">{dir.files.length + dir.dirs.size}</span>
            </button>
            {open && <div class="tree-children"><TreeView node={dir} prefix={path} expanded={expanded} onToggle={onToggle} selected={selected} onSelect={onSelect} /></div>}
          </div>
        );
      })}
      {files.map((file) => (
        <button
          type="button"
          class={`tree-row tree-file ${selected === file.path ? 'on' : ''}`}
          onClick={() => onSelect(file.path)}
        >
          <Icon name="file-text" size={13} />
          <span class="tree-name">{esc(file.name)}</span>
        </button>
      ))}
    </div>
  );
}

export function SkillDetailPage({ skillId, state, notify, refresh, onBack }) {
  const existing = (state.skills || []).find((s) => s.id === skillId);
  const editing = Boolean(existing);
  const initialFiles = existing?.files?.length
    ? existing.files.map((f) => ({ ...f }))
    : [{ path: 'SKILL.md', content: existing?.instructions || '' }];
  const [form, setForm] = useState({
    id: existing?.id || '',
    name: existing?.name || '',
    description: existing?.description || '',
    mode: existing?.mode || (existing?.enabled ? 'active' : 'passive'),
    instructions: existing?.instructions || '',
    files: initialFiles,
  });
  const [selected, setSelected] = useState(initialFiles[0]?.path || 'SKILL.md');
  const [expanded, setExpanded] = useState(() => new Set());
  const [busy, setBusy] = useState(false);
  const set = (field, value) => setForm((prev) => ({ ...prev, [field]: value }));

  const file = form.files.find((f) => f.path === selected);
  const updateFile = (path, patch) => set('files', form.files.map((f) => (f.path === path ? { ...f, ...patch } : f)));

  const addFile = () => {
    let n = form.files.length + 1;
    let path = `notes-${n}.md`;
    while (form.files.some((f) => f.path === path)) { n += 1; path = `notes-${n}.md`; }
    set('files', [...form.files, { path, content: '' }]);
    setSelected(path);
  };

  const addFolder = () => {
    const name = prompt('Folder name');
    if (!name) return;
    const clean = name.trim().replace(/^\/+|\/+$/g, '').replace(/\s+/g, '-');
    if (!clean) return;
    let path = `${clean}/notes.md`;
    let n = 1;
    while (form.files.some((f) => f.path === path)) { n += 1; path = `${clean}/notes-${n}.md`; }
    set('files', [...form.files, { path, content: `# ${clean}\n` }]);
    setExpanded((prev) => new Set([...prev, clean]));
    setSelected(path);
  };

  const removeFile = (path) => {
    if (form.files.length <= 1) { notify('A skill needs at least one file'); return; }
    const next = form.files.filter((f) => f.path !== path);
    set('files', next);
    if (selected === path) setSelected(next[0].path);
  };

  const renameFile = (oldPath, newPath) => {
    const clean = newPath.trim().replace(/^\/+/, '');
    if (!clean) return;
    if (form.files.some((f) => f.path === clean && f.path !== oldPath)) { notify('That path already exists'); return; }
    set('files', form.files.map((f) => (f.path === oldPath ? { ...f, path: clean } : f)));
    setSelected(clean);
  };

  const save = async () => {
    setBusy(true);
    try {
      const payload = {
        ...form,
        enabled: form.mode === 'active',
        files: form.files.filter((f) => f.path.trim()),
      };
      await api('/api/skills', { method: 'POST', body: JSON.stringify(payload) });
      await refresh();
      notify(editing ? 'Skill saved' : 'Skill created');
      onBack();
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm(`Delete skill "${form.name}"?`)) return;
    try {
      await api(`/api/skills/${encodeURIComponent(form.id)}`, { method: 'DELETE' });
      await refresh();
      notify('Skill deleted');
      onBack();
    } catch (error) { notify(error.message); }
  };

  const tree = buildTree(form.files);
  const toggleDir = (path) => setExpanded((prev) => {
    const next = new Set(prev);
    if (next.has(path)) next.delete(path); else next.add(path);
    return next;
  });

  return (
    <div class="detail-page">
      <div class="detail-head">
        <div style="display:flex;align-items:flex-start;gap:10px;min-width:0">
          <button class="icon-btn" onClick={onBack} aria-label="Back"><Icon name="arrow-left" /></button>
          <div>
            <div class="eyebrow">Skill bundle</div>
            <h1 class="detail-title">{form.name || 'New skill'}</h1>
            <div class="detail-sub">{form.files.length} files · {form.mode === 'active' ? 'active' : 'passive'}</div>
          </div>
        </div>
        <div class="detail-actions">
          {editing && (
            <button class="btn" style="color:var(--red);border-color:#523030" onClick={remove}>
              <Icon name="trash" size={14} />Delete
            </button>
          )}
          <button class="btn primary" disabled={busy} onClick={save}>
            <Icon name="save" size={14} />{editing ? 'Save skill' : 'Create skill'}
          </button>
        </div>
      </div>

      <div class="detail-body">
      <div class="detail-grid" style="margin-bottom:14px">
        <div class="field">
          <label>Name</label>
          <input maxlength="100" value={form.name} onInput={(e) => set('name', e.currentTarget.value)} />
        </div>
        <div class="field">
          <label>Mode</label>
          <select value={form.mode} onChange={(e) => set('mode', e.currentTarget.value)}>
            <option value="passive">Passive — reference only</option>
            <option value="active">Active — injected into context</option>
          </select>
        </div>
        <div class="field" style="grid-column:1/-1">
          <label>Description</label>
          <input maxlength="300" value={form.description} onInput={(e) => set('description', e.currentTarget.value)} />
        </div>
      </div>

      <div class="file-manager">
        <div class="file-sidebar">
          <div class="file-sidebar-head">
            <span>Files</span>
            <div style="display:flex;gap:4px">
              <button class="icon-btn" title="New file" onClick={addFile}><Icon name="file-text" size={14} /></button>
              <button class="icon-btn" title="New folder" onClick={addFolder}><Icon name="folder" size={14} /></button>
            </div>
          </div>
          <TreeView
            node={tree}
            prefix=""
            expanded={expanded}
            onToggle={toggleDir}
            selected={selected}
            onSelect={setSelected}
          />
        </div>

        <div class="file-main">
          {file ? (
            <>
              <div class="file-main-head">
                <input class="file-path-input" value={file.path}
                  onChange={(e) => renameFile(file.path, e.currentTarget.value)} />
                <button class="icon-btn" title="Delete file" onClick={() => removeFile(file.path)}>
                  <Icon name="trash" size={14} />
                </button>
              </div>
              <MarkdownEditor
                value={file.content}
                onInput={(value) => updateFile(file.path, { content: value })}
                placeholder="# Skill\n\nWrite the instructions for this file."
                minHeight={320}
              />
            </>
          ) : <div class="empty">Select a file to edit.</div>}
        </div>
      </div>
      </div>
    </div>
  );
}