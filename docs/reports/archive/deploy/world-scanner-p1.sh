#!/usr/bin/env bash
set -euo pipefail
project=$(cd "$(dirname "$0")/.." && pwd)
export PYTHONDONTWRITEBYTECODE=1
export TMPDIR="$project/.work/p1-data/.work"
exec /nix/store/x8qgr4h05psniynzi7ifkbsahbqm4ir1-python3-3.13.15-env/bin/python "$project/tools/world-scanner/scan.py" "$@"
