#!/usr/bin/env bash
set -euo pipefail
set +x
umask 077

project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
secret_dir="$project/.work/p1-data/secrets"
install -d -m 0700 "$TMPDIR" "$secret_dir"
[[ $(hostname) == encore ]]
sudo -n true

if ! test -e /srv/chorus-build.lock; then
  sudo install -m 0444 -o root -g root /dev/null /srv/chorus-build.lock
fi
[[ $(stat -c '%a:%U:%G:%s' /srv/chorus-build.lock) == 444:root:root:0 ]]
exec 9</srv/chorus-build.lock
flock --exclusive --nonblock 9
if sudo -n -u nobody /run/current-system/sw/bin/bash -c 'exec 8</srv/chorus-build.lock; flock --exclusive --nonblock --conflict-exit-code 73 8'; then
  exit 1
else
  [[ $? == 73 ]]
fi
flock --unlock 9
sudo -n -u nobody /run/current-system/sw/bin/bash -c 'exec 8</srv/chorus-build.lock; flock --exclusive --nonblock 8'
printf '%s\n' 'cross-user read-only flock verified'

test ! -e "$secret_dir/users.toml"
test ! -e "$secret_dir/signing.key"
linwhite_token=$(openssl rand -hex 32)
hank_token=$(openssl rand -hex 32)
printf '[users]\nlinwhite = "%s"\nhank = "%s"\n' "$linwhite_token" "$hank_token" >"$secret_dir/users.toml"
unset linwhite_token hank_token
openssl rand 32 >"$secret_dir/signing.key"
chmod 0600 "$secret_dir/users.toml" "$secret_dir/signing.key"
[[ $(stat -c %s "$secret_dir/signing.key") == 32 ]]
printf '%s\n' 'P1 secret files created with mode 0600'
