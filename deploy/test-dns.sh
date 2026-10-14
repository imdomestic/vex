#!/usr/bin/env bash
set -euo pipefail
umask 077
project=$(cd "$(dirname "$0")/.." && pwd)
export TMPDIR="$project/.work/temp"
dnsmasq=/nix/store/cc9lkx3igxymsdsxnv99zsfcxp1a5b64-dnsmasq-2.93/bin/dnsmasq
configuration=/nix/store/kxzfx74ij8aq1mlr2mw3xg63q7ah5vq2-chorus-dnsmasq.conf
"$dnsmasq" --test --conf-file="$configuration"
"$dnsmasq" --keep-in-foreground --user=linwhite --port=1053 --pid-file="$TMPDIR/dns-test.pid" --conf-file="$configuration" >"$TMPDIR/dns-test.log" 2>&1 &
dns_pid=$!
trap 'kill "$dns_pid"; wait "$dns_pid"' EXIT
for attempt in {1..20}; do
  kill -0 "$dns_pid"
  answer=$(dig @100.64.0.43 -p 1053 main-fabric.chorus.internal A +short +time=1 +tries=1)
  if [[ "$answer" == 100.64.0.43 ]]; then
    break
  fi
  sleep 1
done
[[ "$answer" == 100.64.0.43 ]]
[[ $(dig @10.1.2.137 -p 1053 plaza.chorus.internal A +short +tcp +time=1 +tries=1) == 100.64.0.43 ]]
[[ -z $(dig @10.1.2.137 -p 1053 example.com A +short +time=1 +tries=1) ]]
printf '%s\n' 'DNS UDP/TCP wildcard and external-zone rejection verified'
