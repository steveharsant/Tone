/**
 * Page status indicator: a small pill fixed at the bottom-right showing what
 * Tone is doing — checking, done (with suggestion count), or engine trouble.
 * Hidden until the user focuses an editable, so ordinary browsing never sees
 * it. Can be disabled in the extension options (takes effect live).
 */

export type IndicatorState = 'idle' | 'checking' | 'done' | 'error';

const STYLE = `
:host { all: initial; }
.pill {
  position: fixed; right: 16px; bottom: 16px; z-index: 2147483647;
  display: flex; align-items: center; gap: 7px;
  background: rgba(28, 28, 34, .92); color: #ececf1;
  border-radius: 999px; padding: 6px 12px 6px 9px;
  font: 12px/1 system-ui, -apple-system, "Segoe UI", sans-serif;
  box-shadow: 0 2px 10px rgba(0,0,0,.25);
  opacity: 0; transform: translateY(6px);
  transition: opacity .25s ease, transform .25s ease;
  pointer-events: none; user-select: none;
}
@media (prefers-color-scheme: light) {
  .pill { background: rgba(255,255,255,.95); color: #1a1a1f; box-shadow: 0 2px 10px rgba(0,0,0,.18); }
}
.pill.visible { opacity: 1; transform: translateY(0); }
.pill.faded { opacity: .45; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: #9a9aa5; flex: none; }
.checking .dot { background: #4f6df5; animation: tone-pulse 1s ease-in-out infinite; }
.done .dot { background: #2e9e5b; }
.error .dot { background: #c73a3a; }
@keyframes tone-pulse {
  0%, 100% { transform: scale(1); opacity: 1; }
  50% { transform: scale(1.6); opacity: .55; }
}
.label:empty { display: none; }
`;

export class StatusIndicator {
  private pill: HTMLElement;
  private label: HTMLElement;
  private enabled = true;
  private shown = false;
  private fadeTimer: number | undefined;

  constructor() {
    const host = document.createElement('tone-status');
    const shadow = host.attachShadow({ mode: 'open' });
    const style = document.createElement('style');
    style.textContent = STYLE;
    this.pill = document.createElement('div');
    this.pill.className = 'pill';
    const dot = document.createElement('span');
    dot.className = 'dot';
    this.label = document.createElement('span');
    this.label.className = 'label';
    this.pill.append(dot, this.label);
    shadow.append(style, this.pill);
    document.documentElement.append(host);
  }

  setEnabled(enabled: boolean): void {
    this.enabled = enabled;
    if (!enabled) this.pill.classList.remove('visible');
    else if (this.shown) this.pill.classList.add('visible');
  }

  set(state: IndicatorState, detail = ''): void {
    this.shown = true;
    if (!this.enabled) return;
    if (this.fadeTimer !== undefined) {
      clearTimeout(this.fadeTimer);
      this.fadeTimer = undefined;
    }
    this.pill.className = `pill visible ${state}`;
    const labels: Record<IndicatorState, string> = {
      idle: '',
      checking: 'Checking…',
      done: detail,
      error: detail || 'Engine offline',
    };
    this.label.textContent = labels[state];
    // Settle: after a finished check, collapse to a faint idle dot so the
    // pill never nags. Errors stay visible until the state changes.
    if (state === 'done') {
      this.fadeTimer = window.setTimeout(() => {
        this.label.textContent = '';
        this.pill.classList.add('faded');
      }, 2500);
    }
  }
}
