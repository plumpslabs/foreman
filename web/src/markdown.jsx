// Minimal, dependency-free markdown renderer for previews and chat bubbles.
// Supports headings, tables, fenced code, lists, blockquotes, rules,
// bold/italic, inline code, and links. Raw HTML is never injected.

import { useState } from 'preact/hooks';
import { Icon } from './icons.jsx';

const SAFE_HREF = /^(https?:\/\/|\/|#|mailto:)/i;

const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(__[^_\n]+__)|(\*[^*\n]+\*)|(_[^_\n]+_)|(\[[^\]]+\]\([^)\s]+\))/g;

function inline(text, keyBase) {
  const out = [];
  let last = 0;
  let match;
  let index = 0;
  while ((match = INLINE.exec(text)) !== null) {
    if (match.index > last) out.push(text.slice(last, match.index));
    const token = match[0];
    const key = `${keyBase}-${index++}`;
    if (token.startsWith('`')) {
      out.push(<code key={key} class="md-code">{token.slice(1, -1)}</code>);
    } else if (token.startsWith('**') || token.startsWith('__')) {
      out.push(<strong key={key}>{token.slice(2, -2)}</strong>);
    } else if (token.startsWith('[')) {
      const link = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(token);
      if (link && SAFE_HREF.test(link[2])) {
        out.push(<a key={key} href={link[2]} target="_blank" rel="noopener noreferrer">{link[1]}</a>);
      } else {
        out.push(token);
      }
    } else {
      out.push(<em key={key}>{token.slice(1, -1)}</em>);
    }
    last = INLINE.lastIndex;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

const BLOCK_START = /^(#{1,6}\s|```|>\s?|\s*[-*+]\s+|\s*\d+\.\s+|\s*\|)/;

// A GFM delimiter row: | --- | :--: | ---: |
const TABLE_DELIM = /^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)+\|?\s*$/;

// Split one table row into its cells, unescaping \| so cells can contain pipes.
function splitTableRow(line) {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|')) s = s.slice(0, -1);
  const cells = [];
  let cur = '';
  for (let i = 0; i < s.length; i += 1) {
    const ch = s[i];
    if (ch === '\\' && s[i + 1] === '|') { cur += '|'; i += 1; continue; }
    if (ch === '|') { cells.push(cur.trim()); cur = ''; continue; }
    cur += ch;
  }
  cells.push(cur.trim());
  return cells;
}

function tableAlign(cell) {
  const left = cell.startsWith(':');
  const right = cell.endsWith(':');
  if (left && right) return 'center';
  if (right) return 'right';
  return 'left';
}

export function Markdown({ text, class: cls = '' }) {
  const lines = String(text || '').replace(/\r\n/g, '\n').split('\n');
  const blocks = [];
  let i = 0;
  let key = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (!line.trim()) { i += 1; continue; }

    // Fenced code block
    if (/^```/.test(line.trim())) {
      const fence = line.trim().slice(3).trim();
      const buffer = [];
      i += 1;
      while (i < lines.length && !/^```/.test(lines[i].trim())) {
        buffer.push(lines[i]);
        i += 1;
      }
      i += 1;
      blocks.push(
        <pre key={key++} class="md-pre" data-lang={fence || undefined}>
          <code>{buffer.join('\n')}</code>
        </pre>,
      );
      continue;
    }

    // Heading
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const level = Math.min(heading[1].length, 6);
      blocks.push(
        <div key={key++} class={`md-h md-h${level}`}>{inline(heading[2], `h${key}`)}</div>,
      );
      i += 1;
      continue;
    }

    // Horizontal rule
    if (/^(-{3,}|\*{3,}|_{3,})$/.test(line.trim())) {
      blocks.push(<hr key={key++} class="md-hr" />);
      i += 1;
      continue;
    }

    // Blockquote
    if (/^>\s?/.test(line)) {
      const buffer = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) {
        buffer.push(lines[i].replace(/^>\s?/, ''));
        i += 1;
      }
      blocks.push(
        <blockquote key={key++} class="md-quote">{inline(buffer.join(' '), `q${key}`)}</blockquote>,
      );
      continue;
    }

    // Unordered list
    if (/^\s*[-*+]\s+/.test(line)) {
      const items = [];
      while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*[-*+]\s+/, ''));
        i += 1;
      }
      blocks.push(
        <ul key={key++} class="md-ul">
          {items.map((item, n) => <li key={n}>{inline(item, `li${key}-${n}`)}</li>)}
        </ul>,
      );
      continue;
    }

    // Ordered list
    if (/^\s*\d+\.\s+/.test(line)) {
      const items = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*\d+\.\s+/, ''));
        i += 1;
      }
      blocks.push(
        <ol key={key++} class="md-ol">
          {items.map((item, n) => <li key={n}>{inline(item, `oli${key}-${n}`)}</li>)}
        </ol>,
      );
      continue;
    }

    // Table: header row + delimiter row + body rows. Tables used to be
    // swallowed by the paragraph branch and joined with spaces, which is how a
    // report ended up as one unreadable wall of pipes.
    if (line.includes('|') && i + 1 < lines.length && TABLE_DELIM.test(lines[i + 1])) {
      const header = splitTableRow(line);
      const aligns = splitTableRow(lines[i + 1]).map(tableAlign);
      i += 2;
      const rows = [];
      while (i < lines.length && lines[i].trim() && lines[i].includes('|') && !TABLE_DELIM.test(lines[i])) {
        rows.push(splitTableRow(lines[i]));
        i += 1;
      }
      const cols = Math.max(header.length, ...rows.map((r) => r.length), 1);
      const cellAlign = (n) => (aligns[n] && aligns[n] !== 'left' ? ` md-a-${aligns[n]}` : '');
      const pad = (cells) => {
        const out = cells.slice(0, cols);
        while (out.length < cols) out.push('');
        return out;
      };
      blocks.push(
        <div key={key++} class="md-table-wrap">
          <table class="md-table">
            <thead>
              <tr>
                {pad(header).map((c, n) => (
                  <th key={n} class={cellAlign(n)}>{inline(c, `th${key}-${n}`)}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, rn) => (
                <tr key={rn}>
                  {pad(row).map((c, cn) => (
                    <td key={cn} class={cellAlign(cn)}>{inline(c, `td${key}-${rn}-${cn}`)}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>,
      );
      continue;
    }

    // Paragraph. Stop when a table begins even if there is no blank line
    // before it — reports commonly put the header right under a sentence.
    const buffer = [];
    while (
      i < lines.length
      && lines[i].trim()
      && !BLOCK_START.test(lines[i])
      && !(lines[i].includes('|') && i + 1 < lines.length && TABLE_DELIM.test(lines[i + 1]))
    ) {
      buffer.push(lines[i]);
      i += 1;
    }
    // BLOCK_START can match a line no branch above handles (e.g. a stray "|"
    // with no delimiter row after it). Emit it instead of dropping it.
    if (!buffer.length) { buffer.push(lines[i]); i += 1; }
    blocks.push(<p key={key++} class="md-p">{inline(buffer.join(' '), `p${key}`)}</p>);
  }

  if (!blocks.length) {
    return <div class={`md md-empty ${cls}`}>Nothing to preview yet.</div>;
  }
  return <div class={`md ${cls}`}>{blocks}</div>;
}

// Write/Preview editor used wherever agents or humans edit markdown (agent
// instructions, task descriptions). Lives here so every surface gets the same
// preview instead of falling back to a raw textarea.
export function MarkdownEditor({ value, onInput, placeholder, minHeight = 260, label = 'Markdown' }) {
  const [mode, setMode] = useState('write');
  return (
    <div class="md-editor">
      <div class="md-editor-bar">
        <span class="md-editor-label"><Icon name="file-text" size={12} />{label}</span>
        <div class="seg">
          <button type="button" class={`seg-btn ${mode === 'write' ? 'on' : ''}`} onClick={() => setMode('write')}>Write</button>
          <button type="button" class={`seg-btn ${mode === 'preview' ? 'on' : ''}`} onClick={() => setMode('preview')}>Preview</button>
        </div>
      </div>
      {mode === 'preview'
        ? <div class="md-preview" style={`min-height:${minHeight}px`}><Markdown text={value} /></div>
        : (
          <textarea
            class="code-area md-textarea"
            style={`min-height:${minHeight}px`}
            placeholder={placeholder}
            value={value}
            onInput={(e) => onInput(e.currentTarget.value)}
          />
        )}
    </div>
  );
}

export const STARTER_INSTRUCTIONS = `# Role

You are a focused engineering agent working inside a local workspace.

## Responsibilities

- Inspect the existing code before changing anything.
- Keep changes small, correct, and easy to review.
- Report evidence (files, commands, test results) for every claim.

## Workflow

1. Locate the relevant files and read them.
2. Write a short plan with bounded steps.
3. Make the smallest change that solves the task.
4. Run the relevant checks.
5. Summarize what changed and what is still risky.

## Guardrails

- Never touch secrets, credentials, or unrelated modules.
- Do not run destructive commands.
- Ask for approval before broad refactors.

## Output

- What I changed
- How I verified it
- Remaining risks
`;