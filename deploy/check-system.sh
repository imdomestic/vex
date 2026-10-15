#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
candidate=$(cat "$project/.work/activation/candidate-system")
[[ $(hostname) == encore ]]
[[ -n ${SSH_CONNECTION:-} ]]
[[ $(readlink -f /run/current-system) == "$candidate" ]]
systemctl is-active --quiet chorus-revert.timer
for unit in k3s mysql postgresql chorusd chorus-dns; do
  systemctl is-active --quiet "$unit"
  printf '%s active\n' "$unit"
done
for name in GTL bedrock-proxy bingo lobby proxy speedrun; do
  for type in service socket; do
    unit="minecraft-server-$name.$type"
    [[ $(systemctl is-enabled "$unit") == masked ]]
    [[ $(systemctl is-active "$unit") == inactive ]]
  done
done
sudo -n k3s kubectl wait --for=condition=Ready node/encore --timeout=30s
pid=$(systemctl show -p MainPID --value chorusd)
binary=$(sudo readlink -f "/proc/$pid/exe")
sudo -n -u chorusd "$(dirname "$binary")/chorusctl" --url http://10.1.2.137:8790 --users-file /var/lib/chorusd/secrets/users.toml health
[[ $(dig @100.64.0.43 main-fabric.chorus.internal A +short +time=2 +tries=1) == 100.64.0.43 ]]
[[ $(dig @10.1.2.137 plaza.chorus.internal A +short +tcp +time=2 +tries=1) == 100.64.0.43 ]]
printf '%s\n' "$candidate" >"$project/.work/activation/ssh-verified"
date +%s >"$project/.work/activation/ssh-verified-at"
printf '%s\n' 'Fresh SSH checks passed; all twelve legacy units remain masked and inactive'
