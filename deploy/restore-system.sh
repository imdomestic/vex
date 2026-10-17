#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
[[ $EUID == 0 && $# == 1 && "$1" == /nix/store/*-nixos-system-* ]]
test -x "$1/bin/switch-to-configuration"
nix-env --profile /nix/var/nix/profiles/system --set "$1"
"$1/bin/switch-to-configuration" switch
if systemctl cat vexd.service >/dev/null 2>&1; then
  systemctl start vexd
fi
