/**
 * Measuring suggestion positions inside <textarea>/<input>.
 *
 * You cannot create DOM Ranges inside form controls, so we replicate the
 * control's text in a hidden "mirror" element with identical typography and
 * measure marker spans there. Rects are stored relative to the mirror
 * content box, then translated into viewport coordinates using the live
 * element rect and scroll position — so scrolling only needs re-translation,
 * not re-measurement.
 */

import type { Span } from './types';

const MIRRORED_PROPS = [
  'boxSizing', 'width', 'paddingTop', 'paddingRight', 'paddingBottom',
  'paddingLeft', 'borderTopWidth', 'borderRightWidth', 'borderBottomWidth',
  'borderLeftWidth', 'fontFamily', 'fontSize', 'fontWeight', 'fontStyle',
  'fontVariant', 'lineHeight', 'letterSpacing', 'wordSpacing', 'textTransform',
  'textIndent', 'whiteSpace', 'wordBreak', 'overflowWrap', 'tabSize',
  'direction',
] as const;

export type FormField = HTMLTextAreaElement | HTMLInputElement;

export interface RelativeRect {
  left: number;
  top: number;
  width: number;
  height: number;
}

/** Measures each span's line rects, relative to the field's content origin. */
export function measureSpans(el: FormField, spans: Span[]): RelativeRect[][] {
  const mirror = document.createElement('div');
  const cs = getComputedStyle(el);
  for (const prop of MIRRORED_PROPS) {
    (mirror.style as unknown as Record<string, string>)[prop] = cs[prop as keyof CSSStyleDeclaration] as string;
  }
  // Textareas wrap; single-line inputs do not.
  if (el instanceof HTMLInputElement) {
    mirror.style.whiteSpace = 'pre';
  } else if (cs.whiteSpace === 'normal') {
    mirror.style.whiteSpace = 'pre-wrap';
  }
  mirror.style.position = 'fixed';
  mirror.style.top = '-9999px';
  mirror.style.left = '0';
  mirror.style.visibility = 'hidden';
  mirror.style.overflow = 'hidden';
  mirror.style.height = 'auto';
  mirror.style.width = `${el.getBoundingClientRect().width}px`;

  const value = el.value;
  const results: RelativeRect[][] = [];
  document.documentElement.appendChild(mirror);
  try {
    for (const span of spans) {
      mirror.textContent = '';
      mirror.append(document.createTextNode(value.slice(0, span.start)));
      const marker = document.createElement('span');
      marker.textContent = value.slice(span.start, span.end);
      mirror.append(marker);
      mirror.append(document.createTextNode(value.slice(span.end)));

      const origin = mirror.getBoundingClientRect();
      const csM = getComputedStyle(mirror);
      const padLeft = parseFloat(csM.borderLeftWidth);
      const padTop = parseFloat(csM.borderTopWidth);
      const rects: RelativeRect[] = [];
      for (const r of Array.from(marker.getClientRects())) {
        if (r.width <= 0) continue;
        rects.push({
          left: r.left - origin.left - padLeft,
          top: r.top - origin.top - padTop,
          width: r.width,
          height: r.height,
        });
      }
      results.push(rects);
    }
  } finally {
    mirror.remove();
  }
  return results;
}

/** Translates a mirror-relative rect into current viewport coordinates. */
export function toViewport(el: FormField, rect: RelativeRect): DOMRect {
  const box = el.getBoundingClientRect();
  const cs = getComputedStyle(el);
  const borderLeft = parseFloat(cs.borderLeftWidth);
  const borderTop = parseFloat(cs.borderTopWidth);
  return new DOMRect(
    box.left + borderLeft + rect.left - el.scrollLeft,
    box.top + borderTop + rect.top - el.scrollTop,
    rect.width,
    rect.height,
  );
}

/** True if the rect is inside the element's visible content box. */
export function isVisibleIn(el: FormField, rect: DOMRect): boolean {
  const box = el.getBoundingClientRect();
  return (
    rect.bottom > box.top + 1 &&
    rect.top < box.bottom - 1 &&
    rect.right > box.left + 1 &&
    rect.left < box.right - 1
  );
}
