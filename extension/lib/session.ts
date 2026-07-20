/**
 * FieldSession: everything Tone knows about one editable on the page —
 * debounced checking, rendering underlines, hover hit-testing, and applying
 * accepted suggestions.
 *
 * Two rendering strategies:
 *  - contenteditable → CSS Custom Highlight API ranges (live, track edits)
 *  - textarea/input  → mirror measurement + overlay bars (form controls
 *    cannot host Ranges)
 */

import { browser } from 'wxt/browser';
import { measureSpans, toViewport, isVisibleIn, type FormField, type RelativeRect } from './mirror';
import { buildTextMap, pointToOffset, rangeFromSpan, type TextMap } from './textmap';
import { CATEGORY_COLORS, type CheckStreamMessage, type Suggestion } from './types';

/** A suggestion tagged with the priority tier that produced it. */
type TieredSuggestion = Suggestion & { tier?: string };

const DEBOUNCE_MS = 700;
const MIN_TEXT_LENGTH = 8;
const MAX_TEXT_LENGTH = 100_000;

export type EditableKind = 'form' | 'ce';

export interface Hit {
  suggestion: Suggestion;
  rect: DOMRect;
}

interface CERendered {
  suggestion: Suggestion;
  range: Range;
}

interface FormRendered {
  suggestion: Suggestion;
  rects: RelativeRect[];
}

export class FieldSession {
  readonly el: HTMLElement;
  readonly kind: EditableKind;

  private suggestions: TieredSuggestion[] = [];
  private dismissed = new Set<string>();
  private lastCheckedText = '';
  /** The text the current suggestion spans refer to (kept in sync on edits). */
  private renderedText = '';
  private debounceTimer: number | undefined;
  private checkPort: ReturnType<typeof browser.runtime.connect> | null = null;
  private ceRendered: CERendered[] = [];
  private formRendered: FormRendered[] = [];
  private overlay: HTMLElement | null = null;
  private onRerender: () => void;
  private onState: (state: 'checking' | 'done' | 'error', detail?: string) => void;

  constructor(
    el: HTMLElement,
    kind: EditableKind,
    onRerender: () => void,
    onState: (state: 'checking' | 'done' | 'error', detail?: string) => void = () => {},
  ) {
    this.el = el;
    this.kind = kind;
    this.onRerender = onRerender;
    this.onState = onState;

    el.addEventListener('input', this.handleInput);
    el.addEventListener('blur', this.handleBlur);
    if (kind === 'form') {
      el.addEventListener('scroll', this.repositionOverlay, { passive: true });
      window.addEventListener('scroll', this.repositionOverlay, { passive: true, capture: true });
      window.addEventListener('resize', this.repositionOverlay, { passive: true });
    }
  }

  get alive(): boolean {
    return this.el.isConnected;
  }

  destroy(): void {
    this.clearRender();
    this.el.removeEventListener('input', this.handleInput);
    this.el.removeEventListener('blur', this.handleBlur);
    if (this.kind === 'form') {
      this.el.removeEventListener('scroll', this.repositionOverlay);
      window.removeEventListener('scroll', this.repositionOverlay, { capture: true } as EventListenerOptions);
      window.removeEventListener('resize', this.repositionOverlay);
    }
    this.overlay?.remove();
    this.overlay = null;
  }

  // --- text access -------------------------------------------------------

  private map: TextMap | null = null;

  getText(): string {
    if (this.kind === 'form') return (this.el as FormField).value;
    this.map = buildTextMap(this.el);
    return this.map.text;
  }

  // --- checking ----------------------------------------------------------

  private handleInput = (): void => {
    // Keep underlines alive through edits: compute the single contiguous
    // edit (common prefix/suffix diff), shift every span after it, and drop
    // only suggestions the edit actually touched. The debounced re-check
    // then replaces everything quietly — no flicker on untouched text.
    const newText = this.getText();
    const edit = diffEdit(this.renderedText, newText);
    if (edit) {
      const delta = edit.newEnd - edit.oldEnd;
      this.suggestions = this.suggestions.filter((s) => {
        if (s.span.end <= edit.start) return true; // before the edit
        if (s.span.start >= edit.oldEnd) {
          s.span = { start: s.span.start + delta, end: s.span.end + delta };
          return true;
        }
        return false; // overlaps the edit
      });
      // Safety: a multi-caret or IME edit can defeat the single-edit model;
      // anything whose text no longer matches gets dropped, never misdrawn.
      this.suggestions = this.suggestions.filter(
        (s) => newText.slice(s.span.start, s.span.end) === s.original,
      );
      this.renderedText = newText;
      this.render();
    }
    this.schedule();
  };

  private handleBlur = (): void => {
    if (this.debounceTimer !== undefined) {
      clearTimeout(this.debounceTimer);
      this.debounceTimer = undefined;
      void this.runCheck();
    }
  };

