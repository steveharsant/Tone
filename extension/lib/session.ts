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
import { buildTextMap, rangeFromSpan, type TextMap } from './textmap';
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
        this.onState('done', n === 0 ? 'Looks good' : `${n} suggestion${n === 1 ? '' : 's'}`);
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

  /** Replaces this tier's previous suggestions; keeps other tiers intact. */
  private mergeTier(tier: string, incoming: Suggestion[], checkText: string): void {
    const kept = this.suggestions.filter((s) => s.tier !== tier);
    const overlapsKept = (s: Suggestion) =>
      kept.some((k) => s.span.start < k.span.end && s.span.end > k.span.start);
    const fresh: TieredSuggestion[] = incoming
      .filter((s) => !this.dismissed.has(keyOf(s)) && !overlapsKept(s))
      .map((s) => ({ ...s, tier }));
    this.suggestions = [...kept, ...fresh].sort((a, b) => a.span.start - b.span.start);
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
    if (this.kind === 'form') {
      const el = this.el as FormField;
      // Guard against drift: only apply if the text still matches.
      if (el.value.slice(s.span.start, s.span.end) !== s.original) return;
      el.focus();
      el.setRangeText(s.replacement, s.span.start, s.span.end, 'end');
      el.dispatchEvent(new InputEvent('input', { bubbles: true }));
      return; // the input event clears + reschedules
    }
    const map = buildTextMap(this.el);
    if (map.text.slice(s.span.start, s.span.end) !== s.original) return;
    const range = rangeFromSpan(map, s.span.start, s.span.end);
    if (!range) return;
    range.deleteContents();
    range.insertNode(document.createTextNode(s.replacement));
    const sel = window.getSelection();
    if (sel) {
      range.collapse(false);
      sel.removeAllRanges();
      sel.addRange(range);
    }
    this.el.dispatchEvent(new InputEvent('input', { bubbles: true }));
  }

  dismiss(s: Suggestion): void {
    this.dismissed.add(keyOf(s));
    this.suggestions = this.suggestions.filter((x) => x.id !== s.id);
    this.clearRender();
    this.render();
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
