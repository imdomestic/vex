#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
[[ $(uname -s) == Darwin && $# == 4 ]]
host=$1
remote=$2
nix_config=$3
expected_candidate=$4
[[ "$host" =~ ^[A-Za-z0-9][A-Za-z0-9.@-]*$ ]]
[[ "$remote" =~ ^/[A-Za-z0-9_./-]+$ && "$nix_config" =~ ^/[A-Za-z0-9_./-]+$ ]]
[[ "$expected_candidate" == /nix/store/*-nixos-system-* ]]
ssh_options=(-o BatchMode=yes -o ControlMaster=no -o ControlPath=none -o ConnectTimeout=10)
environment=()
for name in DEPLOY_HOST DEPLOY_PROFILE DEPLOY_CONFIG DEPLOY_BUILD_LOCK DEPLOY_API_URL DEPLOY_PROTECTED_SERVICES DEPLOY_REQUIRED_SERVICES DEPLOY_DNS_SERVER DEPLOY_DNS_NAME DEPLOY_DNS_EXPECTED NIX_BUILD_DIRECTORY; do
  if [[ -n ${!name:-} ]]; then
    environment+=("$name=${!name}")
  fi
done
remote_script() {
  local command
  printf -v command '%q ' env "${environment[@]}" bash "$remote/deploy/$1"
  shift
  if (( $# > 0 )); then
    local arguments
    printf -v arguments '%q ' "$@"
    command+="$arguments"
  fi
  # 命令参数已在本机通过 printf %q 编码，再交给远端 shell。
  # shellcheck disable=SC2029
  ssh "${ssh_options[@]}" "$host" "$command"
}
remote_script build-system.sh "$nix_config"
# remote 已限制为绝对路径和安全字符。
# shellcheck disable=SC2029
[[ $(ssh "${ssh_options[@]}" "$host" "cat '$remote/.work/activation/candidate-system'") == "$expected_candidate" ]]
remote_script test-system.sh
remote_script check-system.sh
remote_script persist-system.sh