  schedule(): void {
    if (this.debounceTimer !== undefined) clearTimeout(this.debounceTimer);
    this.debounceTimer = window.setTimeout(() => {
      this.debounceTimer = undefined;
      void this.runCheck();
    }, DEBOUNCE_MS);
  }

  async runCheck(): Promise<void> {
    const text = this.getText();
    if (text.trim().length < MIN_TEXT_LENGTH || text.length > MAX_TEXT_LENGTH) {
      this.suggestions = [];
      this.clearRender();
      return;
    }
    if (text === this.lastCheckedText) {
      this.render();
      return;
    }

    // One in-flight check at a time; superseding it aborts the old one all
    // the way down to the model.
    this.abortCheck();
    let port: ReturnType<typeof browser.runtime.connect>;
    try {
      port = browser.runtime.connect({ name: 'tone:check' });
    } catch {
      return; // extension context invalidated — retry on next input
    }
    this.checkPort = port;
    this.onState('checking');

    // Tiers arrive in priority order: spelling lands first and renders
    // immediately; later passes merge in without disturbing earlier ones.
    port.onMessage.addListener((raw: unknown) => {
      const msg = raw as CheckStreamMessage;
      if ('error' in msg) {
        this.onState('error', msg.disconnected ? 'Engine offline' : 'Check failed');
        this.finishCheck(port);
        return;
      }
      if ('done' in msg) {
        this.lastCheckedText = text;
        const n = this.suggestions.length;
        const scorePart = typeof msg.score === 'number' ? `Score ${msg.score} · ` : '';
        this.onState('done', scorePart + (n === 0 ? 'Looks good' : `${n} suggestion${n === 1 ? '' : 's'}`));
        this.finishCheck(port);
        return;
      }
      if (this.getText() !== text) {
        this.finishCheck(port); // stale: user typed while checking
        return;
      }
      this.mergeTier(msg.tier, msg.suggestions, text);
    });
    port.onDisconnect.addListener(() => {
      if (this.checkPort === port) this.checkPort = null;
    });
    port.postMessage({ text });
  }

  private abortCheck(): void {
    if (this.checkPort) {
      try {
        this.checkPort.disconnect();
      } catch {
        /* already gone */
      }
      this.checkPort = null;
    }
  }

  private finishCheck(port: ReturnType<typeof browser.runtime.connect>): void {
    if (this.checkPort === port) {
      try {
        port.disconnect();
      } catch {
        /* already gone */
      }
      this.checkPort = null;
    }
  }

  /**
   * Replaces this tier's previous suggestions and re-resolves overlaps by
   * TIER PRIORITY, not arrival order — tiers run in parallel, so a clarity
   * rewrite may land before the spelling fix hidden inside its span; the
   * spelling fix must still win when it arrives.
   */
  private mergeTier(tier: string, incoming: Suggestion[], checkText: string): void {
    const kept = this.suggestions.filter((s) => s.tier !== tier);
    const fresh: TieredSuggestion[] = incoming
      .filter((s) => !this.dismissed.has(keyOf(s)))
      .map((s) => ({ ...s, tier }));

    // Overlap precedence: within the correctness family (spelling+grammar
    // tiers), the LONGER span wins — it's the complete fix ("sum thing"→
    // "something" beats "sum"→"some"). Across categories, tier priority
    // wins so a clarity rewrite can't shadow a spelling fix.
    const candidates = [...kept, ...fresh].sort((a, b) => {
      if (a.category === 'correctness' && b.category === 'correctness') {
        const len = (b.span.end - b.span.start) - (a.span.end - a.span.start);
        if (len !== 0) return len;
      }
      const p = tierPriority(a.tier) - tierPriority(b.tier);
      return p !== 0 ? p : a.span.start - b.span.start;
    });
    const winners: TieredSuggestion[] = [];
    for (const c of candidates) {
      if (!winners.some((wn) => c.span.start < wn.span.end && c.span.end > wn.span.start)) {
        winners.push(c);
      }
    }
    this.suggestions = winners.sort((a, b) => a.span.start - b.span.start);
    this.renderedText = checkText;
    this.render();
  }

  // --- rendering ---------------------------------------------------------

  private clearRender(): void {
    this.ceRendered = [];
    this.formRendered = [];
    if (this.overlay) this.overlay.textContent = '';
    this.onRerender();
  }

  private render(): void {
    if (this.kind === 'ce') this.renderCE();
    else this.renderForm();
    this.onRerender();
  }

  /** Ranges for the global Highlight registry, grouped by category. */
  rangesByCategory(): Map<string, Range[]> {
    const out = new Map<string, Range[]>();
    for (const r of this.ceRendered) {
      const list = out.get(r.suggestion.category) ?? [];
      list.push(r.range);
      out.set(r.suggestion.category, list);
    }
    return out;
  }

