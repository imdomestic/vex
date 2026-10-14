#!/usr/bin/env bash
set -euo pipefail
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
namespace="chorus-it-network-$(date -u +%Y%m%d%H%M%S)"
sudo -n k3s kubectl create namespace "$namespace"
cleanup() {
  [[ "$namespace" == chorus-it-* ]]
  sudo -n k3s kubectl delete namespace "$namespace" --wait=true --timeout=90s
}
trap cleanup EXIT
sudo -n k3s kubectl -n "$namespace" run controller-network \
  --restart=Never \
  --image=itzg/minecraft-server:java25@sha256:7462fe34a56686719463ff898a674c1a1ce802988138d2bfdeff2507fb1644e8 \
  --command -- curl --silent --show-error --connect-timeout 5 --max-time 10 \
  --output /dev/null --write-out '%{http_code}\n' http://10.1.2.137:8790/v1/health
sudo -n k3s kubectl -n "$namespace" wait --for=jsonpath='{.status.phase}'=Succeeded pod/controller-network --timeout=60s
response=$(sudo -n k3s kubectl -n "$namespace" logs controller-network)
[[ "$response" == 401 ]]
printf '%s\n' 'real Pod reached chorusd: HTTP 401 without credentials'
