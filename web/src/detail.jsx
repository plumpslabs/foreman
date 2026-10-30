import { useState } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';
import { Markdown, STARTER_INSTRUCTIONS } from './markdown.jsx';
import { AgentAvatar } from './avatar.jsx';

const esc = (s) => String(s ?? '');

// Preset role labels used when a role is picked from the dropdown. Kept in
// sync with roleTypeLabel in the Go store.
const ROLE_LABELS = {
  coordinator: 'Coordinator',
  specialist: 'Specialist',
  reviewer: 'Reviewer',
  custom: 'Custom agent',
};

/* Complete OpenHands tool catalog (researched from openhands-tools).
   `wired` marks tools Foreman's bridge actually loads today. */
export const TOOL_CATALOG = [
  {
    id: 'file_editor', label: 'File editor', sdk: 'FileEditorTool', group: 'Core', wired: true,
    desc: 'Create, view, and modify files in the workspace. Always loaded.',
  },
  {
    id: 'task_tracker', label: 'Task tracker', sdk: 'TaskTrackerTool', group: 'Core', wired: true,
    desc: 'Break work into tracked steps and update their status.',
  },
  {
    id: 'terminal', label: 'Terminal', sdk: 'TerminalTool', group: 'Core', wired: true, gated: 'allowProjectCommands',
    desc: 'Run shell commands, builds, and tests. Dropped unless the workspace allows project commands.',
  },
  {
    id: 'grep', label: 'Grep', sdk: 'GrepTool', group: 'Search', wired: true,
    desc: 'Search file contents with regular expressions.',
  },
  {
    id: 'glob', label: 'Glob', sdk: 'GlobTool', group: 'Search', wired: true,
    desc: 'Find files by glob pattern.',
  },
  {
    id: 'apply_patch', label: 'Apply patch', sdk: 'ApplyPatchTool', group: 'Editing', wired: true,
    desc: 'Apply unified diffs with exact line matching.',
  },
  {
    id: 'planning_editor', label: 'Planning file editor', sdk: 'PlanningFileEditorTool', group: 'Editing', wired: true,
    desc: 'Maintain a plan file across steps.',
  },
  {
    id: 'browser', label: 'Browser', sdk: 'BrowserToolSet', group: 'Optional', wired: true, optional: true,
    desc: 'Navigate pages and read content. Requires the browser-use package in the SDK install.',
  },
  {
    id: 'subagents', label: 'Sub-agents', sdk: 'TaskToolSet', group: 'Orchestration', wired: true, optional: true,
    desc: 'Delegate a bounded task to a child agent.',
  },
  {
    id: 'workflow', label: 'Workflow', sdk: 'WorkflowToolSet', group: 'Orchestration', wired: true, optional: true,
    desc: 'Run multi-step workflow definitions.',
  },
  {
    id: 'ask_oracle', label: 'Ask oracle', sdk: 'AskOracleTool', group: 'Orchestration', wired: true, optional: true,
    desc: 'Ask a second model for a focused opinion.',
  },
  {
    id: 'tom_consult', label: 'Consult (Tom)', sdk: 'TomConsultTool', group: 'Orchestration', wired: true, optional: true,
    desc: 'Consult a deliberation model before acting.',
  },
  {
    id: 'sleeptime', label: 'Sleeptime compute', sdk: 'SleeptimeComputeTool', group: 'Orchestration', wired: true, optional: true,
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

/* Model picker bound to the connected providers. The trigger shows the picked
   model as a badge with its source provider; clicking opens a searchable list
   of every configured provider's models. Picking a model from another provider
   also switches the provider. */
function ProviderModelField({ providerId, providers, model, onChange, onProviderChange }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const provider = providers.find((p) => p.id === providerId);
  const allModels = [];
  for (const p of providers) {
    for (const m of p.models || []) {
      if (!allModels.some((x) => x.model === m)) allModels.push({ model: m, provider: p });
    }
  }
  const ql = q.trim().toLowerCase();
  const filtered = ql
    ? allModels.filter((x) => x.model.toLowerCase().includes(ql) || (x.provider.name || '').toLowerCase().includes(ql))
    : allModels;
  const sourceName = provider ? provider.name : providerId || 'workspace default';

  const pick = (m, p) => {
    onChange(m);
    if (p && p.id !== providerId && onProviderChange) onProviderChange(p.id);
    setOpen(false);
    setQ('');
  };

  const addFallback = (m) => {
    const current = (model || '').trim();
    if (!current || current === 'auto') {
      onChange(m);
    } else {
      const parts = current.split(',').map((x) => x.trim()).filter(Boolean);
      if (!parts.includes(m)) {
        onChange([...parts, m].join(', '));
      }
    }
    setOpen(false);
    setQ('');
  };

  const isMulti = (model || '').includes(',');

  return (
    <div class="field" style="grid-column:1/-1">
      <label style="display:flex;justify-content:space-between">
        <span>Model selection & fallback chain</span>
        <span class="meta-text" style="font-size:10.5px">Multi-model / auto-rotation supported</span>
      </label>
      <div class="model-picker">
        <button type="button" class="model-trigger" onClick={() => setOpen(!open)}>
          <span class="model-badge">{esc(model || 'auto')}</span>
          <span class="model-source">{isMulti ? `${model.split(',').length} models in chain` : esc(sourceName)}</span>
          <span class={`model-caret ${open ? 'open' : ''}`}><Icon name="chevron-down" size={13} /></span>
        </button>
        {open && (
          <div class="model-pop">
            <input class="model-search" placeholder="Search models or providers…" value={q}
              onInput={(e) => setQ(e.currentTarget.value)} />
            <div class="model-groups">
              <button type="button" class="model-option" onClick={() => pick('', null)}>
                <span class="model-opt-name"><strong>auto</strong> (workspace & provider rotation)</span>
                <span class="model-opt-src">Auto fallback</span>
              </button>
              {filtered.map(({ model: m, provider: p }) => (
                <div class="model-option" key={`${p.id}-${m}`} style="display:flex;justify-content:space-between;align-items:center">
                  <div style="flex:1;cursor:pointer" onClick={() => pick(m, p)}>
                    <span class="model-opt-name">{esc(m)}</span>
                    <span class="model-opt-src">{esc(p.name)}</span>
                  </div>
                  {model && model !== 'auto' && !model.includes(m) ? (
                    <button type="button" class="btn small subtle" title="Add as fallback model" onClick={(e) => { e.stopPropagation(); addFallback(m); }}>
                      + fallback
                    </button>
                  ) : null}
                </div>
              ))}
              {ql && !filtered.some((x) => x.model.toLowerCase() === ql) && (
                <button type="button" class="model-option custom" onClick={() => pick(q.trim(), null)}>
                  <span class="model-opt-name">Use custom “{esc(q.trim())}”</span>
                </button>
              )}
            </div>
          </div>
        )}
      </div>
      <div style="margin-top:6px">
        <input
          class="field-input"
          style="font-size:12px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;width:100%"
          placeholder="e.g. gpt-4o, claude-3-5-sonnet (comma-separated fallback chain)"
          value={model}
          onInput={(e) => onChange(e.currentTarget.value)}
        />
      </div>
      <div class="md-hint" style="margin-top:4px">
        {!model || model === 'auto'
          ? <span class="meta-text">This agent uses the provider's default model. (Auto fallback chain: automatically rotates across provider models on failure)</span>
          : <span class="meta-text"><strong>Fallback chain:</strong> Models will be tried in order ({esc(model)}). If primary fails, Foreman automatically rotates to the next model.</span>}
      </div>
    </div>
  );
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
        return match ? match.id : ref;
      })
      .filter(Boolean),
    tools: normalizeTools(existing?.tools),
    requireReview: Boolean(existing?.requireReview),
    roleType: existing?.roleType || '',
    canSpawn: existing?.canSpawn || '',
    maxIterations: existing?.maxIterations || 0,
    readOnlyBudget: existing?.readOnlyBudget || 0,
    avatarSeed: existing?.avatarSeed || '',
    avatarStyle: existing?.avatarStyle || '',
  }));
  const [tab, setTab] = useState('overview');
  const [busy, setBusy] = useState(false);

  const set = (field, value) => setForm((prev) => ({ ...prev, [field]: value }));
  const toggle = (field, value) => set(field, form[field].includes(value)
    ? form[field].filter((x) => x !== value)
    : [...form[field], value]);

  // Choosing a role keeps the descriptive title in sync: it is filled in when
  // empty or still equal to a preset label, but a hand-written title is kept.
  const setRoleType = (value) => setForm((prev) => {
    const next = { ...prev, roleType: value };
    const presetLabels = Object.values(ROLE_LABELS);
    if (!prev.role || presetLabels.includes(prev.role)) {
      next.role = value ? ROLE_LABELS[value] : '';
    }
    return next;
  });

  const shuffleAvatar = async () => {
    if (!form.id) return;
    setBusy(true);
    try {
      const updated = await api(`/api/agents/${encodeURIComponent(form.id)}/avatar/shuffle`, { method: 'POST' });
      setForm((prev) => ({ ...prev, avatarSeed: updated.avatarSeed || '', avatarStyle: updated.avatarStyle || '' }));
    } catch (error) {
      notify(error.message);
    } finally {
      setBusy(false);
    }
  };

  const save = async () => {
    setBusy(true);
    try {
      const roleTitle = form.role.trim() || (form.roleType ? ROLE_LABELS[form.roleType] : '') || 'Agent';
      const body = JSON.stringify({ ...form, name: form.name.trim(), role: roleTitle });
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
          <div class="field" style="grid-column:1/-1">
            <label>Avatar</label>
            <div style="display:flex;align-items:center;gap:14px">
              <AgentAvatar agent={{ id: form.id, name: form.name, avatarSeed: form.avatarSeed }}
                name={form.name || '?'} size={56} radius={12} style="font-size:20px" />
              <div style="display:flex;flex-direction:column;gap:6px">
                <button type="button" class="btn small" disabled={!editing || busy} onClick={shuffleAvatar}>
                  <Icon name="refresh" size={13} />Shuffle avatar
                </button>
                <div class="meta-text">
                  {editing
                    ? 'A random avatar is assigned automatically. Shuffle to get a different one.'
                    : 'A random avatar will be assigned when you create this agent.'}
                </div>
              </div>
            </div>
          </div>
          <div class="field">
            <label>Agent name</label>
            <input maxlength="80" placeholder="e.g. Sentinel" value={form.name}
              onInput={(e) => set('name', e.currentTarget.value)} />
          </div>
          <div class="field">
            <label>Role</label>
            <select value={form.roleType} onChange={(e) => setRoleType(e.currentTarget.value)}>
              <option value="">Auto (main agent = coordinator)</option>
              <option value="coordinator">Coordinator</option>
              <option value="specialist">Specialist</option>
              <option value="reviewer">Reviewer</option>
              <option value="custom">Custom…</option>
            </select>
            <div class="meta-text">Pick the agent's role, or choose Custom to write your own. Coordinators may delegate (and can still do small tasks themselves); specialists stay on task; reviewers check independently.</div>
          </div>
          <div class="field">
            <label>Role title{form.roleType === 'custom' ? '' : ' (optional)'}</label>
            <input maxlength="160" placeholder="e.g. Security reviewer" value={form.role}
              onInput={(e) => set('role', e.currentTarget.value)} />
            <div class="meta-text">Shown in team rosters. Leave blank to use the role name above.</div>
          </div>
          <div class="field" style="grid-column:1/-1">
            <label>Short description</label>
            <input maxlength="500" placeholder="What this agent is responsible for" value={form.description}
              onInput={(e) => set('description', e.currentTarget.value)} />
          </div>
          <div class="field">
            <label>Spawn authority</label>
            <select value={form.canSpawn} onChange={(e) => set('canSpawn', e.currentTarget.value)}>
              <option value="">Default (lead only)</option>
              <option value="request">Request (needs lead approval)</option>
              <option value="limited">Limited (may spawn directly)</option>
            </select>
            <div class="meta-text">Default keeps hierarchy: only the team lead spawns. "Request" lets this agent ask the lead to spawn. "Limited" lets it spawn teammates directly.</div>
          </div>
          <div class="field">
            <label>Provider</label>
            <select value={form.providerId} onChange={(e) => set('providerId', e.currentTarget.value)}>
              <option value="">Auto / workspace default</option>
              {(state.providers || []).map((p) => <option value={esc(p.id)}>{esc(p.name)}</option>)}
            </select>
          </div>
          <ProviderModelField providerId={form.providerId} providers={state.providers || []}
            model={form.model} onChange={(model) => set('model', model)}
            onProviderChange={(id) => set('providerId', id)} />
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
          <div class="field">
            <label>Action-round budget (0 = workspace default)</label>
            <input type="number" min="0" max="200" value={form.maxIterations}
              onInput={(e) => set('maxIterations', Number(e.currentTarget.value) || 0)} />
          </div>
          <div class="field">
            <label>Read-only exploration budget (0 = workspace default)</label>
            <input type="number" min="0" max="500" value={form.readOnlyBudget}
              onInput={(e) => set('readOnlyBudget', Number(e.currentTarget.value) || 0)} />
            <div class="meta-text">Consecutive read/search rounds allowed before this agent must plan, delegate, or act. Higher = more freedom to explore.</div>
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
                These tools are loaded into the OpenHands run for this agent. Terminal is dropped unless
                the workspace allows project commands; optional tools load when the SDK install provides them.
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
                        <span class="tag green">Wired</span>
                        {tool.gated ? <span class="tag orange">Policy gated</span> : null}
                        {tool.optional ? <span class="tag orange">Needs SDK extras</span> : null}
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
              <div class="detail-section-title">Attached skills ({form.skills.length} attached)</div>
              <div class="section-note">Centang skill yang ingin dilampirkan ke agent ini. Active skills akan disuntikkan ke dalam context agent saat task dijalankan.</div>
            </div>
          </div>
          <div class="tool-list">
            {(state.skills || []).length
              ? state.skills.map((skill) => {
                const isAttached = form.skills.includes(skill.id) || form.skills.includes(skill.name);
                return (
                  <label class={`tool-row ${isAttached ? 'on' : ''}`} key={skill.id}>
                    <input type="checkbox" checked={isAttached}
                      onChange={() => toggle('skills', skill.id)} />
                    <span class="tool-info">
                      <span class="tool-name">
                        {esc(skill.name)}
                        <span class={`tag ${skill.mode === 'active' ? 'green' : 'muted'}`}>
                          {skill.mode === 'active' ? 'Active' : 'Passive'}
                        </span>
                        <span class="tag">v{esc(skill.version)}</span>
                        {isAttached ? <span class="tag green">Attached</span> : null}
                      </span>
                      <span class="tool-desc">{esc(skill.description || 'No description.')}</span>
                    </span>
                  </label>
                );
              })
              : <div class="empty">No skills yet. Create one on the Skills page.</div>}
          </div>
        </div>
      )}
    </DetailShell>
  );
}

