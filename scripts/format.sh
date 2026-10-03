#!/usr/bin/env bash
# Runs under bash 3.2 (macOS).
set -euo pipefail

cd "$(dirname "$0")/.."

command -v go >/dev/null || { echo "format.sh: go required" >&2; exit 1; }

unformatted=$(gofmt -l .)
if [[ -n "$unformatted" ]]; then
  echo "format.sh: gofmt differs; run 'gofmt -w .':" >&2
  echo "$unformatted" >&2
  exit 1
fi

for tags in '' e2e; do
  go vet -tags "$tags" ./...
done
