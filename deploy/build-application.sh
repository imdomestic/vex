#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
require_linux
require_space
cd "$project"
nix build --no-link --print-out-paths --max-jobs 1 --cores 2 \
  --option build-dir "${NIX_BUILD_DIRECTORY:-/nix/var/nix/builds}" .#vex \
  >"$project/.work/application/candidate"
candidate=$(cat "$project/.work/application/candidate")
[[ "$candidate" == /nix/store/*-vex-applications ]]
for program in vexd vex vex-world-scanner; do
  test -x "$candidate/bin/$program"
done
nix-store --add-root "$project/.work/application/candidate-root" --indirect --realise "$candidate" >/dev/null
printf 'application=%s\n' "$candidate"
