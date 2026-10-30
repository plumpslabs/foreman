import { Icon } from './icons.jsx';

/*
 * PlanGraph — a dependency/execution-flow view of a room's tasks.
 *
 * Kanban answers "what stage is this in"; this view answers "what has to wait
 * for what, and what can run in parallel". Each task is a node; an edge
 * A -> B means B depends on A (A blocks B). Nodes are laid out in layers by
 * longest dependency path, so anything in the same column can run in parallel.
 */

const NODE_W = 196;
const NODE_H = 58;
const GAP_X = 74;
const GAP_Y = 16;

const STATUS_LABEL = {
  todo: 'To do',
  backlog: 'Backlog',
  in_progress: 'In progress',
  review: 'Review',
  blocked: 'Blocked',
  done: 'Done',
  cancelled: 'Cancelled',
  paused: 'Paused',
};

// statusClass maps a task status to a colour bucket used by CSS.
function statusClass(status) {
  switch (status) {
    case 'done': return 'done';
    case 'in_progress': return 'running';
    case 'review': return 'review';
    case 'blocked': return 'blocked';
    case 'paused': return 'paused';
    case 'todo':
    case 'backlog': return 'todo';
    default: return 'idle';
  }
}

function shortName(name) {
  return String(name || '').replace(/[_-]+/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

export function PlanGraph({ tasks, onOpenTask }) {
  const list = (tasks || []).filter((t) => !t.hidden);
  if (!list.length) {
    return <div class="empty" style="margin:14px">No tasks yet. The lead agent creates cards here when it plans.</div>;
  }

  const byId = new Map(list.map((t) => [t.id, t]));
  const deps = new Map();
  const dependents = new Map();
  for (const t of list) {
    const ds = (t.dependencies || []).filter((d) => byId.has(d) && d !== t.id);
    deps.set(t.id, ds);
    for (const d of ds) {
      if (!dependents.has(d)) dependents.set(d, []);
      dependents.get(d).push(t.id);
    }
  }

  // Longest-path layering with a cycle guard.
  const layer = new Map();
  const visiting = new Set();
  const calc = (id) => {
    if (layer.has(id)) return layer.get(id);
    if (visiting.has(id)) return 0;
    visiting.add(id);
    let l = 0;
    for (const d of deps.get(id) || []) l = Math.max(l, calc(d) + 1);
    visiting.delete(id);
    layer.set(id, l);
    return l;
  };
  for (const t of list) calc(t.id);

  const cols = [];
  for (const t of list) {
    const l = layer.get(t.id) || 0;
    (cols[l] = cols[l] || []).push(t);
  }
  const maxRows = cols.reduce((m, c) => Math.max(m, (c || []).length), 1);

  const pos = new Map();
  cols.forEach((col, l) => {
    const rows = col || [];
    const offset = ((maxRows - rows.length) * (NODE_H + GAP_Y)) / 2;
    rows.forEach((t, i) => {
      pos.set(t.id, { x: l * (NODE_W + GAP_X), y: offset + i * (NODE_H + GAP_Y) });
    });
  });

  const width = Math.max(1, cols.length) * (NODE_W + GAP_X) - GAP_X;
  const height = maxRows * (NODE_H + GAP_Y) - GAP_Y;

  const edges = [];
  for (const t of list) {
    for (const d of deps.get(t.id) || []) {
      const from = pos.get(d);
      const to = pos.get(t.id);
      if (from && to) edges.push({ id: `${d}->${t.id}`, from, to, depId: d });
    }
  }

  const isDone = (id) => byId.get(id)?.status === 'done';
  const ready = list.filter((t) => (t.status === 'todo' || t.status === 'backlog')
    && (deps.get(t.id) || []).every(isDone));
  const waiting = list.filter((t) => (t.status === 'todo' || t.status === 'backlog')
    && (deps.get(t.id) || []).some((d) => !isDone(d)));
  const active = list.filter((t) => t.status === 'in_progress' || t.status === 'review');
  const doneCount = list.filter((t) => t.status === 'done').length;

  return (
    <div class="plan-graph-wrap">
      <div class="plan-graph" style={`width:${width + 24}px;height:${height + 24}px`}>
        <svg class="plan-edges" width={width + 24} height={height + 24} aria-hidden="true">
          <defs>
            <marker id="pg-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" fill="#3a4250" />
            </marker>
          </defs>
          {edges.map((e) => {
            const x1 = e.from.x + NODE_W + 12;
            const y1 = e.from.y + NODE_H / 2 + 12;
            const x2 = e.to.x + 12;
            const y2 = e.to.y + NODE_H / 2 + 12;
            const mid = Math.max(30, (x2 - x1) / 2);
            const satisfied = isDone(e.depId);
            return (
              <path
                key={e.id}
                d={`M ${x1} ${y1} C ${x1 + mid} ${y1}, ${x2 - mid} ${y2}, ${x2} ${y2}`}
                fill="none"
                stroke={satisfied ? '#4b7a3a' : '#3a4250'}
                stroke-width="1.5"
                marker-end="url(#pg-arrow)"
              />
            );
          })}
        </svg>
        {list.map((t) => {
          const p = pos.get(t.id);
          const n = (deps.get(t.id) || []).length;
          return (
            <button
              type="button"
              key={t.id}
              class={`pg-node ${statusClass(t.status)}`}
              style={`left:${p.x + 12}px;top:${p.y + 12}px;width:${NODE_W}px;height:${NODE_H}px`}
              onClick={() => onOpenTask && onOpenTask(t)}
              title={`${t.title}\n${STATUS_LABEL[t.status] || t.status}${n ? ` · waits on ${n}` : ''}`}
            >
              <span class="pg-node-title">{t.title}</span>
              <span class="pg-node-meta">
                <span class={`pg-status-dot ${statusClass(t.status)}`} />
                {STATUS_LABEL[t.status] || t.status}
                {t.agentName ? <> · @{shortName(t.agentName)}</> : null}
              </span>
            </button>
          );
        })}
      </div>

      <aside class="plan-side">
        <div class="plan-legend">
          <span class="pg-legend-item"><span class="pg-status-dot done" />Done</span>
          <span class="pg-legend-item"><span class="pg-status-dot running" />Running</span>
          <span class="pg-legend-item"><span class="pg-status-dot review" />Review</span>
          <span class="pg-legend-item"><span class="pg-status-dot todo" />To do</span>
          <span class="pg-legend-item"><span class="pg-status-dot blocked" />Blocked</span>
        </div>
        <div class="plan-flow-card">
          <div class="plan-flow-head">Ready now <span class="tag blue">{ready.length}</span></div>
          {ready.length ? ready.map((t) => (
            <div class="plan-flow-row" key={t.id} onClick={() => onOpenTask && onOpenTask(t)}>
              <Icon name="play" size={12} /><span>{t.title}</span>
            </div>
          )) : <div class="muted" style="font-size:11px">Nothing unblocked right now.</div>}
        </div>
        <div class="plan-flow-card">
          <div class="plan-flow-head">In progress <span class="tag blue">{active.length}</span></div>
          {active.length ? active.map((t) => (
            <div class="plan-flow-row" key={t.id} onClick={() => onOpenTask && onOpenTask(t)}>
              <Icon name="dot" size={12} /><span>{t.title}</span>
            </div>
          )) : <div class="muted" style="font-size:11px">Nothing running.</div>}
        </div>
        <div class="plan-flow-card">
          <div class="plan-flow-head">Waiting on deps <span class="tag">{waiting.length}</span></div>
          {waiting.length ? waiting.map((t) => (
            <div class="plan-flow-row" key={t.id} onClick={() => onOpenTask && onOpenTask(t)}>
              <Icon name="git-branch" size={12} /><span>{t.title}</span>
            </div>
          )) : <div class="muted" style="font-size:11px">No blocked work.</div>}
        </div>
        <div class="plan-progress">
          <div class="plan-progress-bar"><div class="plan-progress-fill" style={`width:${list.length ? Math.round((doneCount / list.length) * 100) : 0}%`} /></div>
          <span>{doneCount} / {list.length} done</span>
        </div>
      </aside>
    </div>
  );
}
