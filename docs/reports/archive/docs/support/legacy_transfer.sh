set -euo pipefail
umask 077

[[ $# == 2 ]] || { echo '用法：bash legacy_transfer.sh <本地归档目录> <受信任known_hosts文件>' >&2; exit 2; }
[[ $EUID == 0 ]]
archive_dir=$1
known_hosts=$2
support_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
stamp=${archive_dir##*/legacy-}
[[ $archive_dir == /* && $stamp =~ ^[0-9]{8}T[0-9]{6}Z$ ]]
[[ $known_hosts =~ ^/[a-zA-Z0-9_./-]+$ && -f $known_hosts ]]
[[ -f $archive_dir/LOCAL_RESTORE_VERIFIED ]]
export TMPDIR="$archive_dir/.work"
(
  cd -- "$archive_dir"
  sha256sum --check SHA256SUMS
)
remote_dir="/srv/archive/chorus/legacy/$stamp"
read -r archive_id unused < <(sha256sum "$archive_dir/SHA256SUMS")
ssh_options=(-o BatchMode=yes -o ConnectTimeout=12 -o StrictHostKeyChecking=yes -o UpdateHostKeys=no -o "UserKnownHostsFile=$known_hosts")
runuser -u linwhite -- ssh "${ssh_options[@]}" root@tank bash -s -- "$remote_dir" "$archive_id" \
  < "$support_dir/legacy_transfer_prepare.sh"
rsync -a --partial --bwlimit=5120 --chmod=D700,F600 \
  --rsh="runuser -u linwhite -- ssh -o BatchMode=yes -o ConnectTimeout=12 -o StrictHostKeyChecking=yes -o UpdateHostKeys=no -o UserKnownHostsFile=$known_hosts" \
  "$archive_dir/" "root@tank:$remote_dir/"
runuser -u linwhite -- ssh "${ssh_options[@]}" root@tank \
  "umask 077; cd '$remote_dir' && test -f LOCAL_RESTORE_VERIFIED && sha256sum --check SHA256SUMS && date -u --iso-8601=seconds > REMOTE_VERIFIED"
date -u --iso-8601=seconds > "$archive_dir/TANK_VERIFIED"
echo "tank 归档校验完成：$remote_dir"
