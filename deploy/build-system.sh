#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
install -d -m 0700 "$TMPDIR" "$project/.work/activation"
[[ $(hostname) == encore ]]
[[ $# == 1 ]]
nix_config=$1
[[ "$nix_config" == /* && "$nix_config" =~ ^[A-Za-z0-9_./-]+$ ]]
test -f "$nix_config/flake.nix"
available=$(df -B1 --output=avail / | tail -n 1)
(( available >= 15 * 1024 * 1024 * 1024 ))
nix build --no-link --print-out-paths --max-jobs 1 --cores 2 \
  --option build-dir /nix/var/nix/builds \
  "path:$nix_config#nixosConfigurations.encore.config.system.build.toplevel" \
  >"$project/.work/activation/candidate-system"
candidate=$(cat "$project/.work/activation/candidate-system")
test -x "$candidate/bin/switch-to-configuration"
nix-store --add-root "$project/.work/activation/candidate-root" --indirect --realise "$candidate" >/dev/null
sudo -n "$candidate/bin/switch-to-configuration" dry-activate >"$project/.work/activation/dry-activate.log" 2>&1
printf 'candidate=%s\n' "$candidate"
cat "$project/.work/activation/dry-activate.log"
