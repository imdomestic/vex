set -euo pipefail
umask 077

[[ $# == 2 ]] || { echo '用法：bash legacy_archive.sh <Python路径> <全新归档目录>' >&2; exit 2; }
[[ $EUID == 0 ]] || { echo '需要 root 权限读取完整服务数据。' >&2; exit 2; }
python_bin=$1
archive_dir=$2
support_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ $archive_dir == /* && ! -e $archive_dir ]] || { echo '归档目录必须是全新绝对路径。' >&2; exit 2; }
for command in jq zstd tar nix nix-store pg_dump psql mariadb mariadb-dump sha256sum runuser; do
  command -v "$command" > /dev/null
done

services=(proxy lobby GTL bingo speedrun bedrock-proxy)
for name in "${services[@]}"; do
  [[ $(systemctl show "minecraft-server-$name.service" -p ActiveState --value) == inactive ]] || {
    echo "需要先完成已授权的服务停止：minecraft-server-$name.service" >&2
    exit 3
  }
  [[ $(systemctl show "minecraft-server-$name.service" -p MainPID --value) == 0 ]]
done
for database_service in mysql postgresql; do
  systemctl is-active --quiet "$database_service"
done

mkdir -m 0700 -- "$archive_dir"
mkdir -m 0700 -- "$archive_dir/databases" "$archive_dir/metadata" "$archive_dir/.work"
export TMPDIR="$archive_dir/.work"
date --utc --iso-8601=seconds > "$archive_dir/metadata/captured-at.txt"
readlink -f /run/current-system > "$archive_dir/metadata/current-system.txt"
getent passwd minecraft > "$archive_dir/metadata/minecraft-user.txt"
getent group minecraft > "$archive_dir/metadata/minecraft-group.txt"

# 固定当前系统版本，并单独导出 Minecraft 服务实际依赖的 Nix 文件。
nix-store --add-root "$archive_dir/current-system" --indirect --realise /run/current-system > /dev/null
"$python_bin" -B "$support_dir/legacy_files.py" nix-roots > "$archive_dir/metadata/nix-roots.txt"
mapfile -t nix_roots < "$archive_dir/metadata/nix-roots.txt"
mkdir -m 0700 -- "$archive_dir/gcroots"
for index in "${!nix_roots[@]}"; do
  nix-store --add-root "$archive_dir/gcroots/runtime-$index" --indirect --realise "${nix_roots[$index]}" > /dev/null
done
nix-store --query --requisites "${nix_roots[@]}" > "$archive_dir/metadata/nix-requisites.txt"
mapfile -t nix_paths < "$archive_dir/metadata/nix-requisites.txt"
nix path-info --json --json-format 1 "${nix_paths[@]}" > "$archive_dir/metadata/nix-path-info.json"
runtime_bytes=$(jq '[.[] | .narSize] | add' "$archive_dir/metadata/nix-path-info.json")
source_bytes=$(du -sb /var/lib/minecraft /var/lib/minecraft-secrets | awk '{total += $1} END {printf "%.0f", total}')
available_bytes=$(df -B1 --output=avail "$archive_dir" | tail -n 1)
required_bytes=$((3 * (source_bytes + runtime_bytes) + 1073741824))
[[ $available_bytes -ge $required_bytes ]] || { echo '归档与独立恢复所需磁盘空间不足。' >&2; exit 3; }
nix-store --export "${nix_paths[@]}" | zstd -T2 -3 -o "$archive_dir/minecraft-runtime.nar.zst"

"$python_bin" -B "$support_dir/legacy_files.py" catalog /var/lib/minecraft > "$archive_dir/metadata/minecraft-files.json"
"$python_bin" -B "$support_dir/legacy_files.py" catalog /var/lib/minecraft-secrets > "$archive_dir/metadata/secret-files.json"
tar --format=pax --acls --xattrs --xattrs-include='*' --numeric-owner -I 'zstd -T2 -3' -cpf "$archive_dir/minecraft-data.tar.zst" \
  -C / var/lib/minecraft var/lib/minecraft-secrets
"$python_bin" -B "$support_dir/legacy_files.py" verify /var/lib/minecraft "$archive_dir/metadata/minecraft-files.json"
"$python_bin" -B "$support_dir/legacy_files.py" verify /var/lib/minecraft-secrets "$archive_dir/metadata/secret-files.json"

for name in "${services[@]}"; do
  systemctl cat "minecraft-server-$name.service" "minecraft-server-$name.socket" > "$archive_dir/metadata/$name.units"
done
for database in minecraft luckperms; do
  runuser -u postgres -- pg_dump --format=custom --create --dbname="$database" > "$archive_dir/databases/postgres-$database.dump"
  runuser -u postgres -- psql -v ON_ERROR_STOP=1 -At --dbname="$database" < "$support_dir/legacy_postgres_counts.sql" \
    > "$archive_dir/metadata/postgres-$database-counts.txt"
done
runuser -u postgres -- psql -v ON_ERROR_STOP=1 -Atc \
  "SELECT format('CREATE ROLE %I LOGIN PASSWORD %L;', rolname, rolpassword) FROM pg_authid WHERE rolname = 'minecraft' AND NOT rolsuper AND rolinherit AND NOT rolcreaterole AND NOT rolcreatedb AND rolcanlogin AND NOT rolreplication AND NOT rolbypassrls AND rolconnlimit = -1 AND rolvaliduntil IS NULL" \
  > "$archive_dir/databases/postgres-role.sql"
[[ -s $archive_dir/databases/postgres-role.sql ]]
mariadb-dump --single-transaction --routines --events --triggers --hex-blob --databases minecraft \
  > "$archive_dir/databases/mariadb-minecraft.sql"
mariadb --batch --raw --skip-column-names -e "SHOW CREATE USER 'mc_user'@'localhost'" \
  > "$archive_dir/databases/mariadb-user.tsv"
mariadb --batch --raw --skip-column-names -e "SHOW GRANTS FOR 'mc_user'@'localhost'" \
  > "$archive_dir/databases/mariadb-grants.tsv"
mariadb --batch --skip-column-names < "$support_dir/legacy_mariadb_counts.sql" > "$archive_dir/metadata/mariadb-counts.txt"
mariadb --batch --skip-column-names -e 'SHOW TABLES IN minecraft' > "$archive_dir/metadata/mariadb-tables.txt"
pg_dump --version > "$archive_dir/metadata/postgresql-version.txt"
mariadb-dump --version > "$archive_dir/metadata/mariadb-version.txt"

for name in "${services[@]}"; do
  [[ $(systemctl show "minecraft-server-$name.service" -p ActiveState --value) == inactive ]]
done
(
  cd -- "$archive_dir"
  sha256sum minecraft-data.tar.zst minecraft-runtime.nar.zst databases/* metadata/* > SHA256SUMS
)
echo "归档文件已生成：$archive_dir"
echo '完成独立恢复验证后，才能将该目录登记为可恢复归档。'
