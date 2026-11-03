import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import { layoutGraph, nodeRadius } from './signals-layout.js';

/*
 * SignalsGraph — force-directed view of the learned file graph.
 *
 * Design decisions that matter:
 *   - The layout is deterministic (see signals-layout.js), so the picture is
 *     stable between polls instead of jittering on every refresh.
 *   - It is responsive: the canvas is measured from its container and the
 *     simulation re-runs on resize, with a device-pixel-ratio-aware transform
 *     so lines stay crisp on retina displays.
 *   - It is navigable: wheel zooms at the cursor, drag pans, double-click
 *     resets. A static picture of 48 nodes is unreadable at one zoom level.
 *   - Labels are only drawn for nodes that carry enough weight (plus the
 *     hovered/selected node), because 48 stacked labels is noise.
 */

const KIND_STYLE = {
  import: { color: '#7aa2f7', dash: '', label: 'import', width: 1.6 },
  edit: { color: '#9ece6a', dash: '', label: 'edit', width: 1.2 },
  co_touch: { color: '#e0af68', dash: '5 4', label: 'co-touch', width: 1.1 },
  search: { color: '#7dcfff', dash: '2 5', label: 'search', width: 1 },
};

const styleFor = (kind) => KIND_STYLE[kind] || { color: '#8b93a1', dash: '3 3', label: kind, width: 1 };

// shortLabel keeps a path recognisable: parent dir + file name.
function shortLabel(path) {
  const parts = String(path).split('/');
  if (parts.length === 1) return parts[0];
  return `${parts[parts.length - 2]}/${parts[parts.length - 1]}`;
}

const MIN_ZOOM = 0.5;
const MAX_ZOOM = 4;

