/**
 * Selection rewrite UI: select text in a tracked field → a small ✦ button
 * appears → pick a tone → preview the rewrite → apply. The selection span
 * and text are captured when the button shows, so focus/selection changes
 * caused by clicking our own UI can't corrupt the replacement — and
 * replaceRange re-verifies the original text before touching anything.
 */

import { browser } from 'wxt/browser';
import type { FieldSession } from './session';
import { REWRITE_TONES, type RewriteResult } from './types';

const MIN_SELECTION = 15;
const MAX_SELECTION = 8000;

const STYLE = `
:host { all: initial; }
.root { position: fixed; z-index: 2147483647; font: 13px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif; }
.btn {
  display: flex; align-items: center; gap: 5px; cursor: pointer;
  background: #4f6df5; color: #fff; border: 0; border-radius: 999px;
  padding: 4px 11px; font: inherit; font-size: 12.5px;
  box-shadow: 0 2px 8px rgba(0,0,0,.25);
}
.panel {
  background: #fff; color: #1a1a1f; border: 1px solid #d9d9e0; border-radius: 10px;
  box-shadow: 0 6px 24px rgba(0,0,0,.18); padding: 8px; min-width: 180px; max-width: 380px;
}
.menu { display: flex; flex-direction: column; }
.menu button {
  text-align: left; background: none; border: 0; color: inherit; font: inherit;
  padding: 7px 10px; border-radius: 6px; cursor: pointer;
}
.menu button:hover { background: #f0f0f4; }
.status { padding: 10px 12px; opacity: .75; }
.preview-text {
  background: #f6f6f8; border-radius: 7px; padding: 9px 11px; margin: 4px 0 10px;
  max-height: 180px; overflow-y: auto; white-space: pre-wrap; font-size: 13px;
}
.row { display: flex; justify-content: flex-end; gap: 8px; padding: 0 2px 2px; }
.row button { font: inherit; border-radius: 7px; padding: 5px 14px; cursor: pointer; border: 0; }
.apply { background: #4f6df5; color: #fff; }
.cancel { background: transparent; color: inherit; opacity: .7; }
.err { color: #c73a3a; }
/* Dark overrides LAST: equal-specificity rules are order-decided, so this
 * block after the base rules is what makes dark mode actually win. */
@media (prefers-color-scheme: dark) {
  .panel { background: #23232b; color: #ececf1; border-color: #3a3a44; }
  .menu button:hover { background: #2e2e38; }
  .preview-text { background: #1a1a20; }
}
`;

interface Capture {
  session: FieldSession;
  start: number;
  end: number;
  text: string;
}

export class Rewriter {
  private root: HTMLElement;
  private captured: Capture | null = null;

