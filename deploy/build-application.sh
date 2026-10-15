#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
install -d -m 0700 "$TMPDIR" "$project/.work/application"
[[ $(hostname) == encore ]]
available=$(df -B1 --output=avail / | tail -n 1)
(( available >= 15 * 1024 * 1024 * 1024 ))
cd "$project"
nix build --no-link --print-out-paths --max-jobs 1 --cores 2 \
  --option build-dir /nix/var/nix/builds .#chorus \
  >"$project/.work/application/candidate"
candidate=$(cat "$project/.work/application/candidate")
for program in chorusd chorusctl chorus-world-scanner; do
  test -x "$candidate/bin/$program"
done
nix-store --add-root "$project/.work/application/candidate-root" --indirect --realise "$candidate" >/dev/null
printf 'application=%s\n' "$candidate"
