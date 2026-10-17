import argparse
import json
import re
import sqlite3
import subprocess
from pathlib import Path


parser = argparse.ArgumentParser()
parser.add_argument('--database', type=Path, required=True)
parser.add_argument('--project', required=True)
parser.add_argument('--room', required=True)
parser.add_argument('--kubectl', default='kubectl')
args = parser.parse_args()
assert re.fullmatch('[a-z][a-z0-9-]{0,22}', args.project) and '--' not in args.project and not args.project.endswith('-')
assert re.fullmatch('[a-z0-9-]+', args.room)
connection = sqlite3.connect(args.database.resolve(strict=True).as_uri() + '?mode=ro', uri=True)
room = json.loads(connection.execute('SELECT data FROM records WHERE kind=? AND id=?', ('room', f'{args.project}/{args.room}')).fetchone()[0])
build = json.loads(connection.execute('SELECT data FROM records WHERE kind=? AND id=?', ('build', f'{args.project}/{room["buildId"]}')).fetchone()[0])
connection.close()
assert room['project'] == args.project and build['project'] == args.project
command = [args.kubectl, '-n', f'vex-p-{args.project}']
selector = f'vex.imdomestic.com/project={args.project},vex.imdomestic.com/room={args.room},app.kubernetes.io/component=minecraft'
pods = json.loads(subprocess.run(command + ['get', 'pods', '-l', selector, '-o', 'json'], check=True, capture_output=True).stdout)['items']
assert len(pods) == 1
pod = pods[0]
pod_name = pod['metadata']['name']
assert pod['metadata']['labels']['vex.imdomestic.com/build'] == room['buildId']
assert pod['metadata']['labels']['vex.imdomestic.com/generation'] == str(room['generation'])
assert any(condition['type'] == 'Ready' and condition['status'] == 'True' for condition in pod['status']['conditions'])
files = [entry for entry in build['files'] if entry['side'] in ('both', 'server')]
for entry in files:
    assert Path(entry['name']).name == entry['name'] and '\n' not in entry['name'] and '\r' not in entry['name']
for algorithm, length in [('sha1', 40), ('sha256', 64), ('sha512', 128)]:
    assert all(re.fullmatch(f'[0-9a-f]{{{length}}}', entry[algorithm]) for entry in files)
    checksums = ''.join(f'{entry[algorithm]}  /data/mods/{entry["name"]}\n' for entry in files)
    subprocess.run(command + ['exec', '-i', pod_name, '-c', 'mc', '--', algorithm + 'sum', '--check', '--status'], input=checksums.encode(), check=True)
actual = subprocess.run(command + ['exec', pod_name, '-c', 'mc', '--', 'find', '/data/mods', '-maxdepth', '1', '-type', 'f', '-name', '*.jar', '-printf', '%f\n'], check=True, capture_output=True).stdout.decode().splitlines()
assert sorted(actual) == sorted(entry['name'] for entry in files)
for entry in files:
    size = subprocess.run(command + ['exec', pod_name, '-c', 'mc', '--', 'stat', '-c', '%s', f'/data/mods/{entry["name"]}'], check=True, capture_output=True).stdout
    assert int(size) == entry['size']
print(json.dumps({'project': args.project, 'room': room['id'], 'build': build['id'], 'generation': room['generation'], 'verifiedMods': len(files), 'hashes': ['sha1', 'sha256', 'sha512']}))
