/*
 * Layout invariants for the Signals graph.
 *
 * These are the properties the picture must hold regardless of how it looks:
 * determinism (same input → same picture), no overlapping circles, everything
 * inside the canvas, and edges shorter than the diagonal. Run with:
 *   node web/src/signals-layout.test.mjs
 */
import assert from 'node:assert/strict';
import { layoutGraph, nodeRadius, hashUnit } from './signals-layout.js';

const W = 880;
const H = 520;

function buildGraph(n, kind = 'import') {
  const nodes = [];
  for (let i = 0; i < n; i += 1) {
    nodes.push({ path: `src/mod${i % 7}/file${i}.ts`, weight: 20 - (i % 10), hits: 5, degree: 0 });
  }
  const edges = [];
  for (let i = 1; i < n; i += 1) {
    edges.push({ from: nodes[i - 1].path, to: nodes[i].path, kind, weight: 1 + (i % 4) });
    if (i > 3) edges.push({ from: nodes[i - 3].path, to: nodes[i].path, kind: 'co_touch', weight: 1 });
  }
  // Recompute degree the way the backend does.
  const deg = new Map(nodes.map((x) => [x.path, 0]));
  for (const e of edges) {
    deg.set(e.from, (deg.get(e.from) || 0) + 1);
    deg.set(e.to, (deg.get(e.to) || 0) + 1);
  }
  for (const x of nodes) x.degree = deg.get(x.path);
  return { nodes, edges };
}

function checkInvariants(label, { nodes, edges }, placed) {
  assert.equal(placed.length, nodes.length, `${label}: node count preserved`);

  // 1. Determinism: same input, same output.
  const again = layoutGraph(nodes, edges, W, H);
  for (let i = 0; i < placed.length; i += 1) {
    assert.equal(placed[i].x, again[i].x, `${label}: deterministic x for ${placed[i].path}`);
    assert.equal(placed[i].y, again[i].y, `${label}: deterministic y for ${placed[i].path}`);
  }

  // 2. No overlap beyond a small tolerance.
  let worst = 0;
  for (let i = 0; i < placed.length; i += 1) {
    for (let j = i + 1; j < placed.length; j += 1) {
      const a = placed[i];
      const b = placed[j];
      const d = Math.hypot(b.x - a.x, b.y - a.y);
      const min = a.r + b.r;
      worst = Math.max(worst, min - d);
    }
  }
  assert.ok(worst < 6, `${label}: overlap ${worst.toFixed(2)}px exceeds tolerance`);

  // 3. Inside the canvas.
  for (const p of placed) {
    assert.ok(p.x >= 0 && p.x <= W, `${label}: ${p.path} x=${p.x} outside canvas`);
    assert.ok(p.y >= 0 && p.y <= H, `${label}: ${p.path} y=${p.y} outside canvas`);
    assert.ok(Number.isFinite(p.x) && Number.isFinite(p.y), `${label}: non-finite position`);
  }

  // 4. Layout actually uses the canvas: no degenerate single point.
  const xs = placed.map((p) => p.x);
  const ys = placed.map((p) => p.y);
  const spanX = Math.max(...xs) - Math.min(...xs);
  const spanY = Math.max(...ys) - Math.min(...ys);
  assert.ok(spanX > W * 0.25, `${label}: horizontal spread ${spanX.toFixed(0)}px too small`);
  assert.ok(spanY > H * 0.25, `${label}: vertical spread ${spanY.toFixed(0)}px too small`);

  // 5. Connected nodes end up closer than random pairs, on average.
  const dist = new Map();
  for (const e of edges) {
    const a = placed.find((p) => p.path === e.from);
    const b = placed.find((p) => p.path === e.to);
    dist.set(`${e.from}->${e.to}`, Math.hypot(b.x - a.x, b.y - a.y));
  }
  const linkAvg = [...dist.values()].reduce((s, v) => s + v, 0) / (dist.size || 1);
  const diag = Math.hypot(W, H);
  assert.ok(linkAvg < diag * 0.55, `${label}: mean edge length ${linkAvg.toFixed(0)}px is not compact`);

  return { worst, spanX, spanY, linkAvg };
}

// --- small graph -----------------------------------------------------------
{
  const g = buildGraph(6);
  const placed = layoutGraph(g.nodes, g.edges, W, H);
  const m = checkInvariants('6 nodes', g, placed);
  console.log(`ok 6 nodes   overlap=${m.worst.toFixed(1)}px span=${m.spanX.toFixed(0)}x${m.spanY.toFixed(0)} meanEdge=${m.linkAvg.toFixed(0)}px`);
}

// --- the real cap: 48 nodes ------------------------------------------------
{
  const g = buildGraph(48);
  const placed = layoutGraph(g.nodes, g.edges, W, H);
  const m = checkInvariants('48 nodes', g, placed);
  console.log(`ok 48 nodes  overlap=${m.worst.toFixed(1)}px span=${m.spanX.toFixed(0)}x${m.spanY.toFixed(0)} meanEdge=${m.linkAvg.toFixed(0)}px`);
}

// --- isolated nodes (no edges at all) --------------------------------------
{
  const nodes = Array.from({ length: 12 }, (_, i) => ({
    path: `lone/${i}.ts`, weight: 3, hits: 1, degree: 0,
  }));
  const placed = layoutGraph(nodes, [], W, H);
  checkInvariants('no edges', { nodes, edges: [] }, placed);
  console.log('ok no edges  12 isolated nodes stay inside the canvas');
}

// --- single node -----------------------------------------------------------
{
  const nodes = [{ path: 'only.ts', weight: 1, hits: 1, degree: 0 }];
  const placed = layoutGraph(nodes, [], W, H);
  assert.equal(placed.length, 1);
  assert.equal(placed[0].x, W / 2);
  assert.equal(placed[0].y, H / 2);
  console.log('ok single node centres itself');
}

// --- radius monotonicity ---------------------------------------------------
{
  const maxW = 10;
  const small = nodeRadius({ degree: 1, weight: 1 }, maxW);
  const big = nodeRadius({ degree: 9, weight: 10 }, maxW);
  assert.ok(big > small, 'radius must grow with degree/weight');
  assert.ok(small >= 6.5 && big <= 19, 'radius stays within its clamp');
  console.log(`ok radius    ${small.toFixed(1)}px → ${big.toFixed(1)}px`);
}

// --- hash stability --------------------------------------------------------
{
  assert.equal(hashUnit('a.ts'), hashUnit('a.ts'));
  assert.notEqual(hashUnit('a.ts'), hashUnit('b.ts'));
  const h = hashUnit('src/deep/path/file.ts');
  assert.ok(h >= 0 && h < 1, 'hash stays in [0,1)');
  console.log('ok hash      stable and in range');
}

console.log('\nall layout invariants hold');
