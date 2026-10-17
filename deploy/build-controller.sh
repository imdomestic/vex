#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
go_cmd=${GO:-go}
export GOCACHE="$project/.work/go-cache"
export GOMODCACHE="$project/.work/go-modcache"
export GOMAXPROCS=2
install -d -m 0700 "$GOCACHE" "$GOMODCACHE" "$project/.work/bin"
GOFMT=${GOFMT:-$(dirname "$(command -v "$go_cmd")")/gofmt} bash "$project/deploy/check-go-format.sh"
cd "$project/controller"
"$go_cmd" vet -p 2 ./...
"$go_cmd" test -p 2 ./...
"$go_cmd" build -p 2 -o "$project/.work/bin/vexd" ./cmd/vexd
"$go_cmd" build -p 2 -o "$project/.work/bin/vex" ./cmd/vex
