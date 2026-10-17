#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
require_linux
candidate=$(cat "$activation/candidate-system")
old=$(readlink -f /run/current-system)
[[ "$candidate" == /nix/store/*-nixos-system-"$deploy_host"-* && "$candidate" != "$old" ]]
test -x "$candidate/bin/switch-to-configuration"
read -r -a protected_services < <(printf '%s\n' "${DEPLOY_PROTECTED_SERVICES:-sshd tailscaled k3s mysql postgresql}")
for unit in "${protected_services[@]}"; do
  [[ "$unit" =~ ^[A-Za-z0-9@_.-]+$ ]]
  systemctl is-active --quiet "$unit"
  cmp "$old/etc/systemd/system/$unit.service" "$candidate/etc/systemd/system/$unit.service"
  if test -d "$old/etc/systemd/system/$unit.service.d"; then
    diff -qr "$old/etc/systemd/system/$unit.service.d" "$candidate/etc/systemd/system/$unit.service.d" >/dev/null
  else
    test ! -d "$candidate/etc/systemd/system/$unit.service.d"
  fi
done
systemctl list-unit-files --state=masked --output=json >"$activation/masked-units.json"
jq -r '.[] | .unit_file | select(test("\\.(service|socket)$"))' "$activation/masked-units.json" >"$activation/masked-units.txt"
while IFS= read -r unit; do
  [[ "$unit" =~ ^[A-Za-z0-9@_.-]+\.(service|socket)$ ]]
  [[ $(readlink -f "$candidate/etc/systemd/system/$unit") == /dev/null ]]
done <"$activation/masked-units.txt"
if systemctl is-active --quiet vex-system-restore.timer || systemctl is-active --quiet vex-system-restore.service; then
  printf '%s\n' 'A restoration operation is already active' >&2
  exit 1
fi
exec 9<"$build_lock"
flock --exclusive --nonblock 9
require_idle_workers
sudo -n -u vexd "$application_profile/bin/vexd" config check --config "$configuration"
{ sudo -n "$candidate/bin/switch-to-configuration" dry-activate; } >"$activation/final-dry-activate.log" 2>&1
printf '%s\n' "$old" >"$activation/old-system"
sudo -n systemd-run --collect --on-active=600 --unit=vex-system-restore \
  /run/current-system/sw/bin/bash "$project/deploy/restore-system.sh" "$old"
systemctl is-active --quiet vex-system-restore.timer
sudo -n systemctl stop vexd
require_idle_workers
printf '%s\n' "$candidate" >"$activation/test-started"
{ sudo -n "$candidate/bin/switch-to-configuration" test; } >"$activation/test.log" 2>&1
sudo -n systemctl start vexd
[[ $(readlink -f /run/current-system) == "$candidate" ]]
printf '%s\n' 'Candidate test activation completed; restoration timer remains active'
