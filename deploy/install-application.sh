#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ $(hostname) == encore ]]
[[ $# == 2 ]]
mode=$1
candidate=$2
[[ "$mode" == prepare || "$mode" == deploy ]]
[[ "$candidate" == /nix/store/*-chorus-applications ]]
project=$(cd "$(dirname "$0")/.." && pwd)
[[ $(cat "$project/.work/application/candidate") == "$candidate" ]]
for program in chorusd chorusctl chorus-world-scanner; do
  test -x "$candidate/bin/$program"
done
sudo -n true
exec 9</srv/chorus-build.lock
flock --exclusive --nonblock 9
if [[ "$mode" == deploy ]]; then
  systemctl show chorusd --property=ExecStart --value | rg -F '/nix/var/nix/profiles/chorus/bin/chorusd' >/dev/null
fi
sudo -n nix-env --profile /nix/var/nix/profiles/chorus --set "$candidate"
[[ $(readlink -f /nix/var/nix/profiles/chorus) == "$candidate" ]]
if [[ "$mode" == deploy ]]; then
  sudo -n systemctl restart chorusd
  systemctl is-active --quiet chorusd
  [[ $(curl --silent --show-error --retry 5 --retry-connrefused --retry-delay 1 --connect-timeout 3 --max-time 10 --output /dev/null --write-out '%{http_code}' http://10.1.2.137:8790/v1/health) == 401 ]]
  sudo -n -u chorusd "$candidate/bin/chorusctl" --url http://10.1.2.137:8790 --users-file /var/lib/chorusd/secrets/users.toml health
fi
printf 'application profile installed: %s\n' "$candidate"
