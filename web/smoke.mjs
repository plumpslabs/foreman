// Smoke test: load the built dashboard bundle in jsdom with a mocked API and
// assert that the Preact app renders workspace content and the page flows work.
import { readFileSync, readdirSync } from 'node:fs';
import { JSDOM } from 'jsdom';

const dist = new URL('../internal/foreman/web/dist/', import.meta.url);
const files = [...readdirSync(dist), ...readdirSync(new URL('assets/', dist), { withFileTypes: true }).map((entry) => `assets/${entry.name}`)];
const html = readFileSync(new URL('index.html', dist), 'utf8');
const jsFile = files.find((name) => /(^|\/)index-[^/]+\.js$/.test(name));
if (!jsFile) {
  console.error('smoke: no built bundle found in web/dist');
  process.exit(1);
}

const workspace = {
  schemaVersion: 3,
  projectPath: '/tmp/demo',
  rooms: [{
    id: 'r1', name: 'Smoke Room', goal: 'g', teamIds: ['t1'], status: 'planning',
    messages: [
      { id: 'm1', role: 'agent', agentName: 'Quick Coordinator', content: 'saya siap', createdAt: new Date().toISOString() },
      { id: 'm2', role: 'agent', agentName: 'Implementer', content: 'OpenHands SDK is not installed in this Python environment. pip install openhands-sdk', createdAt: new Date().toISOString() },
      {
        id: 'm3', role: 'agent', agentName: 'Implementer',
        content: 'Root cause — 3 bugs found:\n\n| # | Location | Issue |\n| :-- | --- | --: |\n| A | `ticket-helpers.ts:335` | stale `awaitingResponse` keeps it yellow |\n| B | `BoardCard.tsx:117` | bypasses `getWaitingSide()` |\n',
        createdAt: new Date().toISOString(),
      },
    ],
    createdAt: new Date().toISOString(),
    questions: [{ id: 'q1', prompt: 'Proceed with the refactor?', options: ['Yes', 'No'], answer: '', createdAt: new Date().toISOString() }],
  }],
  approvals: [{
    id: 'ap1', roomId: 'r1', type: 'question', agentName: 'M', severity: 'warning',
    description: 'Proceed with the refactor?', payloadJson: '{}', status: 'pending',
    createdAt: new Date().toISOString(),
  }],
  teams: [{ id: 't1', templateId: 'quick-fix', name: 'Smoke Team', purpose: 'p', mainAgent: 'M', agents: [{ id: 'ta1', name: 'M', role: 'r', instructions: '# hi' }], instructions: 'i', createdAt: new Date().toISOString() }],
  templates: [{ id: 'quick-fix', version: 1, name: 'Quick Fix', purpose: 'p', mainAgent: 'M', agents: [{ name: 'M', role: 'r' }], instructions: 'i' }],
  providers: [{ id: 'tokenharbor', name: 'TokenHarbor', protocol: 'openai-compatible', baseUrl: 'https://tokenharbor.ai/v1', models: ['deepseek-v4.1-flash:free', 'claude-sonnet-5'], defaultModel: 'deepseek-v4.1-flash:free', hasApiKey: true, enabled: true }],
  tasks: [{ id: 'k1', roomId: 'r1', title: 'Smoke Task', description: 'd', status: 'todo', priority: 'normal', effort: 'medium', dependencies: [], approved: false, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }],
  runs: [{
    id: 'run1', roomId: 'r1', taskId: 'k1', provider: 'tokenharbor', model: 'deepseek-v4.1-flash:free',
    status: 'running', startedAt: new Date().toISOString(),
    events: [{ type: 'diff', message: 'diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1,2 +1,3 @@\n-old line\n+new line\n+extra line\n' }],
    tokens: 120,
    result: 'diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1,2 +1,3 @@\n-old line\n+new line\n+extra line\n',
  }],
  skills: [{ id: 's1', name: 'Docs', description: 'd', version: 1, instructions: '# Docs', mode: 'passive', enabled: false, files: [{ path: 'SKILL.md', content: '# Docs' }, { path: 'references/steps.md', content: 'step 1' }] }],
  agents: [{ id: 'a1', name: 'Scout', role: 'Researcher', description: 'd', instructions: '# Scout\n\n## Responsibilities\n\n- Inspect', tools: ['file_editor'], skills: [], requireReview: false }],
  settings: { theme: 'dark', effortMode: 'automatic', defaultEffort: 'medium', maxConcurrentRuns: 1, maxWorkersPerTask: 1, maxDelegationDepth: 0, maxRetries: 0, maxExecutionSeconds: 600, budgetUsd: 0, requireRunApproval: false, allowProjectCommands: false },
};

