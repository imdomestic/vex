#!/usr/bin/env bash
set -euo pipefail
project=$(cd "$(dirname "$0")/.." && pwd)
gofmt_cmd=${GOFMT:-gofmt}
unformatted=$("$gofmt_cmd" -l "$project/controller")
if [[ -n "$unformatted" ]]; then
  printf '%s\n' "$unformatted" >&2
  exit 1
fi
