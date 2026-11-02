import { useState, useEffect } from 'preact/hooks';
import { api } from './api.js';
import { Icon } from './icons.jsx';
import { ChangesView } from './diff.jsx';

/*
 * GitPanel — read-only snapshot of the workspace repository: current branch,
 * HEAD, uncommitted files, and recent commits, plus an optional switch to the
 * room's dedicated branch. Committing stays in the Approvals Inbox; this panel
 * only shows where things stand.
 */

const plain = (s) => String(s ?? '');

export function GitPanel({ roomId, notify, refresh }) {
  const [info, setInfo] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [diffOpen, setDiffOpen] = useState(false);
  const [diffText, setDiffText] = useState('');

  // Read the snapshot as soon as the panel appears, so the tab shows content
  // without the user having to click "Read repository" first.
  useEffect(() => {
    let alive = true;
    setError('');
    api(`/api/rooms/${encodeURIComponent(roomId)}/git`)
      .then((d) => { if (alive) setInfo(d || { isRepo: false }); })
      .catch((e) => { if (alive) { setError(e.message); setInfo(null); } });
    return () => { alive = false; };
  }, [roomId]);

  const load = async () => {
    setError('');
    try {
      const d = await api(`/api/rooms/${encodeURIComponent(roomId)}/git`);
      setInfo(d || { isRepo: false });
    } catch (e) {
      setError(e.message);
      setInfo(null);
    }
  };

  const loadDiff = async (open) => {
    setDiffOpen(open);
    if (!open) return;
    try {
      const d = await api(`/api/rooms/${encodeURIComponent(roomId)}/git/diff`);
      setDiffText(plain(d?.raw));
    } catch (e) {
      notify(e.message);
    }
  };

  const useRoomBranch = async () => {
    setBusy(true);
    try {
      const d = await api(`/api/rooms/${encodeURIComponent(roomId)}/git/prepare`, { method: 'POST' });
      notify(`Workspace is on ${plain(d?.branch || 'the room branch')}`);
      await load();
      if (refresh) await refresh();
    } catch (e) {
      notify(e.message);
    } finally {
      setBusy(false);
    }
  };

  if (!info) {
    return (
      <div class="room-view active">
        <div class="tab-scroll">
          <div class="section-title">Git</div>
          <div class="section-note" style="margin-top:5px">
            Branch, working-tree state, and recent commits for the workspace repository.
          </div>
          {error && <div class="notice">Could not read the repository: {plain(error)}</div>}
          <button class="btn subtle" style="margin-top:14px" onClick={load}>
            <Icon name="refresh" size={14} />Read repository
          </button>
        </div>
      </div>
    );
  }

  if (!info.isRepo) {
    return (
      <div class="room-view active">
        <div class="tab-scroll">
          <div class="section-title">Git</div>
          <div class="notice">This workspace is not a git repository yet. Run <code>git init</code> or clone a repo into the project folder, then reopen this tab.</div>
        </div>
      </div>
    );
  }

  return (
    <div class="room-view active">
      <div class="tab-scroll">
        <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px">
          <div>
            <div class="section-title">Git</div>
            <div class="section-note" style="margin-top:5px">
              Working-tree state of the project repository. Commits are still requested through the agent and approved in the Approvals Inbox.
            </div>
          </div>
          <button class="btn subtle" onClick={load} title="Refresh from git"><Icon name="refresh" size={14} /><span class="hide-sm">Refresh</span></button>
        </div>

        <div style="display:flex;gap:10px;flex-wrap:wrap;margin-top:14px;align-items:center">
          <span class="tag blue"><Icon name="git-branch" size={12} /> {plain(info.branch || 'detached HEAD')}</span>
          {info.headSha && <span class="tag" title={plain(info.head)}>{plain(info.headSha)}</span>}
          {info.aheadBehind && <span class="tag orange">{plain(info.aheadBehind)}</span>}
          <span class="tag">{info.dirty?.length ? `${info.dirty.length} changed` : 'clean tree'}</span>
          {info.dirty?.length > 0 && (
            <button class={`btn small ${diffOpen ? '' : 'subtle'}`} onClick={() => loadDiff(!diffOpen)}>
              {diffOpen ? 'Hide diff' : 'View diff'}
            </button>
          )}
        </div>

        {info.dirty?.length > 0 && (
          <div class="panel" style="margin-top:12px;padding:12px 16px">
            <div class="section-title" style="margin-bottom:8px">Uncommitted files</div>
            <div style="display:flex;flex-direction:column;gap:4px">
              {info.dirty.map((f) => (
                <div key={f} style="font-size:12px;font-family:var(--mono,monospace)">{plain(f)}</div>
              ))}
            </div>
          </div>
        )}

        {diffOpen && (
          <div class="panel" style="margin-top:12px;padding:12px 16px">
            {diffText.includes('diff --git')
              ? <ChangesView text={diffText} />
              : <div class="muted" style="font-size:12px">{diffText ? <pre style="white-space:pre-wrap;margin:0">{plain(diffText)}</pre> : 'No uncommitted diff.'}</div>}
          </div>
        )}

        <div style="margin-top:14px">
          <button class="btn subtle" onClick={useRoomBranch} disabled={busy}>
            <Icon name="git-branch" size={14} />
            Switch to room branch <code>foreman/{plain(roomId.slice(0, 8))}…</code>
          </button>
          <div class="section-note" style="margin-top:6px">
            Rooms that want isolated work can check out their own branch; switching keeps the rest of the workspace untouched.
          </div>
        </div>

        <div class="panel" style="margin-top:14px;padding:12px 16px">
          <div class="section-title" style="margin-bottom:8px">Recent commits</div>
          {(info.recentCommits || []).length ? info.recentCommits.map((c) => (
            <div key={c.sha} style="display:flex;gap:10px;align-items:baseline;padding:3px 0;font-size:12px">
              <span style="font-family:var(--mono,monospace);color:var(--text-soft,#888)">{plain(c.sha)}</span>
              <span style="min-width:0">{plain(c.subject)}</span>
              <span class="muted" style="margin-left:auto;white-space:nowrap">{plain(c.when)}</span>
            </div>
          )) : <div class="muted" style="font-size:12px">No commits yet.</div>}
        </div>
      </div>
    </div>
  );
}
