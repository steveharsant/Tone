import { defineConfig } from 'wxt';

export default defineConfig({
  manifest: {
    name: 'Tone — local writing assistant',
    description:
      'Grammar, clarity and style suggestions powered by a local LLM. Nothing leaves your machine.',
    permissions: ['storage'],
    // The background worker is the only place that talks to the engine.
    // Match patterns ignore ports, so this covers any configured engine port.
    host_permissions: ['http://127.0.0.1/*', 'http://localhost/*'],
    action: { default_title: 'Tone' },
    browser_specific_settings: {
      gecko: { id: 'tone@harsant.dev', strict_min_version: '140.0' },
    },
  },
});
