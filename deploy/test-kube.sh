#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
go_cmd=${GO:-go}
export GOCACHE="$project/.work/go-cache"
export GOMODCACHE="$project/.work/go-modcache"
export GOMAXPROCS=2
export VEX_IT_RUN_ID=${VEX_IT_RUN_ID:?provide an isolated integration run ID}
[[ "$VEX_IT_RUN_ID" =~ ^[a-z0-9]{1,8}$ ]]
export VEX_IT_KUBECONFIG=${VEX_IT_KUBECONFIG:?provide a restricted kubeconfig file path}
test -f "$VEX_IT_KUBECONFIG"
install -d -m 0700 "$GOCACHE" "$GOMODCACHE"
GOFMT=${GOFMT:-$(dirname "$(command -v "$go_cmd")")/gofmt} bash "$project/deploy/check-go-format.sh"
cd "$project/controller"
"$go_cmd" test -p 2 -tags integration -count=1 -v ./internal/runtime/kubernetes \
  -run '^(TestActualKubernetesRoomObjects|TestActualEnsureRoomStableResourceVersions|TestActualFailureContainerLogs)$'
