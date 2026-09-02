#!/usr/bin/env bash
#
# Regenerates internal/modelsdev/catalog.json.gz, the models.dev snapshot
# compiled into the binary as the offline/first-run fallback.
#
# This is the Go analogue of packages/opencode/script/generate.ts, which
# fetches the same document at build time and bakes it into the JS bundle as
# the OPENCODE_MODELS_DEV define. The difference is that the snapshot is
# checked in here rather than fetched during every build, so a build never
# depends on models.dev being reachable — run this script to refresh it.
#
# Usage:
#   script/generate-catalog.sh                    # fetch from models.dev
#   OPENCODE_MODELS_URL=http://localhost:3000 \
#     script/generate-catalog.sh                  # fetch from a mirror
#   MODELS_DEV_API_JSON=/path/to/api.json \
#     script/generate-catalog.sh                  # use a local file
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/internal/modelsdev/catalog.json.gz"
source_url="${OPENCODE_MODELS_URL:-https://models.dev}/api.json"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

if [[ -n "${MODELS_DEV_API_JSON:-}" ]]; then
  echo "reading $MODELS_DEV_API_JSON"
  cp "$MODELS_DEV_API_JSON" "$tmp"
else
  echo "fetching $source_url"
  curl -fsSL --max-time 120 -o "$tmp" "$source_url"
fi

# Refuse to write a snapshot that would not load: the whole point of this file
# is that it is the fallback when nothing else is available.
if ! python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if isinstance(d,dict) and "anthropic" in d else 1)' "$tmp"; then
  echo "error: fetched document is not a models.dev catalog" >&2
  exit 1
fi

gzip -9 -c "$tmp" > "$out"
echo "wrote $out ($(du -h "$out" | cut -f1), $(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$tmp") providers)"
