import argparse
import base64
import hashlib
import json
import os
import shutil
import stat
from pathlib import Path


SERVICES = ("proxy", "lobby", "GTL", "bingo", "speedrun", "bedrock-proxy")


def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            checksum.update(block)
    return checksum.hexdigest()


def catalog(root):
    entries = {}
    for path in [root, *sorted(root.rglob("*"))]:
        metadata = path.lstat()
        attributes = {name: base64.b64encode(os.getxattr(path, name, follow_symlinks=False)).decode("ascii")
                      for name in sorted(os.listxattr(path, follow_symlinks=False))}
        item = {"mode": stat.S_IMODE(metadata.st_mode), "uid": metadata.st_uid, "gid": metadata.st_gid,
                "mtime_ns": metadata.st_mtime_ns, "xattrs": attributes}
        if path.is_symlink():
            item.update(type="symlink", target=os.readlink(path))
        elif path.is_dir():
            item.update(type="directory")
        else:
            assert path.is_file(), f"发现不支持的文件类型：{path}"
            item.update(type="file", size=metadata.st_size, sha256=digest(path))
        entries[str(path.relative_to(root))] = item
    return entries


def nix_roots():
    targets = []
    for name in SERVICES:
        for suffix in ("service", "socket"):
            targets.append(Path(f"/etc/systemd/system/minecraft-server-{name}.{suffix}"))
    for name in ("minecraft-mysql-credentials", "minecraft-postgresql-credentials"):
        targets.append(Path(f"/etc/systemd/system/{name}.service"))
    for name in ("pg_dump", "mariadb-dump"):
        executable = shutil.which(name)
        assert executable, f"缺少命令：{name}"
        targets.append(Path(executable))
    targets.extend(path for path in Path("/var/lib/minecraft").rglob("*") if path.is_symlink())
    roots = set()
    for target in targets:
        resolved = target.resolve(strict=True)
        if resolved.is_relative_to("/nix/store"):
            roots.add(str(Path(*resolved.parts[:4])))
    assert roots, "未找到 Nix 运行依赖"
    return sorted(roots)


def main():
    parser = argparse.ArgumentParser(description="核对 Minecraft 归档文件与 Nix 依赖")
    commands = parser.add_subparsers(dest="command", required=True)
    capture = commands.add_parser("catalog")
    capture.add_argument("root", type=Path)
    verify = commands.add_parser("verify")
    verify.add_argument("root", type=Path)
    verify.add_argument("catalog", type=Path)
    commands.add_parser("nix-roots")
    args = parser.parse_args()
    if args.command == "catalog":
        print(json.dumps(catalog(args.root), ensure_ascii=False, sort_keys=True, indent=2))
    elif args.command == "verify":
        expected = json.loads(args.catalog.read_text())
        actual = catalog(args.root)
        assert expected == actual, f"归档文件校验不一致：{args.root}"
        print(json.dumps({"root": str(args.root), "entries": len(actual), "verified": True}))
    else:
        print("\n".join(nix_roots()))


if __name__ == "__main__":
    main()
