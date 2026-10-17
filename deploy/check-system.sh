#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
require_linux
candidate=$(cat "$activation/candidate-system")
[[ -n ${SSH_CONNECTION:-} ]]
[[ $(readlink -f /run/current-system) == "$candidate" ]]
systemctl is-active --quiet vex-system-restore.timer
if systemctl is-active --quiet vex-system-restore.service; then
  exit 1
fi
read -r -a required_services < <(printf '%s\n' "${DEPLOY_REQUIRED_SERVICES:-sshd tailscaled k3s mysql postgresql vexd vex-dns}")
for unit in "${required_services[@]}"; do
  [[ "$unit" =~ ^[A-Za-z0-9@_.-]+$ ]]
  systemctl is-active --quiet "$unit"
  printf '%s active\n' "$unit"
done
while IFS= read -r unit; do
  [[ $(systemctl is-enabled "$unit") == masked ]]
  [[ $(systemctl is-active "$unit") == inactive ]]
done <"$activation/masked-units.txt"
sudo -n k3s kubectl wait --for=condition=Ready "node/$deploy_host" --timeout=30s
sudo -n -u vexd "$application_profile/bin/vexd" config check --config "$configuration"
check_api
if [[ -n ${DEPLOY_DNS_SERVER:-} || -n ${DEPLOY_DNS_NAME:-} || -n ${DEPLOY_DNS_EXPECTED:-} ]]; then
  bash "$project/deploy/test-dns.sh" "$DEPLOY_DNS_SERVER" "$DEPLOY_DNS_NAME" "$DEPLOY_DNS_EXPECTED"
fi
printf '%s\n' "$candidate" >"$activation/ssh-verified"
date +%s >"$activation/ssh-verified-at"
printf '%s\n' 'Fresh SSH checks passed; protected services and masked units verified'
