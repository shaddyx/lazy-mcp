#!/usr/bin/env bash
# Build and run the lazy-mcp proxy server over stdio.
# Skips `go build` when the source hash matches .build-hash.
set -euo pipefail

_CWD="$(pwd)"

cd "$(dirname "$0")"

readonly BIN="lazy-mcp"
readonly HASH_FILE=".build-hash"

NEW_HASH="$(find . -type f \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' -o -path './profiles/*' \) ! -name '*_test.go' | sort | xargs sha256sum | sha256sum | awk '{print $1}')"

if [[ -f "$BIN" && -f "$HASH_FILE" && "$(cat "$HASH_FILE")" == "$NEW_HASH" ]]; then
    : # cache hit — skip go build
else
    go build -o "$BIN" .
    printf '%s\n' "$NEW_HASH" > "$HASH_FILE"
fi

_FULL_PATH="$(pwd)/$BIN"

cd "${_CWD}"

exec "${_FULL_PATH}" "$@"