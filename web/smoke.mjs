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
    messages: [], createdAt: new Date().toISOString(),
    questions: [{ id: 'q1', prompt: 'Proceed with the refactor?', options: ['Yes', 'No'], answer: '', createdAt: new Date().toISOString() }],
  }],
  teams: [{ id: 't1', templateId: 'quick-fix', name: 'Smoke Team', purpose: 'p', mainAgent: 'M', agents: [{ id: 'ta1', name: 'M', role: 'r', instructions: '# hi' }], instructions: 'i', createdAt: new Date().toISOString() }],
  templates: [{ id: 'quick-fix', version: 1, name: 'Quick Fix', purpose: 'p', mainAgent: 'M', agents: [{ name: 'M', role: 'r' }], instructions: 'i' }],
  providers: [{ id: 'tokenharbor', name: 'TokenHarbor', protocol: 'openai-compatible', baseUrl: 'https://tokenharbor.ai/v1', models: ['deepseek-v4.1-flash:free', 'claude-sonnet-5'], defaultModel: 'deepseek-v4.1-flash:free', hasApiKey: true, enabled: true }],
  tasks: [{ id: 'k1', roomId: 'r1', title: 'Smoke Task', description: 'd', status: 'todo', priority: 'normal', effort: 'medium', dependencies: [], approved: false, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }],
  runs: [{
    id: 'run1', roomId: 'r1', taskId: 'k1', provider: 'tokenharbor', model: 'deepseek-v4.1-flash:free',
    status: 'running', startedAt: new Date().toISOString(), events: [], tokens: 120,
    result: 'diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1,2 +1,3 @@\n-old line\n+new line\n+extra line\n',
  }],
  skills: [{ id: 's1', name: 'Docs', description: 'd', version: 1, instructions: '# Docs', mode: 'passive', enabled: false, files: [{ path: 'SKILL.md', content: '# Docs' }, { path: 'references/steps.md', content: 'step 1' }] }],
  agents: [{ id: 'a1', name: 'Scout', role: 'Researcher', description: 'd', instructions: '# Scout\n\n## Responsibilities\n\n- Inspect', tools: ['file_editor'], skills: [], requireReview: false }],
  settings: { theme: 'dark', effortMode: 'automatic', defaultEffort: 'medium', maxConcurrentRuns: 1, maxWorkersPerTask: 1, maxDelegationDepth: 0, maxRetries: 0, maxExecutionSeconds: 600, budgetUsd: 0, requireRunApproval: false, allowProjectCommands: false },
};

const dom = new JSDOM(html, { url: 'http://localhost:8080/?project=%2Ftmp%2Fdemo', runScripts: 'outside-only', pretendToBeVisual: true });
const { window } = dom;

window.fetch = async () => ({ ok: true, status: 200, json: async () => workspace });
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
if (!view.includes('Running')) failures.push('room runtime status missing');
if (!view.includes('Pause run')) failures.push('pause control missing');
if (!view.includes('Agent needs a decision')) failures.push('agent question card missing');
if (!view.includes('Proceed with the refactor?')) failures.push('question prompt missing');
if (window.location.hash !== '#/rooms/r1') failures.push('room route was not written to the hash');

click(byText('.room-tab', 'Changes'));
await flush();
view = window.document.body.innerHTML;
if (!view.includes('diff-file')) failures.push('diff viewer missing');
if (!view.includes('src/app.js')) failures.push('diff file path missing');
if (!view.includes('Collapse all') && !view.includes('Show all')) failures.push('diff collapse control missing');

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
}

if (failures.length) {
  console.error('smoke FAILED:', failures.join('; '));
  process.exit(1);
}
console.log('smoke OK: dashboard flows render room detail, agent editor, and skill tree');