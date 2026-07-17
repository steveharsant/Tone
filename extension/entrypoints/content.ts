/**
 * Tone content script: finds editables, wires them to FieldSessions, and
 * runs the shared popover + highlight plumbing.
 */

import { defineContentScript } from '#imports';
import { browser } from 'wxt/browser';
import { Popover } from '@/lib/popover';
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
        if (popover.activeSuggestion) popover.scheduleHide();
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
          session = new FieldSession(el, kind, rebuildHighlights);
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
