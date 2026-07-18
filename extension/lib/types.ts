/** Wire types shared with the engine (see engine/internal/check/types.go). */

export interface Span {
  /** UTF-16 code-unit offsets into the checked text — native JS indexing. */
  start: number;
  end: number;
}

export type Category = 'correctness' | 'clarity' | 'engagement' | 'delivery';

export interface Suggestion {
  id: string;
  span: Span;
  original: string;
  replacement: string;
  category: Category;
  rule?: string;
  explanation: string;
  confidence: number;
}

export const CATEGORY_COLORS: Record<Category, string> = {
  correctness: '#e05252',
  clarity: '#4f6df5',
  engagement: '#2e9e5b',
  delivery: '#9b59b6',
};

export const CATEGORY_LABELS: Record<Category, string> = {
  correctness: 'Correctness',
  clarity: 'Clarity',
  engagement: 'Engagement',
  delivery: 'Delivery',
};

/** Messages between content script and background worker. */
export type CheckResult =
  | { ok: true; suggestions: Suggestion[] }
  | { ok: false; error: string; disconnected?: boolean };

/**
 * One message on a `tone:check` streaming port. Tiers arrive in priority
 * order (spelling → grammar → clarity → vocabulary → tone), each as soon as
 * its pass completes.
 */
export type CheckStreamMessage =
  | { tier: string; suggestions: Suggestion[] }
  | { done: true; score?: number }
  | { error: string; disconnected?: boolean };

export type RewriteResult =
  | { ok: true; rewritten: string }
  | { ok: false; error: string };

/** Selectable voices for the rewrite menu (mirrors the engine's list). */
export const REWRITE_TONES = [
  ['formal', 'More formal'],
  ['casual', 'More casual'],
  ['confident', 'More confident'],
  ['friendly', 'More friendly'],
  ['concise', 'More concise'],
] as const;

export type HealthResult =
  | { ok: true; status: string; model?: string }
  | { ok: false; error: string; disconnected?: boolean };

export interface SiteStatus {
  enabled: boolean;
  paired: boolean;
  showIndicator: boolean;
}

export interface ToneSettings {
  /** Engine host — 127.0.0.1 unless running the engine on a remote machine. */
  host: string;
  scheme: 'http' | 'https';
  port: number;
  token: string;
  disabledSites: string[];
  /** Show the bottom-right status pill on pages. */
  showIndicator: boolean;
}

export const DEFAULT_SETTINGS: ToneSettings = {
  host: '127.0.0.1',
  scheme: 'http',
  port: 8765,
  token: '',
  disabledSites: [],
  showIndicator: true,
};

export type PairResult =
  | { ok: true }
  | { ok: false; error: 'denied' | 'timeout' | 'engine_unreachable' | 'too_many' | string };
