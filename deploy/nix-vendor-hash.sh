#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
export GOCACHE="$project/.work/go-cache"
export GOMODCACHE="$project/.work/go-modcache"
export GOMAXPROCS=2
install -d -m 0700 "$GOCACHE" "$GOMODCACHE"
vendor_source=$(mktemp -d "$project/.work/vendor-source.XXXXXXXX")
cd "$project/controller"
"${GO:-go}" mod vendor -o "$vendor_source/vendor"
nix hash path "$vendor_source/vendor"
