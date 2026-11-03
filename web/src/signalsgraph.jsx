import { useMemo, useState } from 'preact/hooks';

/*
 * SignalsGraph — a force-free radial view of the learned file graph.
 *
 * The agent-side graph is a weighted multigraph (import / edit / co_touch /
 * search). Drawing it needs two properties the dashboard cares about more
 * than physics: it must be deterministic (the same workspace always paints
 * the same picture, so a human can learn the layout) and cheap (no layout
 * loop on every poll). So nodes are placed on rings by rank — the hottest
 * file at the centre, the rest in concentric rings — and edges are drawn as
 * curves whose opacity follows weight.
 *
 * Only the hot core is rendered (the backend caps nodes and edges); the
 * agent still sees full neighborhoods through the `related` tool.
 */

const W = 880;
const H = 520;
const CX = W / 2;
const CY = H / 2;

const KIND_STYLE = {
  import: { color: '#7aa2f7', dash: '', label: 'import' },
  edit: { color: '#9ece6a', dash: '', label: 'edit' },
  co_touch: { color: '#e0af68', dash: '5 4', label: 'co-touch' },
  search: { color: '#7dcfff', dash: '2 5', label: 'search' },
};

function kindStyle(kind) {
  return KIND_STYLE[kind] || { color: '#8b93a1', dash: '3 3', label: kind };
}

// shortLabel keeps a path recognisable without eating the canvas: the file
// name plus its parent directory.
function shortLabel(path) {
  const parts = String(path).split('/');
  if (parts.length === 1) return parts[0];
  const file = parts[parts.length - 1];
  const dir = parts[parts.length - 2];
  return `${dir}/${file}`;
}

/* layout places nodes on concentric rings by descending weight. Ring 0 is a
   single centre node; later rings hold 6, 12, 18, … so density grows with
   radius and labels never collide with the centre. */
function layout(nodes) {
  const placed = [];
  const sorted = nodes.slice().sort((a, b) => (b.weight || 0) - (a.weight || 0));
  let ring = 0;
  let index = 0;
  let remaining = sorted.length;
  while (remaining > 0) {
    const capacity = ring === 0 ? 1 : ring * 6;
    const take = Math.min(capacity, remaining);
    const radius = ring === 0 ? 0 : 74 + (ring - 1) * 84;
    for (let i = 0; i < take; i += 1) {
      const angle = ring === 0 ? 0 : (Math.PI * 2 * i) / take - Math.PI / 2;
      placed.push({
        ...sorted[index + i],
        x: CX + Math.cos(angle) * radius,
        y: CY + Math.sin(angle) * radius,
        ring,
      });
    }
    index += take;
    remaining -= take;
    ring += 1;
  }
  return placed;
}

// radiusFor maps degree to a node radius: 7..17 px keeps labels legible.
function radiusFor(node) {
  const d = Number(node.degree || 0);
  return Math.min(17, 7 + Math.sqrt(d) * 3.2);
}

