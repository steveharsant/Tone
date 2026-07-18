/**
 * Tone content script: finds editables, wires them to FieldSessions, and
 * runs the shared popover + highlight plumbing.
 */

import { defineContentScript } from '#imports';
import { browser } from 'wxt/browser';
import { StatusIndicator } from '@/lib/indicator';
import { Popover } from '@/lib/popover';
import { Rewriter } from '@/lib/rewriter';
import { FieldSession, type EditableKind } from '@/lib/session';
import { CATEGORY_COLORS, type SiteStatus, type Suggestion } from '@/lib/types';

export default defineContentScript({
  matches: ['<all_urls>'],
  async main() {
    let status: SiteStatus;
    try {
      status = (await browser.runtime.sendMessage({ type: 'tone:siteStatus' })) as SiteStatus;
    } catch {
      return;
    }
    if (!status?.enabled) return;

    injectHighlightStyles();

    // Lazy: the pill only enters the DOM once an editable is focused, so
    // pages the user never types on stay untouched.
    let indicator: StatusIndicator | null = null;
    let indicatorEnabled = status.showIndicator;
    const onState: ConstructorParameters<typeof FieldSession>[3] = (state, detail) => {
      indicator ??= new StatusIndicator();
      indicator.setEnabled(indicatorEnabled);
      indicator.set(state, detail);
    };
    browser.storage.onChanged.addListener((changes) => {
      if ('showIndicator' in changes) {
        indicatorEnabled = changes.showIndicator.newValue !== false;
        indicator?.setEnabled(indicatorEnabled);
      }
    });

    const sessions = new Set<FieldSession>();
    const sessionByEl = new WeakMap<HTMLElement, FieldSession>();

    // --- global CE highlight registry (CSS.highlights is document-wide) ---
    const highlightsSupported = typeof CSS !== 'undefined' && 'highlights' in CSS;
    const rebuildHighlights = () => {
      if (!highlightsSupported) return;
      const byCat = new Map<string, Range[]>();
      for (const session of sessions) {
        for (const [cat, ranges] of session.rangesByCategory()) {
          byCat.set(cat, [...(byCat.get(cat) ?? []), ...ranges]);
        }
      }
      for (const cat of Object.keys(CATEGORY_COLORS)) {
        const ranges = byCat.get(cat) ?? [];
        CSS.highlights.set(`tone-${cat}`, new Highlight(...ranges));
      }
    };

    // --- popover ---------------------------------------------------------
    let popoverOwner: FieldSession | null = null;
    const popover = new Popover({
      onAccept: (s: Suggestion) => popoverOwner?.accept(s),
      onDismiss: (s: Suggestion) => popoverOwner?.dismiss(s),
      onIgnoreRule: (s: Suggestion) => {
        if (!s.rule) return;
        void browser.runtime.sendMessage({ type: 'tone:ignoreRule', rule: s.rule }).catch(() => {});
        for (const session of sessions) session.removeByRule(s.rule);
      },
      onAddWord: (s: Suggestion) => {
        void browser.runtime.sendMessage({ type: 'tone:addWord', word: s.original.trim() }).catch(() => {});
        for (const session of sessions) session.removeByWord(s.original);
      },
      onSnooze: (s: Suggestion) => popoverOwner?.snooze(s, 24),
    });

    // Selection rewrites: "✦ Rewrite" button on selections in tracked fields.
    new Rewriter((target) => {
      if (target instanceof HTMLElement) {
        for (const session of sessions) {
          if (session.el === target || session.el.contains(target)) return session;
        }
      }
      return null;
    });

    let lastMove = 0;
    document.addEventListener(
      'mousemove',
      (e) => {
        const now = performance.now();
        if (now - lastMove < 80) return;
        lastMove = now;
        for (const session of sessions) {
          if (!session.alive) {
            session.destroy();
            sessions.delete(session);
            continue;
          }
          const hit = session.hitTest(e.clientX, e.clientY);
          if (hit) {
            popoverOwner = session;
            popover.show(hit.rect, hit.suggestion);
            return;
          }
        }
        if (popover.activeSuggestion) {
          // Sticky popover: as long as the pointer is in or near it (or on
          // its way there), keep it up; otherwise hide after a grace period.
          if (popover.containsPoint(e.clientX, e.clientY)) popover.cancelHide();
          else popover.scheduleHide();
        }
      },
      { passive: true },
    );

    // --- field discovery ---------------------------------------------------
    document.addEventListener(
      'focusin',
      (e) => {
        const resolved = resolveEditable(e.target);
        if (!resolved) return;
        const [el, kind] = resolved;
        let session = sessionByEl.get(el);
        if (!session) {
          session = new FieldSession(el, kind, rebuildHighlights, onState);
          sessionByEl.set(el, session);
          sessions.add(session);
        }
        session.schedule();
      },
      { passive: true },
    );
  },
});

function resolveEditable(target: EventTarget | null): [HTMLElement, EditableKind] | null {
  if (!(target instanceof HTMLElement)) return null;
  if (target instanceof HTMLTextAreaElement) {
    return target.readOnly || target.disabled ? null : [target, 'form'];
  }
  if (target instanceof HTMLInputElement) {
    if (target.readOnly || target.disabled) return null;
    return ['text', 'email'].includes(target.type) ? [target, 'form'] : null;
  }
  if (target.isContentEditable) {
    // Climb to the editing host (topmost contenteditable ancestor).
    let host: HTMLElement = target;
    while (host.parentElement?.isContentEditable) host = host.parentElement;
    return [host, 'ce'];
  }
  return null;
}

function injectHighlightStyles(): void {
  if (typeof CSS === 'undefined' || !('highlights' in CSS)) return;
  const style = document.createElement('style');
  style.textContent = Object.entries(CATEGORY_COLORS)
    .map(
      ([cat, color]) =>
        `::highlight(tone-${cat}) { text-decoration: underline wavy ${color} 1.5px; text-decoration-skip-ink: none; }`,
    )
    .join('\n');
  (document.head ?? document.documentElement).append(style);
}
