#!/usr/bin/env bash
set -euo pipefail
[[ $(uname -s) == Darwin ]]
[[ $# == 2 ]]
nix_config=$1
expected_candidate=$2
[[ "$nix_config" == /* && "$nix_config" =~ ^[A-Za-z0-9_./-]+$ ]]
[[ "$expected_candidate" == /nix/store/*-nixos-system-encore-* ]]
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
remote=/home/linwhite/chorus-testbed
ssh_options=(-o BatchMode=yes -o ControlMaster=no -o ControlPath=none -o ConnectTimeout=10)
install -d -m 0700 "$project/.work/activation"
ssh "${ssh_options[@]}" encore "bash $remote/deploy/build-system.sh $nix_config"
[[ $(ssh "${ssh_options[@]}" encore "cat $remote/.work/activation/candidate-system") == "$expected_candidate" ]]
ssh "${ssh_options[@]}" encore "bash $remote/deploy/test-system.sh"
ssh "${ssh_options[@]}" encore "bash $remote/deploy/check-system.sh"
[[ $(curl --silent --show-error --connect-timeout 5 --max-time 10 --output /dev/null --write-out '%{http_code}' http://100.64.0.43:8790/v1/health) == 401 ]]
[[ $(dig @100.64.0.43 main-fabric.chorus.internal A +short +time=2 +tries=1) == 100.64.0.43 ]]
ssh "${ssh_options[@]}" encore "bash $remote/deploy/persist-system.sh"
