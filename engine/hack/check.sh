#!/usr/bin/env bash
# Manual smoke check against a running engine:
#   hack/check.sh "I will definately be there tomorow."
# Reads the pairing token from the local config; jq optional.
set -euo pipefail

TEXT="${1:?usage: check.sh \"text to check\"}"
CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/tone/config.json"
PORT=$(grep -oP '"port":\s*\K[0-9]+' "$CONFIG")
TOKEN=$(grep -oP '"pairing_token":\s*"\K[0-9a-f]+' "$CONFIG")

BODY=$(printf '%s' "$TEXT" | python3 -c 'import json,sys; print(json.dumps({"text": sys.stdin.read()}))')

curl -sS "http://127.0.0.1:${PORT}/v1/check" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H 'Content-Type: application/json' \
  -d "$BODY" | (command -v jq >/dev/null && jq . || cat)
