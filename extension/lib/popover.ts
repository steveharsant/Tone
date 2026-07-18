/**
 * The suggestion popover: a single shadow-DOM instance shared by all fields
 * on the page, so page CSS can't restyle it and we never leak styles out.
 */

import { CATEGORY_COLORS, CATEGORY_LABELS, type Suggestion } from './types';

export interface PopoverActions {
  onAccept: (s: Suggestion) => void;
  onDismiss: (s: Suggestion) => void;
  /** Permanently mute this suggestion's rule type ("Ignore all"). */
  onIgnoreRule: (s: Suggestion) => void;
  /** Add the flagged word to the custom dictionary. */
  onAddWord: (s: Suggestion) => void;
}

const STYLE = `
:host { all: initial; }
.pop {
  position: fixed; z-index: 2147483647; max-width: 340px; min-width: 220px;
  background: #ffffff; color: #1a1a1f; border: 1px solid #d9d9e0;
  border-radius: 10px; box-shadow: 0 6px 24px rgba(0,0,0,.16);
  font: 13.5px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif;
  padding: 12px 14px;
}
@media (prefers-color-scheme: dark) {
  .pop { background: #23232b; color: #ececf1; border-color: #3a3a44; }
  .repl .to { color: #fff; }
}
.cat { display: flex; align-items: center; gap: 6px; font-size: 11.5px;
  text-transform: uppercase; letter-spacing: .4px; opacity: .75; margin-bottom: 6px; }
.dot { width: 8px; height: 8px; border-radius: 50%; }
.repl { margin: 2px 0 6px; }
.repl .from { text-decoration: line-through; opacity: .6; }
.repl .arrow { margin: 0 6px; opacity: .5; }
.repl .to { font-weight: 600; }
.expl { opacity: .85; margin-bottom: 10px; }
.row { display: flex; gap: 8px; align-items: center; }
button { font: inherit; border-radius: 7px; padding: 5px 14px; cursor: pointer; border: 0; }
.accept { background: var(--cat, #4f6df5); color: #fff; }
.dismiss { background: transparent; color: inherit; opacity: .7; }
.dismiss:hover { opacity: 1; }
.more { display: flex; gap: 12px; margin-top: 9px; padding-top: 8px;
  border-top: 1px solid rgba(128,128,128,.25); flex-wrap: wrap; }
.more button { background: none; padding: 0; font-size: 12px; color: inherit;
  opacity: .6; text-decoration: underline; text-underline-offset: 2px; }
.more button:hover { opacity: 1; }
`;

export class Popover {
  private host: HTMLElement;
  private pop: HTMLElement;
  private current: Suggestion | null = null;
  private hideTimer: number | undefined;

  constructor(private actions: PopoverActions) {
    this.host = document.createElement('tone-popover');
    // Open so tests (and curious users) can inspect it; style isolation is
    // identical to closed mode.
    const shadow = this.host.attachShadow({ mode: 'open' });
    const style = document.createElement('style');
    style.textContent = STYLE;
    this.pop = document.createElement('div');
    this.pop.className = 'pop';
    this.pop.style.display = 'none';
    shadow.append(style, this.pop);
    document.documentElement.append(this.host);

    this.pop.addEventListener('mouseenter', () => this.cancelHide());
    this.pop.addEventListener('mouseleave', () => this.scheduleHide());
    // Wobble tolerance: re-entering the popover's neighborhood keeps it up.
    this.pop.addEventListener('mousemove', () => this.cancelHide());
    document.addEventListener('scroll', () => this.hide(), { capture: true, passive: true });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') this.hide();
    });
  }

  get activeSuggestion(): Suggestion | null {
    return this.current;
  }

  show(anchor: DOMRect, s: Suggestion): void {
    this.cancelHide();
    if (this.current?.id === s.id && this.pop.style.display !== 'none') return;
    this.current = s;

    const color = CATEGORY_COLORS[s.category] ?? '#4f6df5';
    this.pop.style.setProperty('--cat', color);
    this.pop.textContent = '';

    const cat = el('div', 'cat');
    const dot = el('span', 'dot');
    dot.style.background = color;
    cat.append(dot, CATEGORY_LABELS[s.category] ?? s.category);

    const repl = el('div', 'repl');
    const from = el('span', 'from');
    from.textContent = clip(s.original, 60);
    const arrow = el('span', 'arrow');
    arrow.textContent = '→';
    const to = el('span', 'to');
    to.textContent = clip(s.replacement, 80);
    repl.append(from, arrow, to);

    const expl = el('div', 'expl');
    expl.textContent = s.explanation;

    const row = el('div', 'row');
    const accept = el('button', 'accept') as HTMLButtonElement;
    accept.textContent = 'Accept';
    accept.onclick = () => {
      this.actions.onAccept(s);
      this.hide();
    };
    const dismiss = el('button', 'dismiss') as HTMLButtonElement;
    dismiss.textContent = 'Dismiss';
    dismiss.onclick = () => {
      this.actions.onDismiss(s);
      this.hide();
    };
    row.append(accept, dismiss);

    // Secondary, quieter actions: permanent mutes.
    const more = el('div', 'more');
    if (s.rule) {
      const ignore = el('button', 'ignore') as HTMLButtonElement;
      ignore.textContent = `Ignore all “${s.rule}”`;
      ignore.title = 'Never flag this type of suggestion again';
      ignore.onclick = () => {
        this.actions.onIgnoreRule(s);
        this.hide();
      };
      more.append(ignore);
    }
    if (s.category === 'correctness' && /^\S+$/.test(s.original.trim())) {
      const addWord = el('button', 'add-word') as HTMLButtonElement;
      addWord.textContent = 'Add to dictionary';
      addWord.title = `Never flag “${s.original}” again`;
      addWord.onclick = () => {
        this.actions.onAddWord(s);
        this.hide();
      };
      more.append(addWord);
    }

    this.pop.append(cat, repl, expl, row);
    if (more.childElementCount > 0) this.pop.append(more);
    this.pop.style.display = 'block';

    // Position below the underline; flip above if it would overflow.
    const pw = this.pop.offsetWidth;
    const ph = this.pop.offsetHeight;
    let left = Math.min(Math.max(8, anchor.left), window.innerWidth - pw - 8);
    let top = anchor.bottom + 6;
    if (top + ph > window.innerHeight - 8) top = anchor.top - ph - 6;
    this.pop.style.left = `${left}px`;
    this.pop.style.top = `${top}px`;
  }

  /**
   * Grace period before hiding — generous on purpose: losing the popover to
   * a 2px mouse wobble means the user has to re-hover and wait again.
   */
  scheduleHide(delay = 700): void {
    if (this.hideTimer !== undefined) return; // already counting down
    this.hideTimer = window.setTimeout(() => this.hide(), delay);
  }

  /**
   * True when (x, y) is inside the popover or its surrounding grace margin —
   * the corridor between underline and popover counts as "still interested".
   */
  containsPoint(x: number, y: number, pad = 56): boolean {
    if (this.pop.style.display === 'none') return false;
    const r = this.pop.getBoundingClientRect();
    return x >= r.left - pad && x <= r.right + pad && y >= r.top - pad && y <= r.bottom + pad;
  }

  cancelHide(): void {
    if (this.hideTimer !== undefined) {
      clearTimeout(this.hideTimer);
      this.hideTimer = undefined;
    }
  }

  hide(): void {
    this.pop.style.display = 'none';
    this.current = null;
  }
}

function el(tag: string, className: string): HTMLElement {
  const e = document.createElement(tag);
  e.className = className;
  return e;
}

function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}