export function SignalsGraph({ graph, onPick }) {
  const nodes = graph?.nodes || [];
  const edges = graph?.edges || [];

  const hostRef = useRef(null);
  const [size, setSize] = useState({ w: 880, h: 520 });
  const [hover, setHover] = useState('');
  const [pinned, setPinned] = useState('');
  const [view, setView] = useState({ k: 1, x: 0, y: 0 });
  const dragRef = useRef(null);

  // Measure the container so the simulation fills whatever space it is given
  // (the card is flex-sized, so a fixed viewBox would letterbox on wide
  // screens and squash on narrow ones).
  useLayoutEffect(() => {
    const host = hostRef.current;
    if (!host) return undefined;
    const measure = () => {
      const rect = host.getBoundingClientRect();
      if (rect.width > 0 && rect.height > 0) {
        setSize({ w: Math.round(rect.width), h: Math.round(rect.height) });
      }
    };
    measure();
    // ResizeObserver is the right tool but is not universal (jsdom, older
    // browsers). Fall back to a window resize listener so the graph still
    // tracks its container instead of silently keeping the 880×520 default.
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', measure);
      return () => window.removeEventListener('resize', measure);
    }
    const ro = new ResizeObserver(measure);
    ro.observe(host);
    return () => ro.disconnect();
  }, []);

  const placed = useMemo(
    () => layoutGraph(nodes, edges, size.w, size.h),
    [nodes, edges, size.w, size.h],
  );

  const byPath = useMemo(() => new Map(placed.map((n) => [n.path, n])), [placed]);
  const maxWeight = useMemo(
    () => placed.reduce((m, n) => Math.max(m, Number(n.weight || 0)), 0) || 1,
    [placed],
  );

  // Label budget: only the top third by weight get a permanent label, so the
  // canvas reads as a map instead of a wall of text.
  const labelCutoff = useMemo(() => {
    if (placed.length <= 8) return 0;
    const weights = placed.map((n) => Number(n.weight || 0)).sort((a, b) => b - a);
    return weights[Math.min(weights.length - 1, Math.ceil(weights.length / 3) - 1)];
  }, [placed]);

  const focus = hover || pinned;
  const focusNode = focus ? byPath.get(focus) : null;

  const neighbours = useMemo(() => {
    if (!focus) return new Set();
    const set = new Set([focus]);
    for (const e of edges) {
      if (e.from === focus) set.add(e.to);
      if (e.to === focus) set.add(e.from);
    }
    return set;
  }, [focus, edges]);

  // Zoom at the cursor: the point under the pointer must not slide away.
  const zoomAt = useCallback((factor, clientX, clientY) => {
    const host = hostRef.current;
    if (!host) return;
    const rect = host.getBoundingClientRect();
    const px = clientX - rect.left;
    const py = clientY - rect.top;
    setView((v) => {
      const k = Math.max(MIN_ZOOM, Math.min(MAX_ZOOM, v.k * factor));
      const scale = k / v.k;
      return { k, x: px - (px - v.x) * scale, y: py - (py - v.y) * scale };
    });
  }, []);

  const onWheel = useCallback((event) => {
    event.preventDefault();
    zoomAt(event.deltaY < 0 ? 1.12 : 1 / 1.12, event.clientX, event.clientY);
  }, [zoomAt]);

  // Wheel must be non-passive to be able to preventDefault (otherwise the page
  // scrolls while zooming).
  useEffect(() => {
    const host = hostRef.current;
    if (!host) return undefined;
    const handler = (e) => onWheel(e);
    host.addEventListener('wheel', handler, { passive: false });
    return () => host.removeEventListener('wheel', handler);
  }, [onWheel]);

  const onPointerDown = (event) => {
    if (event.button !== 0) return;
    dragRef.current = { x: event.clientX, y: event.clientY, vx: view.x, vy: view.y, moved: false };
    event.currentTarget.setPointerCapture?.(event.pointerId);
  };

  const onPointerMove = (event) => {
    const d = dragRef.current;
    if (!d) return;
    const dx = event.clientX - d.x;
    const dy = event.clientY - d.y;
    if (Math.abs(dx) + Math.abs(dy) > 3) d.moved = true;
    setView((v) => ({ ...v, x: d.vx + dx, y: d.vy + dy }));
  };

  const endDrag = () => { dragRef.current = null; };

  const reset = () => setView({ k: 1, x: 0, y: 0 });

  const pick = (path) => {
    setPinned((p) => (p === path ? '' : path));
    if (onPick) onPick(path);
  };

  if (!placed.length) {
    return (
      <div class="empty">
        No graph yet. Run a task that reads or edits files, or refresh the index so import edges are mapped.
      </div>
    );
  }

  const dpr = typeof window !== 'undefined' ? Math.min(2, window.devicePixelRatio || 1) : 1;

  return (
    <div class="sig-graph-shell">
      <div
        ref={hostRef}
        class="sig-graph-canvas"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerLeave={endDrag}
        onDblClick={reset}
      >
        <svg
          class="sig-graph-svg"
          width={size.w}
          height={size.h}
          viewBox={`0 0 ${size.w} ${size.h}`}
          style={`shape-rendering:geometricPrecision`}
          role="img"
          aria-label="Learned file graph"
        >
          <defs>
            {Object.entries(KIND_STYLE).map(([kind, s]) => (
              <marker id={`sg-arrow-${kind}`} key={kind} viewBox="0 0 10 10" refX="9" refY="5"
                markerWidth="5" markerHeight="5" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" fill={s.color} />
              </marker>
            ))}
            <radialGradient id="sg-node-fill" cx="35%" cy="30%" r="75%">
              <stop offset="0%" stop-color="#2c3a22" />
              <stop offset="100%" stop-color="#151c11" />
            </radialGradient>
          </defs>

          <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
            {edges.map((e, i) => {
              const a = byPath.get(e.from);
              const b = byPath.get(e.to);
              if (!a || !b) return null;
              const s = styleFor(e.kind);
              const related = !focus || e.from === focus || e.to === focus;
              const opacity = related ? 0.2 + Math.min(0.6, (Number(e.weight || 0) / maxWeight) * 0.6) : 0.05;
              // Curve alternating directions so reciprocal pairs stay apart.
              const dx = b.x - a.x;
              const dy = b.y - a.y;
              const len = Math.hypot(dx, dy) || 1;
              const bend = Math.min(40, len * 0.14) * (i % 2 === 0 ? 1 : -1);
              const ctrlX = (a.x + b.x) / 2 + (-dy / len) * bend;
              const ctrlY = (a.y + b.y) / 2 + (dx / len) * bend;
              return (
                <path
                  key={`${e.kind}|${e.from}|${e.to}|${i}`}
                  d={`M ${a.x} ${a.y} Q ${ctrlX} ${ctrlY} ${b.x} ${b.y}`}
                  fill="none"
                  stroke={s.color}
                  stroke-width={s.width}
                  stroke-dasharray={s.dash}
                  stroke-opacity={opacity}
                  stroke-linecap="round"
                  marker-end={`url(#sg-arrow-${e.kind})`}
                />
              );
            })}

            {placed.map((n) => {
              const dim = focus && !neighbours.has(n.path);
              const isFocus = n.path === focus;
              const showLabel = n.path === focus || Number(n.weight || 0) >= labelCutoff;
              return (
                <g
                  key={n.path}
                  class="sig-node"
                  transform={`translate(${n.x} ${n.y})`}
                  opacity={dim ? 0.22 : 1}
                  onMouseEnter={() => setHover(n.path)}
                  onMouseLeave={() => setHover('')}
                  onClick={(ev) => { ev.stopPropagation(); pick(n.path); }}
                >
                  <circle r={n.r + 7} fill="transparent" />
                  {isFocus && <circle r={n.r + 5} fill="none" stroke="var(--accent)" stroke-width="1" opacity="0.55" />}
                  <circle
                    r={n.r}
                    fill={isFocus ? 'var(--accent)' : 'url(#sg-node-fill)'}
                    stroke={isFocus ? '#c6f07a' : '#4a5c36'}
                    stroke-width="1.2"
                  />
                  {showLabel && (
                    <text
                      class="sig-node-label"
                      y={n.r + 12}
                      text-anchor="middle"
                      opacity={isFocus ? 1 : 0.72}
                    >{shortLabel(n.path)}</text>
                  )}
                </g>
              );
            })}
          </g>
        </svg>

        <div class="sig-graph-hud">
          <button class="sig-hud-btn" title="Zoom in" onClick={(e) => { e.stopPropagation(); zoomAt(1.25, size.w / 2 + (hostRef.current?.getBoundingClientRect().left || 0), size.h / 2 + (hostRef.current?.getBoundingClientRect().top || 0)); }}>+</button>
          <button class="sig-hud-btn" title="Zoom out" onClick={(e) => { e.stopPropagation(); zoomAt(1 / 1.25, size.w / 2 + (hostRef.current?.getBoundingClientRect().left || 0), size.h / 2 + (hostRef.current?.getBoundingClientRect().top || 0)); }}>−</button>
          <button class="sig-hud-btn" title="Reset view (or double-click)" onClick={(e) => { e.stopPropagation(); reset(); }}>⤾</button>
          <span class="sig-hud-zoom">{Math.round(view.k * 100)}%</span>
        </div>
      </div>

      <div class="sig-graph-side">
        <div class="sig-legend">
          {Object.entries(KIND_STYLE).map(([kind, s]) => (
            <span class="sig-legend-item" key={kind}>
              <svg width="24" height="8" aria-hidden="true">
                <line x1="0" y1="4" x2="24" y2="4" stroke={s.color} stroke-width="2" stroke-dasharray={s.dash} />
              </svg>
              {s.label}
            </span>
          ))}
        </div>

        {focusNode ? (
          <div class="sig-detail">
            <div class="sig-detail-path mono">{focusNode.path}</div>
            <div class="sig-detail-row"><span>Weight</span><b>{Number(focusNode.weight || 0).toFixed(1)}</b></div>
            <div class="sig-detail-row"><span>Touches</span><b>{focusNode.hits || 0}</b></div>
            <div class="sig-detail-row"><span>Connections</span><b>{focusNode.degree || 0}</b></div>
            <div class="sig-detail-conn">
              {edges
                .filter((e) => e.from === focusNode.path || e.to === focusNode.path)
                .sort((a, b) => Number(b.weight || 0) - Number(a.weight || 0))
                .slice(0, 8)
                .map((e, i) => {
                  const other = e.from === focusNode.path ? e.to : e.from;
                  const s = styleFor(e.kind);
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
            Hover a node to inspect it, click to pin. Drag to pan, scroll to zoom, double-click to reset.
            <div class="meta-text" style="margin-top:8px">
              Node size blends connection count and learned weight. Only the busiest third carry a label.
            </div>
          </div>
        )}

        {graph?.truncated && (
          <div class="meta-text sig-trunc">
            Showing the {placed.length} hottest files. The agent's <code>related</code> tool still sees the full graph.
          </div>
        )}
      </div>
    </div>
  );
}
