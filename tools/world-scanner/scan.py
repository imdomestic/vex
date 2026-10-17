import argparse
import json
import os
import tarfile
import tempfile
from pathlib import Path, PurePosixPath

import anvil
import nbtlib
from nbt import nbt


def permitted(path):
    return Path(path).resolve(strict=True)


def identifiers(value):
    result = set()

    def identifier(value):
        namespace, separator, name = str(value).partition(':')
        if separator and namespace and name:
            result.add(str(value))

    def visit(value, key=''):
        if isinstance(value, nbt.TAG_Compound):
            for child in value.tags:
                visit(child, child.name or key)
        elif isinstance(value, nbt.TAG_List):
            for child in value.tags:
                visit(child, key)
        elif isinstance(value, nbt.TAG_String):
            if key in ('id', 'Name', 'palette'):
                identifier(value.value)
        elif isinstance(value, dict):
            for name, child in value.items():
                visit(child, name or key)
        elif isinstance(value, (list, nbtlib.List)):
            for child in value:
                visit(child, key)
        elif isinstance(value, (str, nbtlib.String)) and key in ('id', 'Name', 'palette'):
            identifier(value)

    visit(value)
    return result


def scan_world(directory, minecraft):
    world = permitted(directory)
    found = set()
    counts = {'regions': 0, 'chunks': 0, 'nbtFiles': 0}

    if (world / 'level.dat').is_symlink():
        raise ValueError('世界目录包含链接')
    level = nbtlib.load(permitted(world / 'level.dat'))
    version = str(level['Data']['Version']['Name'])
    if version != minecraft:
        raise ValueError(f'世界版本 {version} 与要求版本 {minecraft} 不一致')
    for directory, folders, files in os.walk(world, followlinks=False):
        for name in folders + files:
            if (Path(directory) / name).is_symlink():
                raise ValueError('世界目录包含链接')
        for name in files:
            path = permitted(Path(directory) / name)
            if not path.is_file():
                raise ValueError('世界目录包含特殊文件')
            if path.suffix == '.mca':
                region = anvil.Region.from_file(str(path))
                counts['regions'] += 1
                for x in range(32):
                    for z in range(32):
                        if region.chunk_location(x, z) == (0, 0):
                            continue
                        found.update(identifiers(region.chunk_data(x, z)))
                        counts['chunks'] += 1
            elif path.suffix in ('.dat', '.dat_old'):
                found.update(identifiers(nbtlib.load(path)))
                counts['nbtFiles'] += 1
    namespaces = {value.partition(':')[0] for value in found}
    namespaces.discard('minecraft')
    return {'minecraft': version, 'namespaces': sorted(namespaces), **counts}


def scan_archive(archive, minecraft, work_dir):
    source = permitted(archive)
    scratch = permitted(work_dir)
    with tempfile.TemporaryDirectory(prefix='world-scan-', dir=scratch) as directory:
        with tarfile.open(source, 'r:gz') as bundle:
            seen = set()
            size = 0
            for entry in bundle.getmembers():
                path = PurePosixPath(entry.name)
                if path.is_absolute() or '..' in path.parts or '\\' in entry.name or not path.parts or path.parts[0] != 'world':
                    raise ValueError('世界归档路径无效')
                if not entry.isfile() and not entry.isdir():
                    raise ValueError('世界归档包含链接或特殊文件')
                normalized = str(path)
                if normalized in seen:
                    raise ValueError('世界归档包含重复路径')
                seen.add(normalized)
                size += entry.size
                if entry.size < 0 or size > 64 * 1024 ** 3:
                    raise ValueError('世界归档超过展开大小限制')
            bundle.extractall(directory, filter='data')
        return scan_world(Path(directory) / 'world', minecraft)


def main():
    parser = argparse.ArgumentParser(description='读取真实世界的版本、区域和命名空间')
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument('--world', type=Path)
    source.add_argument('--archive', type=Path)
    parser.add_argument('--minecraft', required=True)
    parser.add_argument('--work-dir', type=Path, required=True)
    args = parser.parse_args()
    result = scan_world(args.world, args.minecraft) if args.world else scan_archive(args.archive, args.minecraft, args.work_dir)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    main()
