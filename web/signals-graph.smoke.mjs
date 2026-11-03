// Signals graph smoke: renders the built bundle in jsdom against a realistic
// /api/signals payload and pins the properties that are easy to regress —
// node/edge counts, the zoom HUD, deterministic layout across remounts, the
// label budget, and the click-to-inspect detail panel.
//
// Kept separate from smoke.mjs because that suite asserts unrelated page flows
// that currently fail for pre-existing reasons (see git history).
import { readFileSync, readdirSync } from 'node:fs';
import { JSDOM } from 'jsdom';

const dist = new URL('../internal/foreman/web/dist/', import.meta.url);
const assets = readdirSync(new URL('assets/', dist));
const jsFile = 'assets/' + assets.find(n => /^index-.*\.js$/.test(n));
const html = readFileSync(new URL('index.html', dist), 'utf8');

const workspace = {
  schemaVersion: 3, projectPath: '/tmp/demo',
  rooms: [], tasks: [], runs: [], skills: [], agents: [], teams: [], templates: [],
  providers: [], mcpServers: [], panels: [], plans: [], artifacts: [],
  settings: { theme: 'dark', maxConcurrentRuns: 1 },
};

const signals = {
  files: 6, edges: 8, touches: 21,
  kinds: { import: 4, edit: 2, co_touch: 2 },
  top_paths: [{ path: 'a/b.ts', weight: 10, hits: 6 }],
  activity: [{ day: '2026-11-01', touches: 8 }],
  graph: {
    truncated: false,
    nodes: [
      { path: 'backend/src/services/tickets/ticket-mutation.service.ts', weight: 10, hits: 6, degree: 4 },
      { path: 'backend/src/scripts/seed.ts', weight: 9, hits: 5, degree: 3 },
      { path: 'backend/src/models/ticket-status.model.ts', weight: 4, hits: 3, degree: 3 },
      { path: 'backend/src/constants/ticket.constants.ts', weight: 3, hits: 2, degree: 2 },
      { path: 'backend/src/db/seeders/custom-field.seeder.ts', weight: 3, hits: 3, degree: 2 },
      { path: 'backend/src/utils/format.ts', weight: 1, hits: 1, degree: 1 },
    ],
    edges: [
      { from: 'backend/src/scripts/seed.ts', to: 'backend/src/models/ticket-status.model.ts', kind: 'import', weight: 5 },
      { from: 'backend/src/services/tickets/ticket-mutation.service.ts', to: 'backend/src/models/ticket-status.model.ts', kind: 'import', weight: 4 },
      { from: 'backend/src/services/tickets/ticket-mutation.service.ts', to: 'backend/src/scripts/seed.ts', kind: 'co_touch', weight: 2 },
      { from: 'backend/src/constants/ticket.constants.ts', to: 'backend/src/services/tickets/ticket-mutation.service.ts', kind: 'co_touch', weight: 1 },
      { from: 'backend/src/db/seeders/custom-field.seeder.ts', to: 'backend/src/scripts/seed.ts', kind: 'edit', weight: 2 },
      { from: 'backend/src/utils/format.ts', to: 'backend/src/constants/ticket.constants.ts', kind: 'import', weight: 1 },
    ],
  },
};

const dom = new JSDOM(html, { url: 'http://localhost:8080/?project=%2Ftmp%2Fdemo', runScripts: 'outside-only', pretendToBeVisual: true });
const { window } = dom;
window.fetch = async (url) => {
  const u = String(url);
  if (u.includes('/api/signals')) return { ok: true, status: 200, json: async () => signals };
  return { ok: true, status: 200, json: async () => workspace };
};
window.matchMedia = window.matchMedia || (() => ({ matches: false, addListener() {}, removeListener() {} }));
window.eval(readFileSync(new URL(jsFile, dist), 'utf8'));

const flush = () => new Promise(r => setTimeout(r, 60));
const click = el => el && el.dispatchEvent(new window.MouseEvent('click', { bubbles: true }));
const byText = (sel, text) => [...window.document.querySelectorAll(sel)].find(el => el.textContent.trim().startsWith(text));

await flush();
click(byText('.nav-btn', 'Signals'));
await flush(); await flush();

const failures = [];
if (!window.document.body.innerHTML.includes('Internal Signals')) failures.push('signals page did not render');
const tab = byText('.settings-tab', 'Graph');
if (!tab) failures.push('Graph tab missing');
click(tab); await flush();

