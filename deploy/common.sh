#!/usr/bin/env bash
set -euo pipefail
set +x
umask 077
project=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export TMPDIR="$project/.work/temp"
export PYTHONDONTWRITEBYTECODE=1
install -d -m 0700 "$TMPDIR" "$project/.work/application" "$project/.work/activation"
application_profile=${DEPLOY_PROFILE:-/nix/var/nix/profiles/vex}
configuration=${DEPLOY_CONFIG:-/etc/vex/vexd.toml}
build_lock=${DEPLOY_BUILD_LOCK:-/var/lib/vex/build.lock}
api_url=${DEPLOY_API_URL:-http://127.0.0.1:8790}
# 由加载本文件的部署脚本共用。
# shellcheck disable=SC2034
activation="$project/.work/activation"
deploy_host=${DEPLOY_HOST:-$(hostname)}
[[ "$deploy_host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]]
[[ "$application_profile" == /* && "$configuration" == /* && "$build_lock" == /* ]]

require_linux() {
  [[ $(uname -s) == Linux && $(hostname) == "$deploy_host" ]]
}

require_space() {
  local available
  available=$(df -B1 --output=avail "$project" | tail -n 1)
  (( available >= 15 * 1024 * 1024 * 1024 ))
}

require_idle_workers() {
  systemctl list-units 'vex-build@*' --all --output=json >"$TMPDIR/build-units.json"
  jq -e 'all(.[]; (.active == "inactive" or .active == "failed") and .sub != "running")' "$TMPDIR/build-units.json" >/dev/null
}

check_api() {
  curl --fail --silent --show-error --retry 5 --retry-connrefused --retry-delay 1 \
    --connect-timeout 3 --max-time 10 "${api_url%/}/v2/health" >"$TMPDIR/health.json"
  jq -e '.status == "ok"' "$TMPDIR/health.json" >/dev/null
  [[ $(curl --silent --show-error --connect-timeout 3 --max-time 10 --output /dev/null --write-out '%{http_code}' "${api_url%/}/v2/me") == 401 ]]
}
