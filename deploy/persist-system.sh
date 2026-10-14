#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
candidate=$(cat "$project/.work/activation/candidate-system")
[[ $(hostname) == encore ]]
[[ $(cat "$project/.work/activation/ssh-verified") == "$candidate" ]]
verified_at=$(cat "$project/.work/activation/ssh-verified-at")
(( $(date +%s) - verified_at < 120 ))
[[ $(readlink -f /run/current-system) == "$candidate" ]]
systemctl is-active --quiet chorus-revert.timer
sudo -n systemctl stop chorus-revert.timer
sudo -n nix-env --profile /nix/var/nix/profiles/system --set "$candidate"
sudo -n "$candidate/bin/switch-to-configuration" switch >"$project/.work/activation/switch.log" 2>&1
[[ $(readlink -f /run/current-system) == "$candidate" ]]
[[ $(readlink -f /nix/var/nix/profiles/system) == "$candidate" ]]
printf '%s\n' "$candidate" >"$project/.work/activation/persisted-system"
printf 'Persistent generation=%s\n' "$candidate"
