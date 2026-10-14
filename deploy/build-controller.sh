#!/usr/bin/env bash
set -euo pipefail
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
export GOCACHE="$project/.work/go-cache"
export GOMODCACHE="$project/.work/go-modcache"
export GOMAXPROCS=2
go_cmd=/nix/store/hfb2fkwkkr6jdcg2ggibvf1zablw7i57-go-1.26.7/bin/go
install -d -m 0700 "$TMPDIR" "$GOCACHE" "$GOMODCACHE" "$project/.work/bin" "$project/.work/logs"
cd "$project/controller"
"$go_cmd" test -p 2 ./...
"$go_cmd" build -p 2 -o "$project/.work/bin/chorusd" ./cmd/chorusd
"$go_cmd" build -p 2 -o "$project/.work/bin/chorusctl" ./cmd/chorusctl
