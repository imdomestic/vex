#!/usr/bin/env bash
set -euo pipefail
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/p1-data/.work"
export SQLITE_TMPDIR="$TMPDIR"
install -d -m 0700 "$TMPDIR" "$project/.work/logs"
cd "$project"
exec "$project/.work/bin/chorusd" serve --config "$project/.work/p1-config.toml" >>"$project/.work/logs/chorusd-p1.log" 2>&1