const dom = new JSDOM(html, { url: 'http://localhost:8080/?project=%2Ftmp%2Fdemo', runScripts: 'outside-only', pretendToBeVisual: true });
const { window } = dom;

const calls = [];
window.fetch = async (url, opts) => {
  const u = String(url);
  const method = opts?.method || 'GET';
  calls.push({ url: u, method, body: opts?.body || null });
  if (u.includes('/api/engine/status')) {
    return { ok: true, status: 200, json: async () => ({ installed: false, pythonVersion: 'Python 3.12.16', pipAvailable: true }) };
  }
  if (u.includes('/api/engine/install')) {
    return { ok: true, status: 200, json: async () => ({ ok: false, installed: false, error: 'Installasi gagal — lihat log di bawah.', hint: '', output: 'exit status 1\n...' }) };
  }
  return { ok: true, status: 200, json: async () => workspace };
};
window.matchMedia = window.matchMedia || (() => ({ matches: false, addListener() {}, removeListener() {} }));

const bundle = readFileSync(new URL(jsFile, dist), 'utf8');
try {
  window.eval(bundle);
} catch (error) {
  console.error('smoke: bundle threw during evaluation:', error);
  process.exit(1);
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 50));
const click = (el) => el && el.dispatchEvent(new window.MouseEvent('click', { bubbles: true }));
const byText = (selector, text) => [...window.document.querySelectorAll(selector)]
  .find((el) => el.textContent.trim().startsWith(text));

await flush();

const failures = [];
const body = window.document.body.innerHTML;
if (!body.includes('foreman.')) failures.push('brand missing');
if (!body.includes('Make the work visible.')) failures.push('home headline missing');
if (!body.includes('Smoke Room')) failures.push('room list not rendered');
if (!body.includes('Overview')) failures.push('navigation missing');
if (!body.includes('/tmp/demo')) failures.push('project path missing');
if (!/<svg/.test(body)) failures.push('icons are not rendering as SVG');
if (/&lt;i[^>]*data-lucide/.test(body)) failures.push('icon markup leaked as escaped text');
if (window.document.querySelectorAll('.nav .nav-btn').length < 7) failures.push('sidebar nav buttons missing');

// Room detail, then back to the list (regression: used to show "Room not found").
click(window.document.querySelector('.room-row.clickable'));
await flush();
let view = window.document.body.innerHTML;
for (const label of ['Chat', 'Kanban', 'Plan', 'Changes', 'Task progress', 'Execution policy', 'Room team']) {
  if (!view.includes(label)) failures.push(`room detail missing "${label}"`);
}
// Markdown tables must render as real tables, not a wall of pipes.
if (!window.document.querySelector('.md-table thead th')) failures.push('markdown table header missing');
const tableCells = [...window.document.querySelectorAll('.md-table tbody td')].map((el) => el.textContent.trim());
if (!tableCells.some((t) => t.includes('ticket-helpers.ts:335'))) failures.push('markdown table cells missing');
if (!window.document.querySelector('.md-table .md-a-right')) failures.push('table column alignment missing');
if (view.includes('|---|') || view.includes('| --: |')) failures.push('table delimiter row leaked as raw text');