export function SignalsGraph({ graph, onPick }) {
  const [hover, setHover] = useState('');
  const nodes = graph?.nodes || [];
  const edges = graph?.edges || [];

  const { placed, byPath, maxWeight } = useMemo(() => {
    const p = layout(nodes);
    const map = new Map(p.map((n) => [n.path, n]));
    const max = p.reduce((m, n) => Math.max(m, Number(n.weight || 0)), 0) || 1;
    return { placed: p, byPath: map, maxWeight: max };
  }, [nodes]);

  if (!placed.length) {
    return (
      <div class="empty">
        No graph yet. Run a task that reads or edits files, or refresh the index so import edges are mapped.
      </div>
    );
  }

  const active = hover ? byPath.get(hover) : null;

  return (
    <div class="sig-graph-wrap">
      <svg class="sig-graph" viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Learned file graph">
        <defs>
          {Object.entries(KIND_STYLE).map(([kind, s]) => (
            <marker id={`sig-arrow-${kind}`} viewBox="0 0 10 10" refX="9" refY="5"
              markerWidth="5" markerHeight="5" orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" fill={s.color} />
            </marker>
          ))}
        </defs>

        {edges.map((e, i) => {
          const a = byPath.get(e.from);
          const b = byPath.get(e.to);
          if (!a || !b) return null;
          const s = kindStyle(e.kind);
          const dim = hover && e.from !== hover && e.to !== hover;
          const opacity = dim ? 0.06 : 0.18 + Math.min(0.55, Number(e.weight || 0) / maxWeight * 0.55);
          // A gentle curve keeps reciprocal pairs from overlapping exactly.
          const mx = (a.x + b.x) / 2;
          const my = (a.y + b.y) / 2;
          const dx = b.x - a.x;
          const dy = b.y - a.y;
          const len = Math.hypot(dx, dy) || 1;
          const bend = Math.min(34, len * 0.12) * (i % 2 === 0 ? 1 : -1);
          const ctrlX = mx + (-dy / len) * bend;
          const ctrlY = my + (dx / len) * bend;
          return (
            <path
              key={`${e.kind}-${e.from}-${e.to}-${i}`}
              d={`M ${a.x} ${a.y} Q ${ctrlX} ${ctrlY} ${b.x} ${b.y}`}
              fill="none"
              stroke={s.color}
              stroke-width={e.kind === 'import' ? 1.5 : 1}
              stroke-dasharray={s.dash}
              stroke-opacity={opacity}
              marker-end={`url(#sig-arrow-${e.kind})`}
            />
          );
        })}

        {placed.map((n) => {
          const r = radiusFor(n);
          const isHot = n.path === hover;
          const dim = hover && !isHot && !edges.some(
            (e) => (e.from === hover && e.to === n.path) || (e.to === hover && e.from === n.path),
          );
          return (
            <g
              key={n.path}
              class="sig-node"
              transform={`translate(${n.x} ${n.y})`}
              onMouseEnter={() => setHover(n.path)}
              onMouseLeave={() => setHover('')}
              onClick={() => onPick && onPick(n.path)}
              style={`opacity:${dim ? 0.25 : 1}`}
            >
              <circle r={r + 6} fill="transparent" />
              <circle r={r} fill={isHot ? 'var(--accent)' : '#1b2116'}
                stroke={isHot ? 'var(--accent)' : '#3b4a2c'} stroke-width="1.4" />
              <text y={r + 13} text-anchor="middle" class="sig-node-label">{shortLabel(n.path)}</text>
            </g>
          );
        })}
      </svg>

      <div class="sig-graph-side">
        <div class="sig-legend">
          {Object.entries(KIND_STYLE).map(([kind, s]) => (
            <span class="sig-legend-item" key={kind}>
              <svg width="26" height="8" aria-hidden="true">
                <line x1="0" y1="4" x2="26" y2="4" stroke={s.color} stroke-width="2" stroke-dasharray={s.dash} />
              </svg>
              {s.label}
            </span>
          ))}
        </div>

        {active ? (
          <div class="sig-detail">
            <div class="sig-detail-path mono">{active.path}</div>
            <div class="sig-detail-row"><span>Weight</span><b>{Number(active.weight || 0).toFixed(1)}</b></div>
            <div class="sig-detail-row"><span>Touches</span><b>{active.hits || 0}</b></div>
            <div class="sig-detail-row"><span>Connections</span><b>{active.degree || 0}</b></div>
            <div class="sig-detail-conn">
              {edges
                .filter((e) => e.from === active.path || e.to === active.path)
                .slice(0, 8)
                .map((e, i) => {
                  const other = e.from === active.path ? e.to : e.from;
                  const s = kindStyle(e.kind);
                  return (
                    <div class="sig-detail-edge" key={`${e.kind}-${other}-${i}`}>
                      <span class="sig-dot" style={`background:${s.color}`} />
                      <span class="mono">{shortLabel(other)}</span>
                      <span class="sig-detail-kind">{s.label}</span>
                    </div>
                  );
                })}
            </div>
          </div>
        ) : (
          <div class="sig-detail sig-detail-idle">
            Hover a node to see its weight, touches, and strongest connections.
            <br />
            <span class="meta-text">Node size follows how many edges touch the file. Ring 0 is the hottest file.</span>
          </div>
        )}

        {graph?.truncated && (
          <div class="meta-text sig-trunc">
            Showing the hottest {placed.length} files. The agent's <code>related</code> tool still sees the full graph.
          </div>
        )}
      </div>
    </div>
  );
}
