import { browser } from 'wxt/browser';
import { DEFAULT_SETTINGS, type HealthResult, type PairResult, type ToneSettings } from '@/lib/types';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;
const scheme = $<HTMLSelectElement>('scheme');
const host = $<HTMLInputElement>('host');
const port = $<HTMLInputElement>('port');
const token = $<HTMLInputElement>('token');
const disabled = $<HTMLTextAreaElement>('disabled');
const showIndicator = $<HTMLInputElement>('show-indicator');
const status = $<HTMLSpanElement>('status');
const remoteHint = $<HTMLParagraphElement>('remote-hint');

const isLocal = (h: string) => ['127.0.0.1', 'localhost', '::1'].includes(h);

async function load(): Promise<void> {
  const s = (await browser.storage.local.get({ ...DEFAULT_SETTINGS })) as unknown as ToneSettings;
  scheme.value = s.scheme;
  host.value = s.host;
  port.value = String(s.port);
  token.value = s.token;
  disabled.value = s.disabledSites.join('\n');
  showIndicator.checked = s.showIndicator !== false;
  updateRemoteUI();
}

function updateRemoteUI(): void {
  const local = isLocal(host.value.trim() || DEFAULT_SETTINGS.host);
  remoteHint.hidden = local;
  $<HTMLButtonElement>('autoconnect').disabled = !local;
  $<HTMLButtonElement>('autoconnect').title = local
    ? ''
    : 'Auto-connect only works for a local engine — paste the token manually for remote.';
}

/** Remote hosts need an origin permission the manifest doesn't include. */
async function ensureHostPermission(s: ToneSettings): Promise<boolean> {
  if (isLocal(s.host)) return true;
  const origin = `${s.scheme}://${s.host}/*`;
  const granted = await browser.permissions.contains({ origins: [origin] });
  if (granted) return true;
  return browser.permissions.request({ origins: [origin] });
}

async function save(): Promise<ToneSettings | null> {
  const s: ToneSettings = {
    scheme: scheme.value === 'https' ? 'https' : 'http',
    host: host.value.trim().toLowerCase() || DEFAULT_SETTINGS.host,
    port: Math.max(1, Math.min(65535, parseInt(port.value, 10) || DEFAULT_SETTINGS.port)),
    token: token.value.trim(),
    disabledSites: disabled.value
      .split('\n')
      .map((l) => l.trim().toLowerCase())
      .filter(Boolean),
    showIndicator: showIndicator.checked,
  };
  if (!(await ensureHostPermission(s))) {
    setStatus('Permission for the remote host was declined.', 'err');
    return null;
  }
  await browser.storage.local.set({ ...s });
  setStatus('Saved.', 'ok');
  return s;
}

async function test(): Promise<void> {
  if (!(await save())) return;
  setStatus('Testing…', '');
  const res = (await browser.runtime.sendMessage({ type: 'tone:health' })) as HealthResult;
  if (res.ok) {
    setStatus(
      res.status === 'ok'
        ? `Connected — model ${res.model ?? 'unknown'}`
        : `Engine reachable, but status: ${res.status}`,
      res.status === 'ok' ? 'ok' : 'err',
    );
  } else {
    const msgs: Record<string, string> = {
      not_paired: 'No pairing token set — try “Connect automatically”.',
      bad_token: 'Engine rejected the token — re-pair or re-copy it.',
      engine_unreachable: 'Engine not reachable — is it running?',
    };
    setStatus(msgs[res.error] ?? res.error, 'err');
  }
}

async function autoconnect(): Promise<void> {
  if (!(await save())) return;
  setStatus('Requested — approve in the engine (tray icon → “Approve pairing request”, or its settings page)…', '');
  $<HTMLButtonElement>('autoconnect').disabled = true;
  try {
    const res = (await browser.runtime.sendMessage({ type: 'tone:pair' })) as PairResult;
    if (res.ok) {
      await load();
      setStatus('Paired ✓', 'ok');
    } else {
      const msgs: Record<string, string> = {
        denied: 'Request denied on the engine.',
        timeout: 'Timed out — approve within 2 minutes and try again.',
        engine_unreachable: 'Engine not reachable — is it running?',
        too_many: 'Too many pending requests — wait a minute and retry.',
      };
      setStatus(msgs[res.error] ?? res.error, 'err');
    }
  } finally {
    $<HTMLButtonElement>('autoconnect').disabled = false;
    updateRemoteUI();
  }
}

function setStatus(text: string, cls: '' | 'ok' | 'err'): void {
  status.textContent = text;
  status.className = `hint ${cls}`.trim();
}

host.addEventListener('input', updateRemoteUI);
$<HTMLButtonElement>('save').addEventListener('click', () => void save());
$<HTMLButtonElement>('test').addEventListener('click', () => void test());
$<HTMLButtonElement>('autoconnect').addEventListener('click', () => void autoconnect());
void load();
