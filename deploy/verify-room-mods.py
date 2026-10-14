import argparse
import json
import sqlite3
import subprocess
from pathlib import Path


parser = argparse.ArgumentParser()
parser.add_argument('--database', type=Path, required=True)
parser.add_argument('--room', required=True)
args = parser.parse_args()
connection = sqlite3.connect(args.database.resolve(strict=True).as_uri() + '?mode=ro', uri=True)
room = json.loads(connection.execute('SELECT data FROM records WHERE kind=? AND id=?', ('room', args.room)).fetchone()[0])
build = json.loads(connection.execute('SELECT data FROM records WHERE kind=? AND id=?', ('build', room['buildId'])).fetchone()[0])
connection.close()
command = ['sudo', '-n', 'k3s', 'kubectl', '-n', 'chorus-rooms']
pod_name = f'room-{room["id"]}-0'
pod = json.loads(subprocess.run(command + ['get', 'pod', pod_name, '-o', 'json'], check=True, capture_output=True).stdout)
assert pod['metadata']['labels']['chorus.dev/build'] == room['buildId']
assert pod['metadata']['labels']['chorus.dev/generation'] == str(room['generation'])
assert any(condition['type'] == 'Ready' and condition['status'] == 'True' for condition in pod['status']['conditions'])
files = [entry for entry in build['files'] if entry['side'] in ('both', 'server')]
checksums = ''.join(f'{entry["sha256"]}  /data/mods/{entry["name"]}\n' for entry in files)
subprocess.run(command + ['exec', '-i', pod_name, '-c', 'mc', '--', 'sha256sum', '--check', '--status'], input=checksums.encode(), check=True)
actual = subprocess.run(command + ['exec', pod_name, '-c', 'mc', '--', 'find', '/data/mods', '-maxdepth', '1', '-type', 'f', '-name', '*.jar', '-printf', '%f\n'], check=True, capture_output=True).stdout.decode().splitlines()
assert sorted(actual) == sorted(entry['name'] for entry in files)
for entry in files:
    size = subprocess.run(command + ['exec', pod_name, '-c', 'mc', '--', 'stat', '-c', '%s', f'/data/mods/{entry["name"]}'], check=True, capture_output=True).stdout
    assert int(size) == entry['size']
print(json.dumps({'room': room['id'], 'build': build['id'], 'generation': room['generation'], 'verifiedMods': len(files)}))
