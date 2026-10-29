// Minimal, dependency-free markdown renderer for previews and chat bubbles.
// Supports headings, fenced code, lists, blockquotes, rules, bold/italic,
// inline code, and links. Raw HTML is never injected.

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

const BLOCK_START = /^(#{1,6}\s|```|>\s?|\s*[-*+]\s+|\s*\d+\.\s+)/;

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

    // Paragraph
    const buffer = [];
    while (i < lines.length && lines[i].trim() && !BLOCK_START.test(lines[i])) {
      buffer.push(lines[i]);
      i += 1;
    }
    if (!buffer.length) { i += 1; continue; }
    blocks.push(<p key={key++} class="md-p">{inline(buffer.join(' '), `p${key}`)}</p>);
  }

  if (!blocks.length) {
    return <div class={`md md-empty ${cls}`}>Nothing to preview yet.</div>;
  }
  return <div class={`md ${cls}`}>{blocks}</div>;
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