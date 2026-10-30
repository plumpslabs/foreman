import { useState } from 'preact/hooks';
import { avatarPath } from './api.js';

// toneClass maps a tone name to the existing avatar tile styles.
function toneClass(tone) {
  switch (tone) {
    case 'green': return 'agent-avatar green';
    case 'blue': return 'agent-avatar blue';
    case 'purple': return 'agent-avatar purple';
    case 'orange': return 'agent-avatar orange';
    default: return 'agent-avatar';
  }
}

// AgentAvatar renders an agent's generated avatar (a cached SVG served by the
// Go server) and falls back to initials when the image is unavailable — for
// example when the agent has not been saved yet or the server is offline and
// the avatar was never cached.
//
// Pass either a full `agent` object (with id/name/avatarSeed) or just a `name`.
export function AgentAvatar({ agent, name, tone = '', size, radius, className = '', style = '' }) {
  const [failed, setFailed] = useState(false);
  const label = name || agent?.name || '?';
  const id = agent?.id;
  const seed = agent?.avatarSeed || '';
  const box = [
    size ? `width:${size}px` : '',
    size ? `height:${size}px` : '',
    radius != null ? `border-radius:${radius}px` : '',
    style,
  ].filter(Boolean).join(';');
  const klass = className || toneClass(tone);

  if (id && !failed) {
    return (
      <div class={klass} style={box}>
        <img class="agent-avatar-img" src={avatarPath(id, seed)} alt={label}
          loading="lazy" onError={() => setFailed(true)} />
      </div>
    );
  }
  return <div class={klass} style={box}>{String(label).slice(0, 2).toUpperCase()}</div>;
}

// findAgent resolves an agent object by name from the workspace state so
// name-only render sites can still show a generated avatar.
export function findAgent(state, name) {
  if (!name) return null;
  const pool = [...(state?.agents || [])];
  for (const t of state?.teams || []) pool.push(...(t.agents || []));
  for (const tpl of state?.templates || []) pool.push(...(tpl.agents || []));
  return pool.find((a) => a.name === name) || null;
}
