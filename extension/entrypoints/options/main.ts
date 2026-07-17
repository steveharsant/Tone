import { browser } from 'wxt/browser';
import { DEFAULT_SETTINGS, type HealthResult, type ToneSettings } from '@/lib/types';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;
const port = $<HTMLInputElement>('port');
const token = $<HTMLInputElement>('token');
const disabled = $<HTMLTextAreaElement>('disabled');
const status = $<HTMLSpanElement>('status');

async function load(): Promise<void> {
  const s = (await browser.storage.local.get({ ...DEFAULT_SETTINGS })) as unknown as ToneSettings;
  port.value = String(s.port);
  token.value = s.token;
  disabled.value = s.disabledSites.join('\n');
}

async function save(): Promise<void> {
  const s: ToneSettings = {
    port: Math.max(1, Math.min(65535, parseInt(port.value, 10) || DEFAULT_SETTINGS.port)),
    token: token.value.trim(),
    disabledSites: disabled.value
      .split('\n')
      .map((l) => l.trim().toLowerCase())
      .filter(Boolean),
  };
  await browser.storage.local.set({ ...s });
  setStatus('Saved.', 'ok');
}

async function test(): Promise<void> {
  await save();
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
      not_paired: 'No pairing token set.',
      bad_token: 'Engine rejected the token — re-copy it from the engine settings page.',
      engine_unreachable: 'Engine not reachable — is it running?',
    };
    setStatus(msgs[res.error] ?? res.error, 'err');
  }
}

function setStatus(text: string, cls: '' | 'ok' | 'err'): void {
  status.textContent = text;
  status.className = `hint ${cls}`.trim();
}

$<HTMLButtonElement>('save').addEventListener('click', () => void save());
$<HTMLButtonElement>('test').addEventListener('click', () => void test());
void load();