/* ---------- Team ---------- */

export function TeamDetailPage({ teamId, state, notify, refresh, onBack, onOpenAgent, onDelete }) {
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
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pickOpen, setPickOpen] = useState(false);
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

  const libraryAgents = (state.agents || []).filter((a) => !form.agents.some((x) => x.name === a.name));

  const pickFromLibrary = (source) => {
    set('agents', [...form.agents, { ...source, id: undefined }]);
    setPickOpen(false);
    notify(`Added ${source.name} to the team`);
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
      actions={<>
        {onDelete && !confirmDelete && (
          <button class="btn danger-outline" disabled={busy} onClick={() => setConfirmDelete(true)}>
            <Icon name="trash-2" size={14} />Delete team
          </button>
        )}
        {onDelete && confirmDelete && (
          <span style="display:flex;gap:6px;align-items:center">
            <span style="font-size:12px;color:var(--text-muted)">Hapus team ini?</span>
            <button class="btn danger" disabled={busy} onClick={async () => { setBusy(true); await onDelete(form.id); }}>
              Ya, hapus
            </button>
            <button class="btn" onClick={() => setConfirmDelete(false)}>Cancel</button>
          </span>
        )}
        <button class="btn primary" disabled={busy} onClick={save}><Icon name="save" size={14} />Save team</button>
      </>}
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
              <div class="section-note">Each entry is a full profile copy owned by this team. Pick which agent is the main one.</div>
            </div>
            <button class="btn small" onClick={() => setPickOpen(true)} disabled={!libraryAgents.length}>
              <Icon name="plus" size={13} />Add agent
            </button>
          </div>
          {form.agents.map((agent) => (
            <div class="team-agent">
              <div class="team-agent-head">
                <AgentAvatar agent={agent} name={agent.name} tone="green" radius={6} />
                <div style="flex:1;min-width:0">
                  <div class="tool-name">
                    <span class="agent-name-trunc" title={esc(agent.name)}>{esc(agent.name)}</span>
                    {agent.name === form.mainAgent ? <span class="tag green">Main agent</span> : <span class="tag">Worker</span>}
                    {agent.requireReview ? <span class="tag orange">Requires review</span> : null}
                  </div>
                  <div class="tool-desc">{esc(agent.role)}</div>
                </div>
                <label class="main-pick" title="Set as the team's main agent">
                  <input type="radio" name="team-main-agent" checked={agent.name === form.mainAgent}
                    onChange={() => set('mainAgent', agent.name)} />
                  <span>Make main</span>
                </label>
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
                {(agent.tools || []).map((t) => <span class="chip" title={esc(t)}>{esc(t)}</span>)}
                {(agent.skills || []).map((s) => <span class="chip" title={esc(skillName(s))}>{esc(skillName(s))}</span>)}
              </div>
            </div>
          ))}

          {pickOpen && (
            <div class="modal-backdrop open" onClick={(e) => { if (e.target === e.currentTarget) setPickOpen(false); }}>
              <div class="modal modal-lg">
                <header class="modal-head">
                  <h3>Add an agent to this team</h3>
                  <button type="button" class="icon-btn" aria-label="Close" onClick={() => setPickOpen(false)}><Icon name="x" /></button>
                </header>
                <div class="modal-body">
                  {libraryAgents.length ? (
                    <div class="agent-pick-grid">
                      {libraryAgents.map((a) => (
                        <button type="button" class="agent-pick-card" key={a.id} onClick={() => pickFromLibrary(a)}>
                          <AgentAvatar agent={a} name={a.name} tone="green" radius={8} />
                          <div class="agent-pick-name" title={esc(a.name)}>{esc(a.name)}</div>
                          <div class="agent-pick-role" title={esc(a.role)}>{esc(a.role || 'No role')}</div>
                          <div class="agent-pick-meta">
                            {(a.tools || []).length} tools · {(a.skills || []).length} skills
                          </div>
                        </button>
                      ))}
                    </div>
                  ) : (
                    <div class="empty">All library agents are already on this team.</div>
                  )}
                </div>
                <footer class="modal-foot">
                  <button type="button" class="btn" onClick={() => setPickOpen(false)}>Close</button>
                </footer>
              </div>
            </div>
          )}
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

function TreeView({ node, prefix, expanded, onToggle, selected, onSelect, onDelete }) {
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
              <span class="tree-name" title={path}>{esc(dir.name)}</span>
              <span class="tree-count">{dir.files.length + dir.dirs.size}</span>
            </button>
            {open && (
              <div class="tree-children">
                <TreeView node={dir} prefix={path} expanded={expanded} onToggle={onToggle}
                  selected={selected} onSelect={onSelect} onDelete={onDelete} />
              </div>
            )}
          </div>
        );
      })}
      {files.map((file) => (
        <div class={`tree-file ${selected === file.path ? 'on' : ''}`}>
          <button type="button" class="tree-file-main" onClick={() => onSelect(file.path)} title={file.path}>
            <Icon name="file-text" size={13} />
            <span class="tree-name">{esc(file.name)}</span>
          </button>
          <button type="button" class="tree-file-del" title={`Delete ${file.path}`}
            onClick={() => onDelete(file.path)}>
            <Icon name="trash" size={12} />
          </button>
        </div>
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

  const [creating, setCreating] = useState(null);

  const startCreate = (type) => setCreating({ type, value: '' });

  const submitCreate = () => {
    const value = (creating?.value || '').trim();
    if (!value) return;
    if (creating.type === 'folder') {
      const clean = value.replace(/^\/+|\/+$/g, '').replace(/\s+/g, '-');
      if (!clean) return;
      let path = `${clean}/notes.md`;
      let n = 1;
      while (form.files.some((f) => f.path === path)) { n += 1; path = `${clean}/notes-${n}.md`; }
      set('files', [...form.files, { path, content: `# ${clean}\n` }]);
      setExpanded((prev) => new Set([...prev, clean]));
      setSelected(path);
    } else {
      let path = value.includes('.') ? value : `${value}.md`;
      if (form.files.some((f) => f.path === path)) { notify('That path already exists'); return; }
      set('files', [...form.files, { path, content: '' }]);
      setSelected(path);
    }
    setCreating(null);
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
              <button class="icon-btn" title="New file" onClick={() => startCreate('file')}><Icon name="file-text" size={14} /></button>
              <button class="icon-btn" title="New folder" onClick={() => startCreate('folder')}><Icon name="folder" size={14} /></button>
            </div>
          </div>
          {creating && (
            <div class="tree-create">
              <Icon name={creating.type === 'folder' ? 'folder' : 'file-text'} size={13} />
              <input
                autoFocus
                value={creating.value}
                placeholder={creating.type === 'folder' ? 'folder-name' : 'path/to/file.md'}
                onInput={(e) => setCreating({ ...creating, value: e.currentTarget.value })}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') { e.preventDefault(); submitCreate(); }
                  if (e.key === 'Escape') setCreating(null);
                }}
              />
              <button class="icon-btn" title="Create" onClick={submitCreate}><Icon name="check" size={13} /></button>
              <button class="icon-btn" title="Cancel" onClick={() => setCreating(null)}><Icon name="x" size={13} /></button>
            </div>
          )}
          <TreeView
            node={tree}
            prefix=""
            expanded={expanded}
            onToggle={toggleDir}
            selected={selected}
            onSelect={setSelected}
            onDelete={removeFile}
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