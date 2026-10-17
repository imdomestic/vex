import argparse
import json
import re
import sqlite3
from pathlib import Path


parser = argparse.ArgumentParser()
parser.add_argument('database', type=Path)
parser.add_argument('--quiescent', action='store_true')
args = parser.parse_args()
connection = sqlite3.connect(args.database.resolve(strict=True).as_uri() + '?mode=ro', uri=True)
assert connection.execute('PRAGMA integrity_check').fetchall() == [('ok',)]
counts = {}
for kind, identifier, data in connection.execute('SELECT kind,id,data FROM records ORDER BY kind,id'):
    project, separator, local_id = identifier.partition('/')
    assert separator and local_id and '/' not in local_id
    assert re.fullmatch('[a-z][a-z0-9-]{0,22}', project) and '--' not in project and not project.endswith('-')
    record = json.loads(data)
    if isinstance(record, dict) and 'project' in record:
        assert record['project'] == project
    counts.setdefault(project, {})
    counts[project][kind] = counts[project].get(kind, 0) + 1
    if args.quiescent and kind == 'build':
        assert record['status'] not in ('queued', 'building'), f'构建仍在进行：{identifier}'
    if args.quiescent and kind == 'room':
        assert record['state'] not in ('queued', 'building', 'starting', 'stopping', 'archiving'), f'房间仍在转换状态：{identifier}'
for name, value in connection.execute('SELECT name,value FROM counters'):
    project, separator, local_id = name.partition('/')
    assert separator and local_id and '/' not in local_id and value >= 1
    assert re.fullmatch('[a-z][a-z0-9-]{0,22}', project) and '--' not in project and not project.endswith('-')
connection.close()
print(json.dumps({'integrity': 'ok', 'projects': counts}, sort_keys=True))
