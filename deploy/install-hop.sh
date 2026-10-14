#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
[[ $(hostname) == encore ]]
source="$project/hop-mod/build/libs/chorus-hop-26.3-1.0.0.jar"
target=/srv/chorus/mods/chorus-hop/26.3/chorus-hop-26.3-1.0.0.jar
digest=b00c3cfd027ccf66dd90fa4423bb8ccb7c689607fb3d4a480cc23b1a6a7f80dc
printf '%s  %s\n' "$digest" "$source" | sha256sum --check --status
exec 9</srv/chorus-build.lock
flock --exclusive --nonblock 9
sudo -n install -m 0600 -o chorusd -g chorusd "$source" "$target.new"
sudo -n mv "$target.new" "$target"
printf '%s  %s\n' "$digest" "$target" | sudo -n sha256sum --check --status
printf '%s\n' 'chorus-hop artifact installed and SHA-256 verified'
