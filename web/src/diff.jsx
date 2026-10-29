import { useState } from 'preact/hooks';
import { Icon } from './icons.jsx';

function classify(line) {
  if (line.startsWith('+') && !line.startsWith('+++')) return 'add';
  if (line.startsWith('-') && !line.startsWith('---')) return 'del';
  if (line.startsWith('@@')) return 'hunk';
  return 'ctx';
}

// parseDiff turns a unified diff into file sections. Text without diff headers
// but with +/- lines is treated as a single "output" section.
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
      current = { path: match ? match[2] : line.slice('diff --git '.length), lines: [] };
      files.push(current);
      continue;
    }
    if (line.startsWith('--- ') || line.startsWith('+++ ')) {
      if (!current) {
        current = { path: line.slice(4).trim(), lines: [] };
        files.push(current);
      }
      continue;
    }
    if (line.startsWith('index ') || line.startsWith('new file') || line.startsWith('deleted file') || line.startsWith('similarity index')) {
      continue;
    }
    if (line.startsWith('@@')) {
      sawHeader = true;
      if (!current) {
        current = { path: 'changes', lines: [] };
        files.push(current);
      }
      current.lines.push({ type: 'hunk', text: line });
      continue;
    }
    if (current) {
      current.lines.push({ type: classify(line), text: line });
    }
  }

  if (!sawHeader) {
    const prepared = lines.map((line) => ({ type: classify(line), text: line }));
    const hasChanges = prepared.some((line) => line.type === 'add' || line.type === 'del');
    if (!hasChanges) return [];
    return [{ path: 'agent output', lines: prepared }];
  }
  return files.filter((file) => file.lines.length);
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

export function DiffView({ text }) {
  const files = parseDiff(text);
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