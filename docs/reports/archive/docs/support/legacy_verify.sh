set -euo pipefail
umask 077

[[ $# == 4 || $# == 5 ]] || { echo '用法：bash legacy_verify.sh <Python路径> <Java路径> <归档目录> <全新恢复目录> [all|databases]' >&2; exit 2; }
[[ $EUID == 0 ]] || { echo '需要 root 权限恢复原始属主信息。' >&2; exit 2; }
python_bin=$1
java_bin=$2
archive_dir=$3
restore_dir=$4
scope=${5:-all}
[[ $scope == all || $scope == databases ]]
for command in zstd tar nix-store pg_restore psql initdb pg_ctl mariadb mariadb-install-db mariadbd mariadb-check mariadb-admin sha256sum runuser; do
  command -v "$command" > /dev/null
done
support_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ $restore_dir =~ ^/[a-zA-Z0-9_./-]+$ && ! -e $restore_dir ]]
db_work="$restore_dir/.work/databases"
pg_socket_file="$db_work/pgsocket/.s.PGSQL.5432"
maria_socket_file="$db_work/mariadb.sock"
[[ ${#pg_socket_file} -lt 108 && ${#maria_socket_file} -lt 108 ]] || {
  echo '恢复目录过长，无法创建数据库 Unix socket。' >&2
  exit 2
}
runuser -u linwhite -- test -x "$(dirname -- "$restore_dir")"
mkdir -m 0700 -- "$restore_dir"
install -d -m 0700 -- "$restore_dir/.work"
export TMPDIR="$restore_dir/.work"
(
  cd -- "$archive_dir"
  sha256sum --check SHA256SUMS
)

if [[ $scope == all ]]; then
  tar --acls --xattrs --xattrs-include='*' --numeric-owner -I zstd -xpf "$archive_dir/minecraft-data.tar.zst" -C "$restore_dir"
  "$python_bin" -B "$support_dir/legacy_files.py" verify "$restore_dir/var/lib/minecraft" "$archive_dir/metadata/minecraft-files.json"
  "$python_bin" -B "$support_dir/legacy_files.py" verify "$restore_dir/var/lib/minecraft-secrets" "$archive_dir/metadata/secret-files.json"
  "$python_bin" -B "$support_dir/legacy_validate_worlds.py" "$restore_dir/var/lib/minecraft" > "$restore_dir/world-validation.json"
  zstd -dc "$archive_dir/minecraft-runtime.nar.zst" | nix-store --store "local?root=$restore_dir/.work/nix" --import > "$restore_dir/nix-imported.txt"
  nix-store --store "local?root=$restore_dir/.work/nix" --verify --check-contents
fi

# 数据库实例使用本次恢复目录内的 socket，关闭 TCP 监听。
install -d -o linwhite -g users -m 0700 -- "$db_work" "$db_work/pgsocket" "$db_work/work"
chown linwhite:users "$restore_dir" "$restore_dir/.work"
pg_started=false
maria_pid=
cleanup() {
  if [[ -n $maria_pid ]]; then
    kill -TERM "$maria_pid"
    wait "$maria_pid"
  fi
  if [[ $pg_started == true ]]; then
    runuser -u linwhite -- pg_ctl -D "$db_work/postgresql" -m fast -w stop
  fi
}
trap cleanup EXIT
runuser -u linwhite -- initdb -D "$db_work/postgresql" -U postgres --auth=trust --no-instructions > "$db_work/postgresql-init.log"
runuser -u linwhite -- pg_ctl -D "$db_work/postgresql" -l "$db_work/postgresql.log" \
  -o "-k $db_work/pgsocket -c listen_addresses='' -c shared_buffers=32MB -c work_mem=2MB -c maintenance_work_mem=32MB -c max_connections=10" -w start
pg_started=true
psql -h "$db_work/pgsocket" -U postgres -v ON_ERROR_STOP=1 < "$archive_dir/databases/postgres-role.sql" > "$db_work/postgres-role.log"
for database in minecraft luckperms; do
  pg_restore -h "$db_work/pgsocket" -U postgres --exit-on-error --create --dbname=postgres "$archive_dir/databases/postgres-$database.dump"
  psql -h "$db_work/pgsocket" -U postgres -v ON_ERROR_STOP=1 -At --dbname="$database" < "$support_dir/legacy_postgres_counts.sql" > "$db_work/postgres-$database-counts.txt"
  cmp "$archive_dir/metadata/postgres-$database-counts.txt" "$db_work/postgres-$database-counts.txt"
done
runuser -u linwhite -- pg_ctl -D "$db_work/postgresql" -m fast -w stop
pg_started=false

runuser -u linwhite -- mariadb-install-db --no-defaults --datadir="$db_work/mariadb" \
  --tmpdir="$db_work/work" --auth-root-authentication-method=normal --skip-test-db > "$db_work/mariadb-init.log"
mariadbd --no-defaults --user=linwhite --datadir="$db_work/mariadb" \
  --socket="$db_work/mariadb.sock" --pid-file="$db_work/mariadb.pid" --skip-networking \
  --innodb-buffer-pool-size=64M --key-buffer-size=8M --max-connections=10 --table-open-cache=128 \
  --performance-schema=OFF --sort-buffer-size=1M --join-buffer-size=256K --tmp-table-size=8M --max-heap-table-size=8M \
  --tmpdir="$db_work/work" --log-error="$db_work/mariadb.log" &
maria_pid=$!
attempt=0
until mariadb-admin --no-defaults --socket="$db_work/mariadb.sock" -u root ping --silent > /dev/null 2>&1; do
  kill -0 "$maria_pid"
  ((attempt += 1))
  [[ $attempt -le 30 ]]
  sleep 1
done
"$python_bin" -B "$support_dir/legacy_mariadb_account.py" "$archive_dir" > "$db_work/mariadb-account.sql"
mariadb --no-defaults --socket="$db_work/mariadb.sock" -u root < "$db_work/mariadb-account.sql"
mariadb --no-defaults --socket="$db_work/mariadb.sock" -u root < "$archive_dir/databases/mariadb-minecraft.sql"
mariadb-check --no-defaults --socket="$db_work/mariadb.sock" -u root --databases minecraft
mariadb --no-defaults --socket="$db_work/mariadb.sock" -u root --batch --skip-column-names \
  < "$support_dir/legacy_mariadb_counts.sql" > "$db_work/mariadb-counts.txt"
mariadb --no-defaults --socket="$db_work/mariadb.sock" -u root --batch --skip-column-names \
  -e 'SHOW TABLES IN minecraft' > "$db_work/mariadb-tables.txt"
cmp "$archive_dir/metadata/mariadb-counts.txt" "$db_work/mariadb-counts.txt"
cmp "$archive_dir/metadata/mariadb-tables.txt" "$db_work/mariadb-tables.txt"
mariadb-admin --no-defaults --socket="$db_work/mariadb.sock" -u root shutdown
wait "$maria_pid"
maria_pid=

if [[ $scope == databases ]]; then
  date --utc --iso-8601=seconds > "$restore_dir/DATABASES_VERIFIED"
  echo "独立数据库恢复验证完成：$restore_dir"
  exit 0
fi

bash "$support_dir/legacy_verify_h2.sh" "$java_bin" "$restore_dir"
date --utc --iso-8601=seconds > "$restore_dir/VERIFIED"
echo "独立恢复验证完成：$restore_dir"
