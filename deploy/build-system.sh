#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
require_linux
[[ $# == 1 ]]
nix_config=$1
[[ "$nix_config" == /* ]]
test -f "$nix_config/flake.nix"
require_space
nix build --no-link --print-out-paths --max-jobs 1 --cores 2 \
  --option build-dir "${NIX_BUILD_DIRECTORY:-/nix/var/nix/builds}" \
  "path:$nix_config#nixosConfigurations.$deploy_host.config.system.build.toplevel" \
  >"$activation/candidate-system"
candidate=$(cat "$activation/candidate-system")
[[ "$candidate" == /nix/store/*-nixos-system-"$deploy_host"-* ]]
test -x "$candidate/bin/switch-to-configuration"
nix-store --add-root "$activation/candidate-root" --indirect --realise "$candidate" >/dev/null
{ sudo -n "$candidate/bin/switch-to-configuration" dry-activate; } >"$activation/dry-activate.log" 2>&1
nix store diff-closures /run/current-system "$candidate" >"$activation/closure-diff.log"
printf 'candidate=%s\n' "$candidate"
cat "$activation/dry-activate.log" "$activation/closure-diff.log"
