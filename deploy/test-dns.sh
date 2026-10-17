#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
[[ $# == 3 ]]
server=$1
name=$2
expected=$3
[[ "$name" == *--*.* ]]
[[ $(dig "@$server" "$name" A +short +time=2 +tries=1) == "$expected" ]]
[[ $(dig "@$server" "$name" A +short +tcp +time=2 +tries=1) == "$expected" ]]
printf 'DNS UDP/TCP verified: %s\n' "$name"
