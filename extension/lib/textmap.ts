/**
 * DOM ↔ text-offset mapping for contenteditable roots.
 *
 * The engine checks a plain-text serialization; suggestions come back as
 * UTF-16 offsets into that exact string. This module owns both directions:
 * building the string (recording which Text node each run came from) and
 * resolving an offset back to a (node, offset) DOM position.
 *
 * Isolated and dependency-free on purpose — it is the most fragile piece of
 * the extension and the one worth unit-testing hardest.
 */

export interface TextRun {
  node: Text;
  /** Offset of this run's first character within the serialized text. */
  start: number;
}

export interface TextMap {
  text: string;
  runs: TextRun[];
}

const BLOCK_TAGS = new Set([
  'ADDRESS', 'ARTICLE', 'ASIDE', 'BLOCKQUOTE', 'DIV', 'DL', 'DT', 'DD',
  'FIELDSET', 'FIGURE', 'FOOTER', 'FORM', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6',
  'HEADER', 'HR', 'LI', 'MAIN', 'NAV', 'OL', 'P', 'PRE', 'SECTION', 'TABLE',
  'TD', 'TH', 'TR', 'UL',
]);

const SKIP_TAGS = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE']);

/** Serializes root's visible text, recording node positions as it goes. */
export function buildTextMap(root: Element): TextMap {
  let text = '';
  const runs: TextRun[] = [];

  const ensureNewline = () => {
    if (text !== '' && !text.endsWith('\n')) text += '\n';
  };

  const walk = (node: Node): void => {
    if (node.nodeType === Node.TEXT_NODE) {
      const t = node as Text;
      if (t.data.length > 0) {
        runs.push({ node: t, start: text.length });
        text += t.data;
      }
      return;
    }
    if (!(node instanceof Element) || SKIP_TAGS.has(node.tagName)) return;
    if (node.tagName === 'BR') {
      text += '\n';
      return;
    }
    const block = BLOCK_TAGS.has(node.tagName);
    if (block) ensureNewline();
    for (const child of Array.from(node.childNodes)) walk(child);
    if (block) ensureNewline();
  };

  walk(root);
  return { text, runs };
}

export interface DomPoint {
  node: Text;
  offset: number;
}

/**
 * Resolves a text offset to a DOM point, or null if it falls on synthetic
 * text (block-boundary newlines) or on a node no longer in the document.
 */
export function resolvePoint(map: TextMap, offset: number): DomPoint | null {
  const runs = map.runs;
  if (runs.length === 0) return null;

  // Binary search: last run with start <= offset.
  let lo = 0;
  let hi = runs.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (runs[mid].start <= offset) lo = mid;
    else hi = mid - 1;
  }
  const run = runs[lo];
  if (offset < run.start) return null;
  const within = offset - run.start;
  if (within > run.node.data.length) return null; // synthetic newline / gap
  if (!run.node.isConnected) return null;
  return { node: run.node, offset: within };
}

/** Builds a DOM Range for a suggestion span; null if either end is unmappable. */
export function rangeFromSpan(map: TextMap, start: number, end: number): Range | null {
  if (end <= start) return null;
  const from = resolvePoint(map, start);
  const to = resolvePoint(map, end);
  if (!from || !to) return null;
  const range = document.createRange();
  try {
    range.setStart(from.node, from.offset);
    range.setEnd(to.node, to.offset);
  } catch {
    return null;
  }
  if (range.collapsed) return null;
  return range;
}
