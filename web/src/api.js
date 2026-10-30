// Foreman dashboard API client. Every request is project-scoped; the Go
// server rejects requests whose project path does not match its workspace.
const project = new URLSearchParams(location.search).get('project') || '';

const apiPath = (path) => `${path}?project=${encodeURIComponent(project)}`;

// avatarPath builds the URL used by <img> tags for an agent avatar. Avatars are
// served by the Go server (cached SVGs) and fall back to initials in the UI.
export function avatarPath(agentId, seed = '') {
  if (!agentId) return '';
  const base = `/api/agents/${encodeURIComponent(agentId)}/avatar?project=${encodeURIComponent(project)}`;
  return seed ? `${base}&v=${encodeURIComponent(seed)}` : base;
}

export async function api(path, options = {}) {
  const response = await fetch(apiPath(path), {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
  return body;
}

export function loadWorkspace() {
  return api('/api/workspace');
}
