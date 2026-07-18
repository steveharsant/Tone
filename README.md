<p align="center">
  <img src="tone_logo.png" width="160" alt="Tone logo">
</p>

# Tone

Local AI spellcheck and grammar, à la Grammarly — nothing leaves your machine.

Tone is two pieces:

- **Engine** (`engine/`) — a Go daemon that manages a local LLM (rootless
  Ollama, installed for you on first run) and serves suggestions over a
  localhost-only HTTP API.
- **Extension** (`extension/`) — a Chrome/Firefox WebExtension that underlines
  suggestions in text fields on any site and lets you accept or dismiss them.

## Quick start

```sh
# 1. Build and run the engine
cd engine && go build -o tone ./cmd/tone && ./tone
# → open the printed setup URL, follow the wizard (installs Ollama rootlessly
#   into ~/.local/share/tone and downloads a model of your choice)

# 2. Build the extension
cd extension && npm install && npm run build          # Chrome  → .output/chrome-mv3
                              npm run build:firefox   # Firefox → .output/firefox-mv2
# → load it unpacked (chrome://extensions / about:debugging), then paste the
#   pairing token from the engine's settings page into the extension options.
```

## How it works

Text from a field is segmented into sentences and checked against the model;
only sentences whose hash changed since the last check hit the model, so
typing stays cheap. The model returns (snippet, replacement) pairs — never
offsets — and the engine anchors each snippet in the source text itself,
dropping anything it can't locate exactly. Suggestions come back as UTF-16
span offsets ready for DOM use, categorized as correctness / clarity /
engagement / delivery.

Security posture: the engine binds `127.0.0.1` only, validates the `Host`
header, and every request needs the pairing token generated on first run.
Cloud API keys (Phase 2) go in the OS keychain, never in config files.

## Development

```sh
cd engine && go test ./...     # unit + mock-backend integration tests
cd extension && npx tsc --noEmit
engine/hack/check.sh "Some text with a mistkae."   # hit a running engine
```