if (!view.includes('Running')) failures.push('room runtime status missing');
// Stop-only UX: a running agent is stopped, never paused from the room view.
if (view.includes('Pause run')) failures.push('pause control should be removed (stop-only UX)');
if (!view.includes('Agent needs a decision')) failures.push('agent question card missing');
if (!view.includes('Proceed with the refactor?')) failures.push('question prompt missing');
// Picking an option must PUT the answer — the server resumes the asking agent
// from that request, so a click that only re-renders would stall the room.
calls.length = 0;
const optionBtn = window.document.querySelector('.question-options .btn');
if (!optionBtn) {
  failures.push('question option button missing');
} else {
  click(optionBtn);
  await flush();
  if (!calls.some((c) => c.method === 'PUT' && c.url.includes('/api/rooms/r1/questions/q1'))) {
    failures.push('answering a question did not PUT the answer');
  }
}
// A pending approval must be discoverable from anywhere (badge) and resolving
// it must reach the server — the backend delivers the decision to the agent
// that asked, so a click that only closed a modal would stall the room.
const approvalBadge = window.document.querySelector('.top-approval-btn');
if (!approvalBadge) {
  failures.push('approvals badge missing for a pending approval');
} else if (!approvalBadge.textContent.includes('Approvals (1)')) {
  failures.push('approvals badge does not count the pending item');
} else {
  click(approvalBadge);
  await flush();
  if (!window.document.querySelector('.approval-item')) failures.push('approvals inbox did not list the pending item');
  calls.length = 0;
  const approveBtn = byText('.approval-item .btn', 'Approve');
  if (!approveBtn) {
    failures.push('approve button missing in the approvals inbox');
  } else {
    click(approveBtn);
    await flush();
    const resolved = calls.find((c) => c.method === 'POST' && c.url.includes('/api/approvals/ap1/resolve'));
    if (!resolved) {
      failures.push('approving did not POST the resolution');
    } else if (!String(resolved.body).includes('approved')) {
      failures.push('resolution did not carry the approved status');
    }
  }
  const inboxClose = window.document.querySelector('.modal-backdrop .modal-head .icon-btn');
  if (inboxClose) { click(inboxClose); await flush(); }
}

if (!view.includes('Teams invited to this room')) failures.push('team-invited card missing');
if (!view.includes('Smoke Team')) failures.push('invited team name missing from card');
if (!window.document.querySelector('.live-agents .live-agent.live-on')) failures.push('live agent indicator missing');
if (!window.document.querySelector('.member-badge.main')) failures.push('member badges missing from the room rail');
if (window.document.querySelector('.compose-selects select')) failures.push('agent select should be removed from the composer');
if (!window.document.querySelector('.compose-route')) failures.push('compose routing hint missing');
if (window.location.hash !== '#/rooms/r1') failures.push('room route was not written to the hash');

// Enter-to-send + send queue: typing and pressing Enter must POST a message.
const composeTa = window.document.querySelector('.compose-shell textarea');
if (!composeTa) {
  failures.push('chat textarea missing');
} else {
  calls.length = 0;
  composeTa.value = 'hello from smoke';
  composeTa.dispatchEvent(new window.Event('input', { bubbles: true }));
  await flush();
  composeTa.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  await flush();
  const sentMsg = calls.some((c) => c.url.includes('/api/rooms/r1/messages') && c.method === 'POST');
  if (!sentMsg) failures.push('enter did not send the message');
}

// Agent reply bubble should show the real responding agent name.
const authored = [...window.document.querySelectorAll('.msg .msg-meta strong')].map((el) => el.textContent.trim());
if (!authored.includes('Quick Coordinator')) failures.push('agent reply does not show the responding agent name');

// SDK-missing banner should offer a one-click install.
if (!window.document.querySelector('.sdk-hint')) failures.push('sdk hint banner missing');
const installBtn = byText('.btn', 'Install engine');
if (!installBtn) {
  failures.push('install engine button missing');
} else {
  calls.length = 0;
  click(installBtn);
  await flush();
  if (!calls.some((c) => c.url.includes('/api/engine/install') && c.method === 'POST')) {
    failures.push('install engine button did not POST /api/engine/install');
  }
}

// @ mention autocomplete -> badge -> routed send.
calls.length = 0;
const ta2 = window.document.querySelector('.compose-shell textarea');
ta2.value = '@M ';
ta2.dispatchEvent(new window.Event('input', { bubbles: true }));
await flush();
if (!window.document.querySelector('.mention-suggest')) failures.push('mention suggestions missing');
const mOpt = byText('.mention-option', 'M');
if (!mOpt) {
  failures.push('mention option missing');
} else {
  click(mOpt);
  await flush();
  if (!window.document.querySelector('.compose-mention .mention-badge')) failures.push('mention badge missing after pick');
}
ta2.value = 'halo';
ta2.dispatchEvent(new window.Event('input', { bubbles: true }));
await flush();
ta2.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
await flush();
const routedMsg = calls.some((c) => c.method === 'POST' && c.url.includes('/messages')
  && typeof c.body === 'string' && c.body.includes('@M halo'));
if (!routedMsg) failures.push('mention-routed message was not sent');

click(byText('.room-tab', 'Changes'));
await flush();
view = window.document.body.innerHTML;
if (!window.document.querySelector('.changes-layout')) failures.push('diff viewer missing');
if (!window.document.querySelector('.changes-tree')) failures.push('changed-files tree missing');
if (!view.includes('src/app.js')) failures.push('diff file path missing');
if (!view.includes('Split') || !view.includes('Unified')) failures.push('diff view-mode toggle missing');

