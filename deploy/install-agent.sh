#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
require_linux
[[ $# == 3 ]]
source=$1
target=$2
digest=$3
[[ "$source" == /*.jar && "$target" == /*.jar && "$digest" =~ ^[0-9a-f]{64}$ ]]
test -f "$source"
printf '%s  %s\n' "$digest" "$source" | sha256sum --check --status
exec 9<"$build_lock"
flock --exclusive --nonblock 9
require_idle_workers
sudo -n install -d -m 0750 -o vexd -g vexd "$(dirname "$target")"
sudo -n install -m 0640 -o vexd -g vexd "$source" "$target.new"
sudo -n mv -- "$target.new" "$target"
printf '%s  %s\n' "$digest" "$target" | sudo -n sha256sum --check --status
printf 'agent installed and SHA256 verified: %s\n' "$target"
