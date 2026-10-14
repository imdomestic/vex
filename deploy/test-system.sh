#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
candidate=$(cat "$project/.work/activation/candidate-system")
old=$(readlink -f /run/current-system)
[[ $(hostname) == encore ]]
test -f "$project/.work/activation/data-migrated"
test -x "$candidate/bin/switch-to-configuration"
[[ "$candidate" != "$old" ]]
for unit in k3s mysql postgresql sshd; do
  systemctl is-active --quiet "$unit"
  cmp "$old/etc/systemd/system/$unit.service" "$candidate/etc/systemd/system/$unit.service"
  if test -d "$old/etc/systemd/system/$unit.service.d"; then
    diff -qr "$old/etc/systemd/system/$unit.service.d" "$candidate/etc/systemd/system/$unit.service.d" >/dev/null
  fi
done
for name in GTL bedrock-proxy bingo lobby proxy speedrun; do
  for type in service socket; do
    unit="minecraft-server-$name.$type"
    [[ $(systemctl is-enabled "$unit") == masked ]]
    [[ $(readlink -f "$candidate/etc/systemd/system/$unit") == /dev/null ]]
  done
done
! systemctl is-active --quiet chorus-revert.timer
! systemctl is-active --quiet chorus-revert.service
exec 9</srv/chorus-build.lock
flock --exclusive --nonblock 9
systemctl list-units 'chorus-build@*' --all --output=json >"$TMPDIR/system-build-units.json"
jq -e 'all(.[]; .sub != "running" and .active != "activating" and .active != "deactivating")' "$TMPDIR/system-build-units.json" >/dev/null
while IFS= read -r unit; do
  [[ "$unit" =~ ^chorus-build@fabric-[0-9a-f]{12}\.service$ ]]
  sudo -n systemctl stop "$unit"
done < <(jq -r '.[] | select(.active == "active" and .sub == "exited") | .unit' "$TMPDIR/system-build-units.json")
sudo -n "$candidate/bin/switch-to-configuration" dry-activate >"$project/.work/activation/final-dry-activate.log" 2>&1
printf '%s\n' "$old" >"$project/.work/activation/old-system"
sudo -n systemd-run --on-active=600 --unit=chorus-revert "$old/bin/switch-to-configuration" test
systemctl is-active --quiet chorus-revert.timer
printf '%s\n' "$candidate" >"$project/.work/activation/test-started"
sudo -n "$candidate/bin/switch-to-configuration" test >"$project/.work/activation/test.log" 2>&1
[[ $(readlink -f /run/current-system) == "$candidate" ]]
printf '%s\n' 'Candidate test activation completed; restoration timer remains active'