const svg = window.document.querySelector('.sig-graph-svg');
if (!svg) failures.push('graph SVG missing');
else {
  const nodes = svg.querySelectorAll('.sig-node');
  const paths = svg.querySelectorAll('path');
  console.log(`nodes=${nodes.length} paths=${paths.length} hud=${!!window.document.querySelector('.sig-graph-hud')}`);
  if (nodes.length !== 6) failures.push(`expected 6 nodes, got ${nodes.length}`);
  if (paths.length < 6) failures.push(`expected >=6 edges, got ${paths.length}`);
  if (!window.document.querySelector('.sig-graph-hud')) failures.push('zoom HUD missing');

  const first = [...nodes].map(g => g.getAttribute('transform')).join('|');
  click(byText('.settings-tab', 'Usage')); await flush();
  click(byText('.settings-tab', 'Graph')); await flush();
  const second = [...window.document.querySelectorAll('.sig-graph-svg .sig-node')].map(g => g.getAttribute('transform')).join('|');
  if (first !== second) failures.push('layout not deterministic across remounts');

  // labels: only top third + focus get one
  const labels = svg.querySelectorAll('.sig-node-label').length;
  console.log(`labels=${labels} (of ${nodes.length} nodes)`);
  // Small graphs (<=8) label everything by design; only assert the budget kicks
  // in on a crowded graph (checked separately below).
  if (labels === 0) failures.push('no labels drawn');

  // hover a node → detail panel shows path
  const hot = window.document.querySelectorAll('.sig-graph-svg .sig-node')[0];
  click(hot);
  await flush();
  const detail = window.document.querySelector('.sig-detail-path');
  console.log('detailHTML:', (window.document.querySelector('.sig-detail')||{}).outerHTML?.slice(0,200));
  console.log('detail:', detail ? detail.textContent : '(none)');
  if (!detail) failures.push('hover did not open detail panel');
}

if (failures.length) { console.error('FAILED:', failures.join('; ')); process.exit(1); }
// Crowded graph: the label budget must keep the canvas readable.
{
  const many = { truncated: true, nodes: [], edges: [] };
  for (let i = 0; i < 40; i += 1) {
    many.nodes.push({ path: `src/deep/dir${i % 6}/mod${i}.ts`, weight: 40 - i, hits: 10, degree: 2 });
    if (i > 0) many.edges.push({ from: `src/deep/dir${(i - 1) % 6}/mod${i - 1}.ts`, to: `src/deep/dir${i % 6}/mod${i}.ts`, kind: 'import', weight: 2 });
  }
  const dom2 = new JSDOM(html, { url: 'http://localhost:8080/?project=%2Ftmp%2Fdemo', runScripts: 'outside-only', pretendToBeVisual: true });
  dom2.window.fetch = async (url) => {
    const u = String(url);
    if (u.includes('/api/signals')) return { ok: true, status: 200, json: async () => ({ ...signals, graph: many }) };
    return { ok: true, status: 200, json: async () => workspace };
  };
  dom2.window.matchMedia = dom2.window.matchMedia || (() => ({ matches: false, addListener() {}, removeListener() {} }));
  dom2.window.eval(readFileSync(new URL(jsFile, dist), 'utf8'));
  const f2 = () => new Promise(r => setTimeout(r, 60));
  const c2 = (el) => el && el.dispatchEvent(new dom2.window.MouseEvent('click', { bubbles: true }));
  const t2 = (sel, text) => [...dom2.window.document.querySelectorAll(sel)].find(el => el.textContent.trim().startsWith(text));
  await f2();
  c2(t2('.nav-btn', 'Signals')); await f2(); await f2();
  c2(t2('.settings-tab', 'Graph')); await f2();
  const drawn = dom2.window.document.querySelectorAll('.sig-graph-svg .sig-node').length;
  const labels = dom2.window.document.querySelectorAll('.sig-node-label').length;
  console.log(`crowded: nodes=${drawn} labels=${labels}`);
  if (drawn !== 40) { console.error(`FAILED: expected 40 nodes, got ${drawn}`); process.exit(1); }
  if (labels === 0 || labels >= drawn) {
    console.error(`FAILED: label budget should hide most labels, drew ${labels}/${drawn}`);
    process.exit(1);
  }
  if (!dom2.window.document.body.innerHTML.includes('hottest files')) {
    console.error('FAILED: truncated notice missing on a capped graph');
    process.exit(1);
  }
}

console.log('signals graph smoke OK');
