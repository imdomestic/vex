set -euo pipefail
umask 077

[[ $# == 2 && $EUID == 0 ]]
java_bin=$1
restore_dir=$2
[[ $restore_dir =~ ^/[a-zA-Z0-9_./-]+$ && -d $restore_dir ]]
export TMPDIR="$restore_dir/.work"
h2_work="$TMPDIR/h2-verify"
[[ ! -e $h2_work ]]
install -d -m 0700 -- "$h2_work"
for name in proxy bedrock-proxy; do
  plugin_dir="$restore_dir/var/lib/minecraft/$name/plugins/luckperms"
  driver="$plugin_dir/libs/h2-driver-2.1.214.jar"
  [[ -f $driver ]]
  sha256sum "$plugin_dir/luckperms-h2-v2.mv.db" > "$h2_work/$name.source.sha256"
  "$java_bin" -Xmx256m -Djava.io.tmpdir="$TMPDIR" -cp "$driver" org.h2.tools.Script \
    -url "jdbc:h2:$plugin_dir/luckperms-h2-v2;IFEXISTS=TRUE;ACCESS_MODE_DATA=r" \
    -user '' -password '' -script "$h2_work/$name.sql"
  "$java_bin" -Xmx256m -Djava.io.tmpdir="$TMPDIR" -cp "$driver" org.h2.tools.RunScript \
    -url "jdbc:h2:$h2_work/$name-restored" -user '' -password '' -script "$h2_work/$name.sql"
  "$java_bin" -Xmx256m -Djava.io.tmpdir="$TMPDIR" -cp "$driver" org.h2.tools.Script \
    -url "jdbc:h2:$h2_work/$name-restored;IFEXISTS=TRUE;ACCESS_MODE_DATA=r" \
    -user '' -password '' -script "$h2_work/$name-restored.sql"
  cmp "$h2_work/$name.sql" "$h2_work/$name-restored.sql"
  sha256sum --check "$h2_work/$name.source.sha256"
done
date --utc --iso-8601=seconds > "$restore_dir/H2_VERIFIED"
echo '两个 H2 数据库独立恢复和内容比较均已通过。'
