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

export type HealthResult =
  | { ok: true; status: string; model?: string }
  | { ok: false; error: string; disconnected?: boolean };

export interface SiteStatus {
  enabled: boolean;
  paired: boolean;
}

export interface ToneSettings {
  port: number;
  token: string;
  disabledSites: string[];
}

export const DEFAULT_SETTINGS: ToneSettings = {
  port: 8765,
  token: '',
  disabledSites: [],
};
