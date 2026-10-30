import { useState, useMemo } from 'preact/hooks';
import { html as renderDiff } from 'diff2html';
import 'diff2html/bundles/css/diff2html.min.css';
import { Icon } from './icons.jsx';

function classify(line) {
  if (line.startsWith('+') && !line.startsWith('+++')) return 'add';
  if (line.startsWith('-') && !line.startsWith('---')) return 'del';
  if (line.startsWith('@@')) return 'hunk';
  return 'ctx';
}

// parseDiff turns a unified diff into file sections. Text without diff headers
// but with +/- lines is treated as a single "output" section. Each section also
// keeps `raw` — the verbatim unified-diff lines — so a real diff renderer
// (diff2html) can be used for the selected file.
export function parseDiff(text) {
  const source = String(text || '');
  if (!source.trim()) return [];
  const lines = source.split('\n');
  const files = [];
  let current = null;
  let sawHeader = false;

  for (const line of lines) {
    if (line.startsWith('diff --git ')) {
      sawHeader = true;
      const match = /diff --git a\/(.+?) b\/(.+)$/.exec(line);
      current = { path: match ? match[2] : line.slice('diff --git '.length), lines: [], raw: [line] };
      files.push(current);
      continue;
    }
    if (line.startsWith('--- ') || line.startsWith('+++ ')) {
      if (!current) {
        current = { path: line.slice(4).trim(), lines: [], raw: [line] };
        files.push(current);
      } else {
        current.raw.push(line);
      }
      continue;
    }
    if (line.startsWith('index ') || line.startsWith('new file') || line.startsWith('deleted file') || line.startsWith('similarity index')) {
      if (current) current.raw.push(line);
      continue;
    }
    if (line.startsWith('@@')) {
      sawHeader = true;
      if (!current) {
        current = { path: 'changes', lines: [], raw: [] };
        files.push(current);
      }
      current.lines.push({ type: 'hunk', text: line });
      current.raw.push(line);
      continue;
    }
    if (current) {
      current.lines.push({ type: classify(line), text: line });
      current.raw.push(line);
    }
  }

  if (!sawHeader) {
    const prepared = lines.map((line) => ({ type: classify(line), text: line }));
    const hasChanges = prepared.some((line) => line.type === 'add' || line.type === 'del');
    if (!hasChanges) return [];
    return [{ path: 'agent output', lines: prepared, raw: null }];
  }
  return files.filter((file) => file.lines.length);
}

// hasRenderableDiff reports whether a file section carries a well-formed
// unified diff (with at least one hunk) that diff2html can render.
export function hasRenderableDiff(file) {
  return Array.isArray(file?.raw) && file.raw.some((line) => line.startsWith('@@'));
}

export function diffStats(files) {
  let added = 0;
  let removed = 0;
  for (const file of files) {
    for (const line of file.lines) {
      if (line.type === 'add') added += 1;
      if (line.type === 'del') removed += 1;
    }
  }
  return { added, removed };
}

// numberLines walks a file's diff lines and assigns old/new line numbers by
// tracking the @@ hunk headers, so the diff can show real file line numbers.
export function numberLines(lines) {
  let oldNo = 0;
  let newNo = 0;
  const out = [];
  for (const line of lines) {
    if (line.type === 'hunk') {
      const m = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line.text || '');
      if (m) {
        oldNo = parseInt(m[1], 10);
        newNo = parseInt(m[2], 10);
      }
      out.push({ ...line, oldNo: '', newNo: '' });
      continue;
    }
    if (line.type === 'add') {
      out.push({ ...line, oldNo: '', newNo: newNo || '' });
      if (newNo) newNo += 1;
      continue;
    }
    if (line.type === 'del') {
      out.push({ ...line, oldNo: oldNo || '', newNo: '' });
      if (oldNo) oldNo += 1;
      continue;
    }
    // context line
    out.push({ ...line, oldNo: oldNo || '', newNo: newNo || '' });
    if (oldNo) oldNo += 1;
    if (newNo) newNo += 1;
  }
  return out;
}

// splitPath renders a path as a dim directory prefix + a bold basename.
function splitPath(path) {
  const parts = String(path || '').split('/');
  const base = parts.pop() || path;
  return { dir: parts.length ? parts.join('/') + '/' : '', base };
}

