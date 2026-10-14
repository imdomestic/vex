#!/usr/bin/env bash
set -euo pipefail
set +x
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
export PYTHONDONTWRITEBYTECODE=1
source_data="$project/.work/p1-data"
python=/nix/store/x8qgr4h05psniynzi7ifkbsahbqm4ir1-python3-3.13.15-env/bin/python
[[ $(hostname) == encore ]]
[[ $# == 1 && $1 == stop-and-copy ]]
sudo -n true
test ! -e /srv/chorus
test ! -e /var/lib/chorusd
hop="$project/hop-mod/build/libs/chorus-hop-26.3-1.0.0.jar"
printf '%s  %s\n' b00c3cfd027ccf66dd90fa4423bb8ccb7c689607fb3d4a480cc23b1a6a7f80dc "$hop" | sha256sum --check --status
test -x "$(cat "$project/.work/activation/candidate-system")/bin/switch-to-configuration"
systemctl --user list-units 'chorus-build*' --all --output=json >"$TMPDIR/user-build-units.json"
jq -e 'all(.[]; .sub != "running" and .active != "activating" and .active != "deactivating")' "$TMPDIR/user-build-units.json" >/dev/null
exec 9</srv/chorus-build.lock
flock --exclusive --nonblock 9
"$python" "$project/deploy/verify-state.py" "$source_data/state/chorusd.db"
[[ -z $(find "$source_data/worktrees" -mindepth 1 -maxdepth 1 -print -quit) ]]
available=$(df -B1 --output=avail / | tail -n 1)
data_size=$(du -sb "$source_data" | cut -f 1)
(( available - data_size >= 15 * 1024 * 1024 * 1024 ))
pid=$(pgrep -f "^$project/.work/bin/chorusd serve --config $project/.work/p1-config.toml$")
[[ "$pid" =~ ^[0-9]+$ ]]
kill -TERM "$pid"
for attempt in {1..60}; do
  if ! kill -0 "$pid" 2>/dev/null; then break; fi
  sleep 1
done
! kill -0 "$pid" 2>/dev/null
"$python" "$project/deploy/verify-state.py" "$source_data/state/chorusd.db" >"$project/.work/activation/source-state.json"
sudo install -d -m 0750 -o 980 -g 980 /srv/chorus /var/lib/chorusd
sudo install -d -m 0750 -o root -g 980 /var/lib/chorusd/secrets
sudo rsync -a --chown=980:980 --exclude=/secrets --exclude=/kubeconfig "$source_data/" /srv/chorus/
sudo rsync -acni --chown=980:980 --exclude=/secrets --exclude=/kubeconfig "$source_data/" /srv/chorus/ >"$project/.work/activation/data-copy-diff"
test ! -s "$project/.work/activation/data-copy-diff"
sudo install -d -m 0700 -o 980 -g 980 /srv/chorus/mods/chorus-hop/26.3
sudo install -m 0600 -o 980 -g 980 "$hop" /srv/chorus/mods/chorus-hop/26.3/chorus-hop-26.3-1.0.0.jar
sudo install -m 0640 -o root -g 980 "$source_data/secrets/users.toml" /var/lib/chorusd/secrets/users.toml
sudo install -m 0640 -o root -g 980 "$source_data/secrets/signing.key" /var/lib/chorusd/secrets/signing.key
sudo install -m 0600 -o 980 -g 980 "$source_data/kubeconfig" /var/lib/chorusd/kubeconfig
sudo install -m 0640 -o root -g 980 "$project/config/chorusd.example.toml" /var/lib/chorusd/config.toml
sudo cmp "$source_data/secrets/users.toml" /var/lib/chorusd/secrets/users.toml
sudo cmp "$source_data/secrets/signing.key" /var/lib/chorusd/secrets/signing.key
sudo "$python" "$project/deploy/verify-state.py" /srv/chorus/state/chorusd.db >"$project/.work/activation/target-state.json"
cmp "$project/.work/activation/source-state.json" "$project/.work/activation/target-state.json"
date -u +%FT%TZ >"$project/.work/activation/data-migrated"
printf '%s\n' 'P1 stopped; production data and unchanged secrets copied and verified; P1 source retained'
