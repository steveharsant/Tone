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
import { CATEGORY_COLORS, type CheckResult, type Suggestion } from './types';

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

  private suggestions: Suggestion[] = [];
  private dismissed = new Set<string>();
  private lastCheckedText = '';
  private debounceTimer: number | undefined;
  private ceRendered: CERendered[] = [];
  private formRendered: FormRendered[] = [];
  private overlay: HTMLElement | null = null;
  private onRerender: () => void;

  constructor(el: HTMLElement, kind: EditableKind, onRerender: () => void) {
    this.el = el;
    this.kind = kind;
    this.onRerender = onRerender;

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
    // Typing invalidates offsets; hide rather than mislead. The engine's
    // sentence cache makes the re-check cheap for unchanged sentences.
    this.clearRender();
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

    let res: CheckResult;
    try {
      res = (await browser.runtime.sendMessage({ type: 'tone:check', text })) as CheckResult;
    } catch {
      return; // extension context invalidated or background asleep — retry on next input
    }
    if (!res?.ok) return; // engine down / unpaired: background handles the badge

    // Discard stale results: the user kept typing while we were checking.
    if (this.getText() !== text) return;
    this.lastCheckedText = text;
    this.suggestions = res.suggestions.filter((s) => !this.dismissed.has(keyOf(s)));
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
  }
}

/** Dismissals should survive re-checks, whose suggestions get fresh ids. */
function keyOf(s: Suggestion): string {
  return `${s.category} ${s.original} ${s.replacement}`;
}