// Task detail: editing happens inside the SAME modal (view <-> edit), never in
// a second stacked dialog, and the description previews as markdown.
click(byText('.room-tab', 'Kanban'));
await flush();
const boardCard = window.document.querySelector('.board-task');
if (!boardCard) {
  failures.push('kanban card missing');
} else {
  click(boardCard);
  await flush();
  if (window.document.querySelectorAll('.modal-backdrop').length !== 1) failures.push('task detail did not open exactly one modal');
  if (!window.document.querySelector('.modal .md-preview')) failures.push('task description is not rendered as markdown');
  const editBtn = byText('.modal-foot .btn', 'Edit');
  if (!editBtn) {
    failures.push('task edit button missing');
  } else {
    click(editBtn);
    await flush();
    if (window.document.querySelectorAll('.modal-backdrop').length !== 1) {
      failures.push('edit must reuse the open modal instead of stacking a second one');
    }
    if (!window.document.querySelector('.modal .md-editor')) failures.push('edit mode markdown editor missing');
    const backBtn = byText('.modal-foot .btn', 'Back to details');
    if (!backBtn) {
      failures.push('edit mode lacks a back-to-details action');
    } else {
      click(backBtn);
      await flush();
      if (window.document.querySelectorAll('.modal-backdrop').length !== 1) failures.push('returning from edit changed the modal');
      if (!window.document.querySelector('.modal .md-preview')) failures.push('back did not restore the detail view');
    }
  }
  const closeBtn = window.document.querySelector('.modal-head .icon-btn');
  if (closeBtn) { click(closeBtn); await flush(); }
}

const back = window.document.querySelector('[aria-label="Back to rooms"]');
if (!back) {
  failures.push('room back button missing');
} else {
  click(back);
  await flush();
  view = window.document.body.innerHTML;
  if (view.includes('Room not found')) failures.push('room back still lands on "Room not found"');
  if (!view.includes('How rooms work')) failures.push('room back did not return to the room list');
}

// Agent detail page with tabs + markdown preview.
click(byText('.nav-btn', 'Agent library'));
await flush();
click(window.document.querySelector('.content .room-row.clickable'));
await flush();
view = window.document.body.innerHTML;
for (const label of ['Overview', 'Instructions', 'Permissions', 'Skills']) {
  if (!view.includes(label)) failures.push(`agent detail tab missing "${label}"`);
}

click(byText('.detail-tab', 'Instructions'));
await flush();
if (!window.document.body.innerHTML.includes('Load starter template')) failures.push('agent instructions starter missing');
const previewToggle = byText('.seg-btn', 'Preview');
if (!previewToggle) {
  failures.push('markdown preview toggle missing');
} else {
  click(previewToggle);
  await flush();
  const preview = window.document.querySelector('.md-preview');
  if (!preview || !preview.querySelector('.md-h')) failures.push('markdown preview is not rendered as formatted HTML');
}

click(byText('.detail-tab', 'Permissions'));
await flush();
view = window.document.body.innerHTML;
for (const label of ['File editor', 'Terminal', 'Glob', 'Grep', 'Browser', 'Sub-agents']) {
  if (!view.includes(label)) failures.push(`tool catalog missing "${label}"`);
}
if (!view.includes('Wired')) failures.push('tool wired badge missing');
if (!view.includes('Policy gated')) failures.push('terminal policy badge missing');

// Agent overview: model picker bound to the connected provider.
click(byText('.detail-tab', 'Overview'));
await flush();
const providerSelect = [...window.document.querySelectorAll('.field select')]
  .find((s) => [...s.options].some((o) => o.textContent === 'TokenHarbor'));
if (!providerSelect) {
  failures.push('provider select missing in agent overview');
} else {
  providerSelect.value = 'tokenharbor';
  providerSelect.dispatchEvent(new window.Event('change', { bubbles: true }));
  await flush();
  view = window.document.body.innerHTML;
  if (!view.includes('auto')) failures.push('provider-bound model trigger missing');
  if (!view.includes('This agent uses the provider\'s default model.')) failures.push('provider-bound model picker hint missing');
}

