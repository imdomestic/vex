#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
export GOCACHE="$project/.work/go-cache"
export GOMODCACHE="$project/.work/go-modcache"
export GOMAXPROCS=2
install -d -m 0700 "$TMPDIR"
vendor_source=$(mktemp -d "$project/.work/vendor-source.XXXXXXXX")
cp -a "$project/controller/." "$vendor_source/"
cd "$vendor_source"
/nix/store/hfb2fkwkkr6jdcg2ggibvf1zablw7i57-go-1.26.7/bin/go mod vendor
nix hash path vendor
