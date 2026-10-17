#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
require_linux
[[ $# == 2 ]]
mode=$1
candidate=$2
[[ "$mode" == prepare || "$mode" == deploy ]]
[[ "$candidate" == /nix/store/*-vex-applications ]]
[[ $(cat "$project/.work/application/candidate") == "$candidate" ]]
for program in vexd vex vex-world-scanner; do
  test -x "$candidate/bin/$program"
done
sudo -n -u vexd "$candidate/bin/vexd" config check --config "$configuration"
exec 9<"$build_lock"
flock --exclusive --nonblock 9
require_idle_workers
if [[ "$mode" == prepare ]] && systemctl is-active --quiet vexd; then
  printf '%s\n' 'Use deploy mode to update an active Vex service' >&2
  exit 1
fi
if [[ "$mode" == deploy ]]; then
  systemctl show vexd --property=ExecStart --value | rg -F "$application_profile/bin/vexd" >/dev/null
  sudo -n systemctl stop vexd
  [[ $(systemctl is-active vexd) == inactive ]]
  require_idle_workers
fi
sudo -n nix-env --profile "$application_profile" --set "$candidate"
[[ $(readlink -f "$application_profile") == "$candidate" ]]
if [[ "$mode" == deploy ]]; then
  sudo -n systemctl start vexd
  systemctl is-active --quiet vexd
  check_api
fi
printf 'application profile installed: %s\n' "$candidate"
