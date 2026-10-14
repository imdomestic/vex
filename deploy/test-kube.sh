#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
export GOCACHE="$project/.work/go-cache"
export GOMODCACHE="$project/.work/go-modcache"
export GOMAXPROCS=2
export CHORUS_IT_KUBECONFIG="$TMPDIR/kube-integration.json"
install -d -m 0700 "$TMPDIR"
sudo -n k3s kubectl config view --raw -o json >"$CHORUS_IT_KUBECONFIG"
chmod 0600 "$CHORUS_IT_KUBECONFIG"
cd "$project/controller"
/nix/store/hfb2fkwkkr6jdcg2ggibvf1zablw7i57-go-1.26.7/bin/go test -p 2 -tags integration -count=1 -v ./internal/kube
