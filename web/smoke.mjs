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
  rooms: [{ id: 'r1', name: 'Smoke Room', goal: 'g', teamIds: [], status: 'planning', messages: [], createdAt: new Date().toISOString() }],
  teams: [],
  templates: [{ id: 'quick-fix', version: 1, Name: 'Quick Fix', name: 'Quick Fix', purpose: 'p', mainAgent: 'M', agents: [{ name: 'M', role: 'r' }], instructions: 'i' }],
  providers: [],
  tasks: [],
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
if (!body.includes('Kanban')) failures.push('navigation missing');
if (!body.includes('/tmp/demo')) failures.push('project path missing');

if (failures.length) {
  console.error('smoke FAILED:', failures.join('; '));
  process.exit(1);
}
console.log('smoke OK: dashboard rendered workspace state from mocked API');
