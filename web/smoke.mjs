// Smoke test: load the built dashboard bundle in jsdom with a mocked API and
// assert that the Preact app actually renders workspace content.
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
  schemaVersion: 2,
  projectPath: '/tmp/demo',
  rooms: [{ id: 'r1', name: 'Smoke Room', goal: 'g', teamIds: ['t1'], status: 'planning', messages: [], createdAt: new Date().toISOString() }],
  teams: [{ id: 't1', templateId: 'quick-fix', name: 'Smoke Team', purpose: 'p', mainAgent: 'M', agents: [{ name: 'M', role: 'r' }], instructions: 'i', createdAt: new Date().toISOString() }],
  templates: [{ id: 'quick-fix', version: 1, name: 'Quick Fix', purpose: 'p', mainAgent: 'M', agents: [{ name: 'M', role: 'r' }], instructions: 'i' }],
  providers: [],
  tasks: [{ id: 'k1', roomId: 'r1', title: 'Smoke Task', description: 'd', status: 'todo', priority: 'normal', effort: 'medium', dependencies: [], approved: false, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }],
  runs: [],
  skills: [],
  settings: { theme: 'dark', effortMode: 'automatic', defaultEffort: 'medium', maxConcurrentRuns: 1, maxWorkersPerTask: 1, maxDelegationDepth: 0, maxRetries: 0, maxExecutionSeconds: 600, budgetUsd: 0, requireRunApproval: false, allowProjectCommands: false },
};

const dom = new JSDOM(html, { url: 'http://localhost:8080/?project=%2Ftmp%2Fdemo', runScripts: 'outside-only', pretendToBeVisual: true });
const { window } = dom;

window.fetch = async () => ({
  ok: true,
  status: 200,
  json: async () => workspace,
});
window.matchMedia = window.matchMedia || (() => ({ matches: false, addListener() {}, removeListener() {} }));

const bundle = readFileSync(new URL(jsFile, dist), 'utf8');
try {
  window.eval(bundle);
} catch (error) {
  console.error('smoke: bundle threw during evaluation:', error);
  process.exit(1);
}

// Allow Preact effects/fetch to flush.
await new Promise((resolve) => setTimeout(resolve, 50));

const body = window.document.body.innerHTML;
const failures = [];
if (!body.includes('foreman.')) failures.push('brand missing');
if (!body.includes('Make the work visible.')) failures.push('home headline missing');
if (!body.includes('Smoke Room')) failures.push('room list not rendered from workspace state');
if (!body.includes('Overview')) failures.push('navigation missing');
if (!body.includes('/tmp/demo')) failures.push('project path missing');
if (!/<svg/.test(body)) failures.push('icons are not rendering as SVG');
if (/&lt;i[^>]*data-lucide/.test(body)) failures.push('icon markup leaked as escaped text');
if (window.document.querySelectorAll('.nav .nav-btn').length < 7) {
  failures.push('sidebar nav buttons are missing the nav-btn class');
}

// Open the room and assert the room-centric layout: tabs + right rail.
const roomRow = window.document.querySelector('.room-row.clickable');
if (!roomRow) {
  failures.push('room row not clickable');
} else {
  roomRow.dispatchEvent(new window.MouseEvent('click', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 50));
  const detail = window.document.body.innerHTML;
  for (const label of ['Chat', 'Kanban', 'Plan', 'Changes', 'Task progress', 'Execution policy', 'Room team']) {
    if (!detail.includes(label)) failures.push(`room detail missing "${label}"`);
  }
}

if (failures.length) {
  console.error('smoke FAILED:', failures.join('; '));
  process.exit(1);
}
console.log('smoke OK: dashboard rendered workspace state from mocked API');
