#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
[[ $# == 2 ]]
image=$1
endpoint=$2
[[ "$image" == *@sha256:* && "$endpoint" == http*://*/v2/health ]]
namespace="vex-it-network-$(date -u +%Y%m%d%H%M%S)"
kubectl create namespace "$namespace"
cleanup() {
  [[ "$namespace" == vex-it-network-* ]]
  kubectl delete namespace "$namespace" --wait=true --timeout=60s
}
trap cleanup EXIT
kubectl -n "$namespace" run controller-network --restart=Never --image="$image" \
  --env=TMPDIR=/data --command -- curl --fail --silent --show-error \
  --connect-timeout 5 --max-time 10 "$endpoint"
kubectl -n "$namespace" wait --for=jsonpath='{.status.phase}'=Succeeded pod/controller-network --timeout=60s
kubectl -n "$namespace" logs controller-network >"$TMPDIR/pod-network.json"
jq -e '.status == "ok"' "$TMPDIR/pod-network.json" >/dev/null
printf '%s\n' 'Real Pod reached the Vex health endpoint'