  private renderCE(): void {
    this.ceRendered = [];
    if (!this.map) this.getText();
    const map = this.map;
    if (!map) return;
    for (const s of this.suggestions) {
      const range = rangeFromSpan(map, s.span.start, s.span.end);
      if (range) this.ceRendered.push({ suggestion: s, range });
    }
  }

  private renderForm(): void {
    const el = this.el as FormField;
    this.formRendered = [];
    const spans = this.suggestions.map((s) => s.span);
    const measured = measureSpans(el, spans);
    for (let i = 0; i < this.suggestions.length; i++) {
      if (measured[i]?.length) {
        this.formRendered.push({ suggestion: this.suggestions[i], rects: measured[i] });
      }
    }
    this.ensureOverlay();
    this.repositionOverlay();
  }

  private ensureOverlay(): void {
    if (this.overlay) return;
    this.overlay = document.createElement('tone-underlines');
    Object.assign(this.overlay.style, {
      position: 'fixed',
      inset: '0',
      pointerEvents: 'none',
      zIndex: '2147483646',
    });
    document.documentElement.append(this.overlay);
  }

  private repositionOverlay = (): void => {
    if (this.kind !== 'form' || !this.overlay) return;
    const el = this.el as FormField;
    this.overlay.textContent = '';
    if (!el.isConnected) return;
    for (const fr of this.formRendered) {
      const color = CATEGORY_COLORS[fr.suggestion.category] ?? '#4f6df5';
      for (const rel of fr.rects) {
        const rect = toViewport(el, rel);
        if (!isVisibleIn(el, rect)) continue;
        const bar = document.createElement('div');
        Object.assign(bar.style, {
          position: 'fixed',
          left: `${rect.left}px`,
          top: `${rect.bottom - 2}px`,
          width: `${rect.width}px`,
          height: '2px',
          borderRadius: '1px',
          background: color,
          opacity: '0.85',
        });
        this.overlay.append(bar);
      }
    }
  };

  // --- hover -------------------------------------------------------------

  hitTest(x: number, y: number): Hit | null {
    const pad = 4;
    if (this.kind === 'ce') {
      for (const r of this.ceRendered) {
        for (const rect of Array.from(r.range.getClientRects())) {
          if (x >= rect.left - pad && x <= rect.right + pad && y >= rect.top - pad && y <= rect.bottom + pad) {
            return { suggestion: r.suggestion, rect };
          }
        }
      }
      return null;
    }
    const el = this.el as FormField;
    if (!el.isConnected) return null;
    for (const fr of this.formRendered) {
      for (const rel of fr.rects) {
        const rect = toViewport(el, rel);
        if (!isVisibleIn(el, rect)) continue;
        if (x >= rect.left - pad && x <= rect.right + pad && y >= rect.top - pad && y <= rect.bottom + pad) {
          return { suggestion: fr.suggestion, rect };
        }
      }
    }
    return null;
  }

  // --- actions -----------------------------------------------------------

  accept(s: Suggestion): void {
    this.replaceRange(s.span.start, s.span.end, s.original, s.replacement);
  }

  /**
   * Replaces [start,end) with replacement, but only if the text there still
   * equals `original` — the shared safety gate for accepts and rewrites.
   */
  replaceRange(start: number, end: number, original: string, replacement: string): boolean {
    if (this.kind === 'form') {
      const el = this.el as FormField;
      if (el.value.slice(start, end) !== original) return false;
      el.focus();
      el.setRangeText(replacement, start, end, 'end');
      el.dispatchEvent(new InputEvent('input', { bubbles: true }));
      return true; // the input event shifts spans + reschedules
    }
    const map = buildTextMap(this.el);
    if (map.text.slice(start, end) !== original) return false;
    const range = rangeFromSpan(map, start, end);
    if (!range) return false;
    range.deleteContents();
    range.insertNode(document.createTextNode(replacement));
    const sel = window.getSelection();
    if (sel) {
      range.collapse(false);
      sel.removeAllRanges();
      sel.addRange(range);
    }
    this.el.dispatchEvent(new InputEvent('input', { bubbles: true }));
    return true;
  }

  /**
   * The user's current selection as a span in this session's text, or null
   * when there is no usable selection in this field.
   */
  getSelectionSpan(): { start: number; end: number; text: string } | null {
    if (this.kind === 'form') {
      const el = this.el as FormField;
      const s = el.selectionStart;
      const e = el.selectionEnd;
      if (s == null || e == null || e <= s) return null;
      return { start: s, end: e, text: el.value.slice(s, e) };
    }
    const sel = window.getSelection();
    if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;
    const range = sel.getRangeAt(0);
    if (!this.el.contains(range.commonAncestorContainer)) return null;
    const map = buildTextMap(this.el);
    const start = pointToOffset(map, range.startContainer, range.startOffset);
    const end = pointToOffset(map, range.endContainer, range.endOffset);
    if (start == null || end == null || end <= start) return null;
    return { start, end, text: map.text.slice(start, end) };
  }

