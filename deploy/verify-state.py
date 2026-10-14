import argparse
import json
import sqlite3
from pathlib import Path


parser = argparse.ArgumentParser()
parser.add_argument('database', type=Path)
args = parser.parse_args()
connection = sqlite3.connect(args.database.resolve(strict=True).as_uri() + '?mode=ro', uri=True)
assert connection.execute('PRAGMA integrity_check').fetchall() == [('ok',)]
counts = {}
for kind, identifier, data in connection.execute('SELECT kind,id,data FROM records ORDER BY kind,id'):
    record = json.loads(data)
    counts[kind] = counts.get(kind, 0) + 1
    if kind == 'build':
        assert record['status'] not in ('queued', 'building'), f'构建仍在进行：{identifier}'
    if kind == 'room':
        assert record['state'] not in ('starting', 'stopping', 'archiving'), f'房间仍在转换状态：{identifier}'
connection.close()
print(json.dumps({'integrity': 'ok', 'records': counts}, sort_keys=True))
