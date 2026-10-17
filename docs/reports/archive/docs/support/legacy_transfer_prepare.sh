set -euo pipefail
umask 077

[[ $# == 2 && $EUID == 0 ]]
remote_dir=$1
archive_id=$2
[[ $remote_dir =~ ^/srv/archive/chorus/legacy/[0-9]{8}T[0-9]{6}Z$ ]]
[[ $archive_id =~ ^[0-9a-f]{64}$ ]]
if [[ -e $remote_dir || -L $remote_dir ]]; then
  [[ -d $remote_dir && ! -L $remote_dir ]]
  [[ $(stat -c '%u:%a' "$remote_dir") == 0:700 ]]
  [[ -f $remote_dir/ARCHIVE_ID && ! -L $remote_dir/ARCHIVE_ID ]]
  [[ $(cat "$remote_dir/ARCHIVE_ID") == "$archive_id" ]] || {
    echo '目标目录的归档标记不匹配。' >&2
    exit 3
  }
else
  install -d -m 0700 -- "$remote_dir"
  printf '%s\n' "$archive_id" > "$remote_dir/ARCHIVE_ID"
fi
