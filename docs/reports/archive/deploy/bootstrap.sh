#!/usr/bin/env bash
set -euo pipefail
umask 077

project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
install -d -m 0700 "$TMPDIR" "$project/.work/p1-data"
[[ $(hostname) == encore ]]
sudo -n true

sudo k3s kubectl apply -k "$project/deploy"
sudo k3s kubectl -n chorus-system rollout status deployment/mc-router --timeout=180s

credential="$TMPDIR/chorusd-api.json"
sudo k3s kubectl -n chorus-rooms get secret chorusd-api -o json >"$credential"
jq -e '.data.token != null and .data["ca.crt"] != null' "$credential" >/dev/null
jq -n --slurpfile credential "$credential" '
  {
    apiVersion: "v1", kind: "Config",
    clusters: [{name: "encore", cluster: {
      server: "https://10.1.2.137:6443",
      "certificate-authority-data": $credential[0].data["ca.crt"]
    }}],
    users: [{name: "chorusd", user: {token: ($credential[0].data.token | @base64d)}}],
    contexts: [{name: "chorusd", context: {
      cluster: "encore", user: "chorusd", namespace: "chorus-rooms"
    }}],
    "current-context": "chorusd"
  }' >"$project/.work/p1-data/kubeconfig"
chmod 0600 "$project/.work/p1-data/kubeconfig"

kubeconfig="$project/.work/p1-data/kubeconfig"
for resource in statefulsets.apps services pods persistentvolumeclaims secrets jobs.batch; do
  k3s kubectl --kubeconfig "$kubeconfig" auth can-i create "$resource" -n chorus-rooms | rg '^yes$'
done
k3s kubectl --kubeconfig "$kubeconfig" auth can-i get nodes | rg '^yes$'
if k3s kubectl --kubeconfig "$kubeconfig" auth can-i create pods -n default; then
  exit 1
fi
if k3s kubectl --kubeconfig "$kubeconfig" auth can-i delete nodes; then
  exit 1
fi
printf '%s\n' 'chorus deployment and restricted kubeconfig verified'
