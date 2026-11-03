/*
 * Deterministic force-directed layout for the Signals graph.
 *
 * Kept as a pure module (no DOM, no preact) for two reasons:
 *   1. it can be validated numerically — see web/src/signals-layout.test.mjs;
 *   2. the same graph always produces the same picture, so a human can learn
 *      the layout instead of re-reading a new blob on every poll.
 *
 * The simulation is O(n²) per tick, which is fine at the 48-node cap the
 * backend enforces (≈1100 pairs × 420 ticks).
 */

const TAU = Math.PI * 2;

// hashUnit is a stable [0,1) hash of a path. It seeds initial positions so two
// runs of the same workspace start from the same place.
export function hashUnit(str) {
  let h = 2166136261;
  for (let i = 0; i < str.length; i += 1) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) % 100003) / 100003;
}

// nodeRadius blends connectivity (degree) with learned weight so the eye can
// rank a node without reading the side panel.
export function nodeRadius(node, maxWeight) {
  const degree = Number(node.degree || 0);
  const weight = Number(node.weight || 0);
  const fromDegree = Math.sqrt(degree) * 3.1;
  const fromWeight = maxWeight > 0 ? (weight / maxWeight) * 4.2 : 0;
  return Math.max(6.5, Math.min(19, 6.5 + fromDegree + fromWeight));
}

/*
 * layoutGraph places nodes in a W×H box.
 *
 * Forces: pairwise repulsion (with a hard collision term so circles never
 * overlap), spring attraction along edges weighted by edge weight, and a weak
 * pull to the centre that keeps disconnected components from drifting to the
 * corners. Velocity damping plus a cooling factor make it settle quickly.
 */
export function layoutGraph(nodes, edges, W, H, opts = {}) {
  const n = nodes.length;
  if (n === 0) return [];

  const maxWeight = nodes.reduce((m, x) => Math.max(m, Number(x.weight || 0)), 0);
  const radii = nodes.map((x) => nodeRadius(x, maxWeight));

  if (n === 1) {
    return [{ ...nodes[0], x: W / 2, y: H / 2, r: radii[0] }];
  }

  const index = new Map(nodes.map((x, i) => [x.path, i]));
  const links = [];
  for (const e of edges) {
    const s = index.get(e.from);
    const t = index.get(e.to);
    if (s === undefined || t === undefined || s === t) continue;
    links.push({ s, t, w: Number(e.weight || 0) });
  }
  const maxLink = links.reduce((m, l) => Math.max(m, l.w), 0) || 1;

  const pts = nodes.map((node, i) => {
    const angle = i * 2.399963229728653 + hashUnit(node.path) * TAU;
    const radius = 44 + Math.sqrt(i + 1) * (Math.min(W, H) / 11);
    return {
      ...node,
      r: radii[i],
      x: W / 2 + Math.cos(angle) * radius,
      y: H / 2 + Math.sin(angle) * radius,
      vx: 0,
      vy: 0,
    };
  });

  const cx = W / 2;
  const cy = H / 2;
  const PAD = 26;

  const REPULSE = opts.repulse ?? 5200;
  // Adaptive spacing: a 6-node graph should fill the canvas, a 48-node one
  // must stay legible. Target area per node sets the spring rest length.
  const areaPerNode = (W * H) / n;
  const LINK_DIST = opts.linkDist ?? Math.max(58, Math.min(150, Math.sqrt(areaPerNode) * 0.62));
  const LINK_PULL = opts.linkPull ?? 0.05;
  const GRAVITY = opts.gravity ?? 0.0012;
  const DAMPING = opts.damping ?? 0.86;
  const TICKS = opts.ticks ?? 520;

  for (let tick = 0; tick < TICKS; tick += 1) {
    const cooling = 0.35 + (1 - tick / TICKS) * 0.65;

    for (let i = 0; i < n; i += 1) {
      const a = pts[i];
      for (let j = i + 1; j < n; j += 1) {
        const b = pts[j];
        let dx = b.x - a.x;
        let dy = b.y - a.y;
        let d2 = dx * dx + dy * dy;
        if (d2 < 0.01) {
          dx = hashUnit(a.path + b.path) - 0.5 || 0.5;
          dy = hashUnit(b.path + a.path) - 0.5 || 0.5;
          d2 = dx * dx + dy * dy;
        }
        const d = Math.sqrt(d2);
        const minD = a.r + b.r + 14;
        let push = REPULSE / d2;
        if (d < minD) push += (minD - d) * 1.5;
        if (push > 80) push = 80;
        const ux = dx / d;
        const uy = dy / d;
        a.vx -= ux * push;
        a.vy -= uy * push;
        b.vx += ux * push;
        b.vy += uy * push;
      }
    }

    for (const link of links) {
      const a = pts[link.s];
      const b = pts[link.t];
      const dx = b.x - a.x;
      const dy = b.y - a.y;
      const d = Math.hypot(dx, dy) || 1;
      const strength = LINK_PULL * (0.45 + (link.w / maxLink) * 0.85);
      const f = (d - LINK_DIST) * strength;
      const ux = dx / d;
      const uy = dy / d;
      a.vx += ux * f;
      a.vy += uy * f;
      b.vx -= ux * f;
      b.vy -= uy * f;
    }

    for (const p of pts) {
      p.vx += (cx - p.x) * GRAVITY;
      p.vy += (cy - p.y) * GRAVITY;
      p.vx *= DAMPING;
      p.vy *= DAMPING;
      p.x += p.vx * cooling;
      p.y += p.vy * cooling;

      const padX = Math.min(W / 2, p.r + PAD);
      const padY = Math.min(H / 2, p.r + PAD);
      if (p.x < padX) { p.x = padX; p.vx = Math.abs(p.vx) * 0.35; }
      if (p.x > W - padX) { p.x = W - padX; p.vx = -Math.abs(p.vx) * 0.35; }
      if (p.y < padY) { p.y = padY; p.vy = Math.abs(p.vy) * 0.35; }
      if (p.y > H - padY) { p.y = H - padY; p.vy = -Math.abs(p.vy) * 0.35; }
    }
  }

  return pts;
}
