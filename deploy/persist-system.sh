#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
require_linux
candidate=$(cat "$activation/candidate-system")
[[ $(cat "$activation/ssh-verified") == "$candidate" ]]
verified_at=$(cat "$activation/ssh-verified-at")
[[ "$verified_at" =~ ^[0-9]+$ ]]
elapsed=$(( $(date +%s) - verified_at ))
(( elapsed >= 0 && elapsed < 120 ))
[[ $(readlink -f /run/current-system) == "$candidate" ]]
systemctl is-active --quiet vex-system-restore.timer
exec 9<"$build_lock"
flock --exclusive --nonblock 9
require_idle_workers
if systemctl is-active --quiet vex-system-restore.service; then
  exit 1
fi
sudo -n systemctl stop vex-system-restore.timer
[[ $(systemctl is-active vex-system-restore.timer) == inactive ]]
sudo -n nix-env --profile /nix/var/nix/profiles/system --set "$candidate"
{ sudo -n "$candidate/bin/switch-to-configuration" switch; } >"$activation/switch.log" 2>&1
[[ $(readlink -f /run/current-system) == "$candidate" ]]
[[ $(readlink -f /nix/var/nix/profiles/system) == "$candidate" ]]
systemctl is-active --quiet vexd
check_api
printf '%s\n' "$candidate" >"$activation/persisted-system"
printf 'Persistent generation=%s\n' "$candidate"