// Skill detail page with a directory tree.
click(byText('.nav-btn', 'Skills'));
await flush();
click(window.document.querySelector('.skill-row'));
await flush();
view = window.document.body.innerHTML;
if (!view.includes('Files')) failures.push('skill file manager missing');
if (!view.includes('references')) failures.push('skill directory tree missing nested folder');
if (!byText('.tree-file', 'SKILL.md')) failures.push('skill tree missing SKILL.md');
if (!window.document.querySelector('.tree-file-del')) failures.push('file delete icon missing from the tree');
const newFolder = window.document.querySelector('[title="New folder"]');
if (!newFolder) {
  failures.push('new folder button missing');
} else {
  click(newFolder);
  await flush();
  if (!window.document.querySelector('.tree-create')) failures.push('inline folder input missing');
}

// Hash routing: a hashchange should open the target page (survives refresh).
window.location.hash = '#/agents/a1';
window.dispatchEvent(new window.HashChangeEvent('hashchange'));
await flush();
if (!window.document.body.innerHTML.includes('Instructions')) failures.push('hash route did not open the agent page');

// Team detail: add-agent modal grid + per-row "Make main" radio.
click(byText('.nav-btn', 'Teams'));
await flush();
const teamRow = window.document.querySelector('.content .room-row.clickable');
if (!teamRow) {
  failures.push('team row missing');
} else {
  click(teamRow);
  await flush();
  click(byText('.detail-tab', 'Agents'));
  await flush();
  view = window.document.body.innerHTML;
  if (!view.includes('Make main')) failures.push('"Make main" radio missing from team agents');
  if (!view.includes('Main agent')) failures.push('main agent tag missing');
  const addAgentBtn = byText('.btn', 'Add agent');
  if (!addAgentBtn) {
    failures.push('"Add agent" button missing');
  } else {
    click(addAgentBtn);
    await flush();
    view = window.document.body.innerHTML;
    if (!view.includes('Add an agent to this team')) failures.push('add-agent modal did not open');
    const cards = [...window.document.querySelectorAll('.agent-pick-card')];
    if (!cards.length) failures.push('add-agent modal grid is empty');
    if (!cards.some((c) => c.textContent.includes('Scout'))) failures.push('library agent Scout missing from picker grid');
    if (cards.some((c) => c.textContent.includes('M'))) failures.push('already-on-team agent appears in picker grid');
  }
}

// Settings: provider presets, configured row, and the add-provider modal.
click(byText('.nav-btn', 'Settings'));
await flush();
view = window.document.body.innerHTML;
for (const label of ['Connect a provider', 'TokenHarbor', 'OpenCode Zen', 'DeepSeek', 'MiniMax', 'Anthropic (Claude)', 'OpenAI (GPT)', 'Custom provider']) {
  if (!view.includes(label)) failures.push(`settings providers missing "${label}"`);
}
if (!view.includes('deepseek-v4.1-flash:free · default')) failures.push('configured provider default model not shown');
const addProvider = byText('.btn', 'Add provider');
if (!addProvider) {
  failures.push('add provider button missing');
} else {
  click(addProvider);
  await flush();
  view = window.document.body.innerHTML;
  for (const label of ['Models (one per line', 'Discover models', 'Test connection']) {
    if (!view.includes(label)) failures.push(`provider modal missing "${label}"`);
  }
  const modelArea = window.document.querySelector('.modal textarea.code-area');
  if (modelArea && modelArea.value.trim() !== '') failures.push('new provider modal pre-filled models');
}

// Sidebar collapse toggle (desktop).
const menuButton = window.document.querySelector('.mobile-menu');
if (!menuButton) {
  failures.push('sidebar toggle missing');
} else {
  click(menuButton);
  await flush();
  if (!window.document.querySelector('.app.collapsed')) failures.push('sidebar collapse did not apply');
  click(menuButton);
  await flush();
  if (window.document.querySelector('.app.collapsed')) failures.push('sidebar did not expand again');
}

// Room deletion from the rooms list.
window.confirm = () => true;
click(byText('.nav-btn', 'Rooms'));
await flush();
const delBtn = window.document.querySelector('.room-row .row-delete');
if (!delBtn) {
  failures.push('room delete button missing');
} else {
  calls.length = 0;
  click(delBtn);
  await flush();
  if (!calls.some((c) => c.url.includes('/api/rooms/r1') && c.method === 'DELETE')) {
    failures.push('room delete did not call DELETE /api/rooms/r1');
  }
}

if (failures.length) {
  console.error('smoke FAILED:', failures.join('; '));
  process.exit(1);
}
console.log('smoke OK: dashboard flows render room detail, agent editor, and skill tree');