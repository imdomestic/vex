#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
[[ $# == 2 ]]
host=$1
destination=$2
[[ "$host" =~ ^[A-Za-z0-9][A-Za-z0-9.@-]*$ ]]
[[ "$destination" =~ ^/[A-Za-z0-9_./-]+$ && "$destination" != / ]]
[[ -z $(git -C "$project" status --porcelain) ]]
ssh_options=(-o BatchMode=yes -o ControlMaster=no -o ControlPath=none -o ConnectTimeout=10)
# destination 已限制为绝对路径和安全字符。
# shellcheck disable=SC2029
ssh "${ssh_options[@]}" "$host" "mkdir -- '$destination'"
# shellcheck disable=SC2029
git -C "$project" archive --format=tar HEAD | ssh "${ssh_options[@]}" "$host" "tar -xf - -C '$destination'"
printf 'source=%s destination=%s:%s\n' "$(git -C "$project" rev-parse HEAD)" "$host" "$destination"
