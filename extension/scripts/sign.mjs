/**
 * Signs the Firefox build through addons.mozilla.org (unlisted channel),
 * producing an .xpi that installs permanently in release Firefox.
 *
 * Credentials come from the environment only — never stored in the repo:
 *   WEB_EXT_API_KEY / WEB_EXT_API_SECRET
 * Create them at https://addons.mozilla.org/developers/addon/api/key/
 */
import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';

const key = process.env.WEB_EXT_API_KEY;
const secret = process.env.WEB_EXT_API_SECRET;

if (!key || !secret) {
  console.error(`Missing AMO credentials.

  1. Create API credentials: https://addons.mozilla.org/developers/addon/api/key/
  2. Run:  WEB_EXT_API_KEY=user:... WEB_EXT_API_SECRET=... npm run sign

The signed .xpi lands in web-ext-artifacts/ and installs permanently
(no more re-adding after Firefox restarts).`);
  process.exit(1);
}

if (!existsSync('.output/firefox-mv2/manifest.json')) {
  console.error('No Firefox build found — run `npm run build:firefox` first.');
  process.exit(1);
}

const result = spawnSync(
  'npx',
  ['web-ext', 'sign', '--source-dir', '.output/firefox-mv2', '--channel', 'unlisted',
   '--api-key', key, '--api-secret', secret],
  { stdio: 'inherit' },
);
process.exit(result.status ?? 1);