  /** Selection rectangle in viewport coordinates (for anchoring UI). */
  selectionRect(span: { start: number; end: number }): DOMRect | null {
    if (this.kind === 'form') {
      const rects = measureSpans(this.el as FormField, [span]);
      const last = rects[0]?.[rects[0].length - 1];
      return last ? toViewport(this.el as FormField, last) : null;
    }
    const sel = window.getSelection();
    if (!sel || sel.rangeCount === 0) return null;
    const r = sel.getRangeAt(0).getBoundingClientRect();
    return r.width || r.height ? r : null;
  }

  /** Current suggestions in document order (for keyboard review). */
  getSuggestions(): Suggestion[] {
    return [...this.suggestions];
  }

  /** Viewport rect of a rendered suggestion (anchor for keyboard review). */
  rectFor(s: Suggestion): DOMRect | null {
    if (this.kind === 'ce') {
      const r = this.ceRendered.find((x) => x.suggestion.id === s.id);
      const rect = r?.range.getBoundingClientRect();
      return rect && (rect.width || rect.height) ? rect : null;
    }
    const fr = this.formRendered.find((x) => x.suggestion.id === s.id);
    if (!fr || fr.rects.length === 0) return null;
    return toViewport(this.el as FormField, fr.rects[0]);
  }

  snooze(s: Suggestion, hours: number): void {
    this.dismissedLocallyOnly(s);
    void browser.runtime
      .sendMessage({ type: 'tone:dismiss', category: s.category, original: s.original, hours })
      .catch(() => {});
  }

  private dismissedLocallyOnly(s: Suggestion): void {
    this.dismissed.add(keyOf(s));
    this.suggestions = this.suggestions.filter((x) => x.id !== s.id);
    this.clearRender();
    this.render();
  }

  dismiss(s: Suggestion): void {
    this.dismissedLocallyOnly(s);
    // Durable: the engine remembers dismissals across reloads and devices.
    void browser.runtime
      .sendMessage({ type: 'tone:dismiss', category: s.category, original: s.original })
      .catch(() => {});
  }

  /** Instantly strip every current suggestion of a muted rule type. */
  removeByRule(rule: string): void {
    const n = normalizeRule(rule);
    const before = this.suggestions.length;
    this.suggestions = this.suggestions.filter((x) => normalizeRule(x.rule ?? '') !== n);
    if (this.suggestions.length !== before) {
      this.clearRender();
      this.render();
    }
  }

  /** Instantly strip suggestions for a word just added to the dictionary. */
  removeByWord(word: string): void {
    const w = word.trim().toLowerCase();
    const before = this.suggestions.length;
    this.suggestions = this.suggestions.filter((x) => x.original.trim().toLowerCase() !== w);
    if (this.suggestions.length !== before) {
      this.clearRender();
      this.render();
    }
  }
}

/** Mirrors the engine's rule normalization (case + dash/space folding). */
function normalizeRule(rule: string): string {
  return rule.trim().toLowerCase().replace(/[ _]/g, '-');
}

const TIER_ORDER = ['spelling', 'grammar', 'clarity', 'vocabulary', 'tone'];

function tierPriority(tier: string | undefined): number {
  const i = TIER_ORDER.indexOf(tier ?? '');
  return i === -1 ? TIER_ORDER.length : i;
}

/**
 * Dismissals must survive re-checks. Keyed by category+original only: model
 * rewrites (especially clarity) vary their replacement wording run to run,
 * and a dismissed issue must not resurface with a new phrasing.
 */
function keyOf(s: Suggestion): string {
  return `${s.category} ${s.original}`;
}

interface Edit {
  start: number;
  oldEnd: number;
  newEnd: number;
}

/**
 * Models the difference between two texts as one contiguous replacement
 * (true for typing, deletion, paste, and accepted suggestions). Returns null
 * when the texts are identical.
 */
export function diffEdit(oldText: string, newText: string): Edit | null {
  if (oldText === newText) return null;
  let p = 0;
  const max = Math.min(oldText.length, newText.length);
  while (p < max && oldText[p] === newText[p]) p++;
  let oldEnd = oldText.length;
  let newEnd = newText.length;
  while (oldEnd > p && newEnd > p && oldText[oldEnd - 1] === newText[newEnd - 1]) {
    oldEnd--;
    newEnd--;
  }
  return { start: p, oldEnd, newEnd };
}
