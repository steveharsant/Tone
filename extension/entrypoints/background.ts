/**
 * Tone background worker — the ONLY component that talks to the engine.
 * Content scripts message it; it attaches the pairing token and reports
 * connection state via the toolbar badge. Stateless by design so MV3
 * service-worker suspension never loses anything.
 */

import { defineBackground } from '#imports';
import { browser } from 'wxt/browser';
import { DEFAULT_SETTINGS, type CheckResult, type HealthResult, type PairResult, type SiteStatus, type Suggestion, type ToneSettings } from '@/lib/types';

export default defineBackground(() => {
  browser.runtime.onMessage.addListener((msg: unknown, sender) => {
    const m = msg as { type?: string; text?: string };
    switch (m?.type) {
      case 'tone:check':
        return checkText(m.text ?? '');
      case 'tone:health':
        return health();
      case 'tone:siteStatus':
        return siteStatus(sender.url ?? sender.tab?.url);
      case 'tone:pair':
        return pair();
    }
  });
});

async function getSettings(): Promise<ToneSettings> {
  const stored = await browser.storage.local.get({ ...DEFAULT_SETTINGS });
  return stored as unknown as ToneSettings;
}

function engineURL(settings: ToneSettings, path: string): string {
  return `${settings.scheme}://${settings.host}:${settings.port}${path}`;
}

/**
 * Auto-connect: file a pairing request with the engine, then poll until the
 * user approves it from the engine's settings page or tray menu. The token
 * arrives on the approved poll and is stored immediately.
 */
async function pair(): Promise<PairResult> {
  const settings = await getSettings();
  let id: string;
  try {
    const resp = await fetch(engineURL(settings, '/api/pair/request'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ client: 'Tone browser extension' }),
      signal: AbortSignal.timeout(5_000),
    });
    if (resp.status === 429) return { ok: false, error: 'too_many' };
    if (!resp.ok) return { ok: false, error: `HTTP ${resp.status}` };
    id = ((await resp.json()) as { id: string }).id;
  } catch {
    return { ok: false, error: 'engine_unreachable' };
  }

  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 1_500));
    try {
      const resp = await fetch(engineURL(settings, `/api/pair/poll?id=${id}`), {
        signal: AbortSignal.timeout(5_000),
      });
      const data = (await resp.json()) as { status: string; token?: string; port?: number };
      if (data.status === 'approved' && data.token) {
        await browser.storage.local.set({ token: data.token, port: data.port ?? settings.port });
        await setBadge('');
        return { ok: true };
      }
      if (data.status === 'denied') return { ok: false, error: 'denied' };
      if (data.status === 'unknown') return { ok: false, error: 'timeout' };
    } catch {
      /* engine mid-restart; keep polling until the deadline */
    }
  }
  return { ok: false, error: 'timeout' };
}

async function checkText(text: string): Promise<CheckResult> {
  const settings = await getSettings();
  if (!settings.token) {
    await setBadge('set');
    return { ok: false, error: 'not_paired' };
  }
  try {
    const resp = await fetch(engineURL(settings, '/v1/check'), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${settings.token}`,
      },
      body: JSON.stringify({ text }),
      signal: AbortSignal.timeout(120_000),
    });
    if (resp.status === 401) {
      await setBadge('key');
      return { ok: false, error: 'bad_token' };
    }
    if (!resp.ok) {
      const body = (await resp.json().catch(() => ({}))) as { error?: string };
      await setBadge('err');
      return { ok: false, error: body.error ?? `HTTP ${resp.status}` };
    }
    const data = (await resp.json()) as { suggestions: Suggestion[] };
    await setBadge('');
    return { ok: true, suggestions: data.suggestions ?? [] };
  } catch {
    await setBadge('off');
    return { ok: false, error: 'engine_unreachable', disconnected: true };
  }
}

async function health(): Promise<HealthResult> {
  const settings = await getSettings();
  if (!settings.token) return { ok: false, error: 'not_paired' };
  try {
    const resp = await fetch(engineURL(settings, '/v1/health'), {
      headers: { Authorization: `Bearer ${settings.token}` },
      signal: AbortSignal.timeout(5_000),
    });
    if (resp.status === 401) return { ok: false, error: 'bad_token' };
    if (!resp.ok) return { ok: false, error: `HTTP ${resp.status}` };
    const data = (await resp.json()) as { status: string; provider?: { model?: string } };
    await setBadge(data.status === 'ok' ? '' : 'err');
    return { ok: true, status: data.status, model: data.provider?.model };
  } catch {
    await setBadge('off');
    return { ok: false, error: 'engine_unreachable', disconnected: true };
  }
}

async function siteStatus(url: string | undefined): Promise<SiteStatus> {
  const settings = await getSettings();
  let enabled = true;
  if (url) {
    try {
      const host = new URL(url).hostname;
      enabled = !settings.disabledSites.includes(host);
    } catch {
      /* non-URL sender (extension pages) stays enabled */
    }
  }
  return { enabled, paired: settings.token !== '' };
}

/** Badge doubles as the "engine not connected" indicator. */
async function setBadge(state: '' | 'off' | 'err' | 'set' | 'key'): Promise<void> {
  const action = browser.action ?? (browser as unknown as { browserAction: typeof browser.action }).browserAction;
  if (!action?.setBadgeText) return;
  const text = { '': '', off: 'off', err: '!', set: 'set', key: 'key' }[state];
  const color = { '': '#000', off: '#9a9aa5', err: '#c73a3a', set: '#c7841c', key: '#c7841c' }[state];
  try {
    await action.setBadgeText({ text });
    if (text) await action.setBadgeBackgroundColor({ color });
  } catch {
    /* badge is cosmetic; never let it break a check */
  }
}