function FileSection({ file, collapsed, onToggle }) {
  const added = file.lines.filter((line) => line.type === 'add').length;
  const removed = file.lines.filter((line) => line.type === 'del').length;
  return (
    <section class="diff-file">
      <button type="button" class={`diff-file-head ${collapsed ? 'collapsed' : ''}`} onClick={onToggle}>
        <Icon name="chevron-down" size={13} class="diff-caret" />
        <Icon name="file-code" size={14} />
        <span class="diff-path" title={file.path}>{file.path}</span>
        <span class="diff-count add">+{added}</span>
        <span class="diff-count del">-{removed}</span>
      </button>
      {!collapsed && (
        <div class="diff-body">
          {file.lines.map((line) => (
            <div class={`diff-line ${line.type}`}>
              <span class="diff-sign">{line.type === 'add' ? '+' : line.type === 'del' ? '-' : line.type === 'hunk' ? '' : ' '}</span>
              <span class="diff-text">{line.text || ' '}</span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

export function DiffView({ text }) {  const files = parseDiff(text);
  const [collapsed, setCollapsed] = useState(() => new Set(files.map((file) => file.path)));
  if (!files.length) return null;
  const stats = diffStats(files);
  const allCollapsed = files.every((file) => collapsed.has(file.path));

  const toggle = (path) => setCollapsed((prev) => {
    const next = new Set(prev);
    if (next.has(path)) next.delete(path); else next.add(path);
    return next;
  });

  const toggleAll = () => {
    setCollapsed(allCollapsed ? new Set() : new Set(files.map((file) => file.path)));
  };

  return (
    <div class="diff-view">
      <div class="diff-toolbar">
        <span class="diff-summary">
          {files.length} file{files.length === 1 ? '' : 's'} · <b class="add">+{stats.added}</b> · <b class="del">-{stats.removed}</b>
        </span>
        <button type="button" class="btn small" onClick={toggleAll}>
          <Icon name={allCollapsed ? 'eye' : 'eye-off'} size={13} />{allCollapsed ? 'Show all' : 'Collapse all'}
        </button>
      </div>
      {files.map((file) => (
        <FileSection
          key={file.path}
          file={file}
          collapsed={collapsed.has(file.path)}
          onToggle={() => toggle(file.path)}
        />
      ))}
    </div>
  );
}
/*
 * ChangesView — GitHub/Bitbucket-style review layout:
 * a collapsible directory tree on the left, the selected file's diff on the right.
 */

/* mergeFiles: several edits to the same file produce multiple events;
 * keep only the LATEST section per file (it is the cumulative diff). */
export function mergeFiles(files) {
  const byPath = new Map();
  for (const file of files || []) {
    const path = file.path || 'changes';
    const prev = byPath.get(path);
    if (prev) {
      prev.lines = file.lines || [];
      prev.raw = file.raw || null;
    } else {
      byPath.set(path, { path, lines: [...(file.lines || [])], raw: file.raw || null });
    }
  }
  // preserve original order of first appearance
  const order = [];
  for (const file of files || []) {
    if (order.some((f) => f.path === (file.path || 'changes'))) continue;
    order.push(byPath.get(file.path || 'changes'));
  }
  return order;
}

function buildTree(files) {
  const root = { name: '', path: '', dirs: new Map(), files: [] };
  for (const file of files) {
    const parts = String(file.path || '').split('/');
    let node = root;
    for (let i = 0; i < parts.length - 1; i++) {
      const name = parts[i];
      if (!node.dirs.has(name)) {
        const path = node.path ? `${node.path}/${name}` : name;
        node.dirs.set(name, { name, path, dirs: new Map(), files: [] });
      }
      node = node.dirs.get(name);
    }
    node.files.push(file);
  }
  return root;
}

function subtreeStats(node) {
  let added = 0;
  let removed = 0;
  let count = 0;
  const walk = (n) => {
    for (const f of n.files) {
      count += 1;
      for (const l of f.lines) {
        if (l.type === 'add') added += 1;
        if (l.type === 'del') removed += 1;
      }
    }
    for (const d of n.dirs.values()) walk(d);
  };
  walk(node);
  return { added, removed, count };
}

export function ChangesView({ text }) {
  const merged = mergeFiles(parseDiff(text));
  const tree = useMemo(() => buildTree(merged), [merged]);
  const [openFile, setOpenFile] = useState(() => (merged.length ? merged[0].path : null));
  const [collapsed, setCollapsed] = useState(() => new Set());
  const [mode, setMode] = useState('split');

  if (!merged.length) return null;

  const toggleFolder = (path) => setCollapsed((prev) => {
    const next = new Set(prev);
    if (next.has(path)) next.delete(path); else next.add(path);
    return next;
  });

  const renderNode = (node, depth, isDir) => {
    if (isDir) {
      const isCollapsed = collapsed.has(node.path);
      const stat = subtreeStats(node);
      return (
        <div key={`dir-${node.path}`}>
          <div
            class={`tree-row ${isCollapsed ? '' : ''}`}
            style={`padding-left:${6 + depth * 14}px`}
            onClick={() => toggleFolder(node.path)}
            title={node.path}
          >
            <Icon name={isCollapsed ? 'chevron-right' : 'chevron-down'} size={12} class="tree-icon" />
            <Icon name="folder" size={12} class="tree-icon" />
            <span class="tree-name">{node.name}</span>
            <span class="tree-meta">
              <span class="tag" style="margin-left:2px">{stat.count}</span>
              {stat.added > 0 ? <span class="diff-count add">+{stat.added}</span> : null}
              {stat.removed > 0 ? <span class="diff-count del">-{stat.removed}</span> : null}
            </span>
          </div>
          {!isCollapsed && (
            <div class="tree-children">
              {Array.from(node.dirs.values()).map((d) => renderNode(d, depth + 1, true))}
              {node.files.map((f) => renderNode(f, depth + 1, false))}
            </div>
          )}
        </div>
      );
    }
    const stat = { added: 0, removed: 0 };
    for (const l of node.lines) {
      if (l.type === 'add') stat.added += 1;
      if (l.type === 'del') stat.removed += 1;
    }
    const name = node.path.split('/').pop();
    return (
      <div key={`file-${node.path}`}>
        <div
          class={`tree-row file-row ${openFile === node.path ? 'active' : ''}`}
          style={`padding-left:${6 + depth * 14}px`}
          onClick={() => setOpenFile(node.path)}
          title={node.path}
        >
          <Icon name="file-code" size={12} class="tree-icon" />
          <span class="tree-file-main">
            <span class="tree-name">{name}</span>
            {node.path !== name ? <span class="tree-subpath">{node.path}</span> : null}
          </span>
          <span class="tree-meta">
            {stat.added > 0 ? <span class="diff-count add">+{stat.added}</span> : null}
            {stat.removed > 0 ? <span class="diff-count del">-{stat.removed}</span> : null}
          </span>
        </div>
      </div>
    );
  };

  const dirsTop = Array.from(tree.dirs.values());
  const filesTop = tree.files;
  const selected = merged.find((f) => f.path === openFile) || null;
  const numbered = selected ? numberLines(selected.lines) : [];
  const crumb = selected ? splitPath(selected.path) : { dir: '', base: '' };
  const canRender = !!selected && hasRenderableDiff(selected);
  const selStat = selected
    ? {
      added: selected.lines.filter((l) => l.type === 'add').length,
      removed: selected.lines.filter((l) => l.type === 'del').length,
    }
    : { added: 0, removed: 0 };
  const renderedHTML = (() => {
    if (!selected || !canRender || mode === 'unified') return '';
    try {
      return renderDiff(selected.raw.join('\n'), {
        drawFileList: false,
        matching: 'lines',
        outputFormat: 'side-by-side',
        colorScheme: 'dark',
      });
    } catch {
      return '';
    }
  })();

  return (
    <div class="changes-layout">
      <div class="changes-tree" role="tree">
        <div class="changes-tree-head">Changed files</div>
        {dirsTop.length === 0 && filesTop.length === 0 ? (
          <div class="empty" style="padding:10px">No changes yet.</div>
        ) : null}
        {dirsTop.map((d) => renderNode(d, 0, true))}
        {filesTop.map((f) => renderNode(f, 0, false))}
      </div>
      <div class="changes-diff">
        {selected ? (
          <>
            <div class="diff-toolbar changes-diff-toolbar">
              <span class="diff-crumb" title={selected.path}>
                {crumb.dir ? <span class="diff-crumb-dir">{crumb.dir}</span> : null}
                <b class="diff-crumb-name">{crumb.base}</b>
              </span>
              <span class="diff-summary">
                <b class="add">+{selStat.added}</b> · <b class="del">-{selStat.removed}</b>
              </span>
              {canRender && (
                <span class="diff-mode-toggle">
                  <button type="button" class={mode === 'split' ? 'active' : ''} onClick={() => setMode('split')}>Split</button>
                  <button type="button" class={mode === 'unified' ? 'active' : ''} onClick={() => setMode('unified')}>Unified</button>
                </span>
              )}
            </div>
            {renderedHTML ? (
              <div class="d2h-wrap" dangerouslySetInnerHTML={{ __html: renderedHTML }} />
            ) : (
              <div class="diff-file changes-diff-file">
                <div class="diff-body diff-body-ln">
                  {numbered.map((line) => (
                    <div class={`diff-line ${line.type}`}>
                      <span class="diff-ln">{line.oldNo}</span>
                      <span class="diff-ln">{line.newNo}</span>
                      <span class="diff-sign">{line.type === 'add' ? '+' : line.type === 'del' ? '-' : line.type === 'hunk' ? '' : ' '}</span>
                      <span class="diff-text">{line.text || ' '}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </>
        ) : (
          <div class="empty">Select a file in the tree to view its diff.</div>
        )}
      </div>
    </div>
  );
}