  constructor(private sessionAt: (target: EventTarget | null) => FieldSession | null) {
    const host = document.createElement('tone-rewrite');
    const shadow = host.attachShadow({ mode: 'open' });
    const style = document.createElement('style');
    style.textContent = STYLE;
    this.root = document.createElement('div');
    this.root.className = 'root';
    this.root.style.display = 'none';
    shadow.append(style, this.root);
    document.documentElement.append(host);

    let timer: number | undefined;
    const onMaybeSelection = (e: Event) => {
      if (e.composedPath?.().includes(host)) return; // our own UI
      clearTimeout(timer);
      timer = window.setTimeout(() => this.evaluateSelection(e.target), 250);
    };
    document.addEventListener('mouseup', onMaybeSelection, { passive: true });
    document.addEventListener('keyup', onMaybeSelection, { passive: true });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') this.hide();
    });
    document.addEventListener('scroll', () => this.hide(), { capture: true, passive: true });
  }

  private evaluateSelection(target: EventTarget | null): void {
    // Only offer the button while idle; never yank an open menu/preview away.
    if (this.root.dataset.mode && this.root.dataset.mode !== 'button') return;
    const session = this.sessionAt(target) ?? this.sessionAt(document.activeElement);
    const span = session?.getSelectionSpan() ?? null;
    if (!session || !span || span.text.trim().length < MIN_SELECTION || span.text.length > MAX_SELECTION) {
      if (this.root.dataset.mode === 'button') this.hide();
      return;
    }
    this.captured = { session, start: span.start, end: span.end, text: span.text };
    const rect = session.selectionRect(span);
    if (!rect) return;
    this.root.dataset.mode = 'button';
    this.root.textContent = '';
    const btn = document.createElement('button');
    btn.className = 'btn';
    btn.textContent = '✦ Rewrite';
    btn.onclick = () => this.showMenu();
    this.root.append(btn);
    this.place(rect.right, rect.bottom + 6);
  }

  private showMenu(): void {
    this.root.dataset.mode = 'menu';
    const panel = this.panel();
    const menu = document.createElement('div');
    menu.className = 'menu';
    for (const [tone, label] of REWRITE_TONES) {
      const b = document.createElement('button');
      b.textContent = label;
      b.onclick = () => void this.runRewrite({ tone });
      menu.append(b);
    }
    const fix = document.createElement('button');
    fix.textContent = 'Just fix errors';
    fix.onclick = () =>
      void this.runRewrite({ instruction: 'Fix all spelling, grammar and punctuation errors without changing the style or tone' });
    menu.append(fix);
    panel.append(menu);
  }

  private async runRewrite(opts: { tone?: string; instruction?: string }): Promise<void> {
    const cap = this.captured;
    if (!cap) return;
    this.root.dataset.mode = 'busy';
    const panel = this.panel();
    const status = document.createElement('div');
    status.className = 'status';
    status.textContent = 'Rewriting…';
    panel.append(status);

    let res: RewriteResult;
    try {
      res = (await browser.runtime.sendMessage({ type: 'tone:rewrite', text: cap.text, ...opts })) as RewriteResult;
    } catch {
      res = { ok: false, error: 'extension error' };
    }
    if (this.root.dataset.mode !== 'busy') return; // user closed it meanwhile
    if (!res.ok) {
      status.textContent = res.error === 'engine_unreachable' ? 'Engine not reachable.' : `Rewrite failed: ${res.error}`;
      status.className = 'status err';
      return;
    }
    this.showPreview(cap, res.rewritten);
  }

  private showPreview(cap: Capture, rewritten: string): void {
    this.root.dataset.mode = 'preview';
    const panel = this.panel();
    const text = document.createElement('div');
    text.className = 'preview-text';
    text.textContent = rewritten;
    const row = document.createElement('div');
    row.className = 'row';
    const cancel = document.createElement('button');
    cancel.className = 'cancel';
    cancel.textContent = 'Cancel';
    cancel.onclick = () => this.hide();
    const apply = document.createElement('button');
    apply.className = 'apply';
    apply.textContent = 'Replace';
    apply.onclick = () => {
      const ok = cap.session.replaceRange(cap.start, cap.end, cap.text, rewritten);
      if (!ok) {
        text.insertAdjacentHTML('beforebegin', '<div class="status err">Text changed since selection — not applied.</div>');
        return;
      }
      this.hide();
    };
    row.append(cancel, apply);
    panel.append(text, row);
  }

  private panel(): HTMLElement {
    const { left, top } = this.root.getBoundingClientRect();
    this.root.textContent = '';
    const panel = document.createElement('div');
    panel.className = 'panel';
    this.root.append(panel);
    // Keep the panel on-screen once it grows.
    this.place(Math.min(left, window.innerWidth - 400), Math.min(top, window.innerHeight - 260));
    return panel;
  }

  private place(x: number, y: number): void {
    this.root.style.display = 'block';
    this.root.style.left = `${Math.max(8, Math.min(x, window.innerWidth - 120))}px`;
    this.root.style.top = `${Math.max(8, Math.min(y, window.innerHeight - 48))}px`;
  }

  hide(): void {
    this.root.style.display = 'none';
    this.root.textContent = '';
    delete this.root.dataset.mode;
    this.captured = null;
  }
}
