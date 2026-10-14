import argparse
import json
import sqlite3
import sys
from pathlib import Path

import anvil
import nbtlib


def main():
    parser = argparse.ArgumentParser(description="验证独立恢复目录中的 Minecraft 世界与 SQLite 数据库")
    parser.add_argument("root", type=Path)
    args = parser.parse_args()
    levels = sorted(args.root.rglob("level.dat"))
    assert levels, "恢复目录中没有世界"
    for path in levels:
        nbtlib.load(path)
    regions = sorted(args.root.rglob("*.mca"))
    assert regions, "恢复目录中没有 Anvil 区域文件"
    chunks = 0
    empty_regions = []
    for index, path in enumerate(regions, 1):
        if path.stat().st_size == 0:
            empty_regions.append(str(path.relative_to(args.root)))
            continue
        assert path.stat().st_size >= 8192, f"区域文件头部不完整：{path}"
        region = anvil.Region.from_file(str(path))
        for x in range(32):
            for z in range(32):
                if region.chunk_location(x, z) != (0, 0):
                    region.chunk_data(x, z)
                    chunks += 1
        if index % 100 == 0:
            print(f"世界检查：{index}/{len(regions)} 个区域文件，{chunks} 个区块", file=sys.stderr, flush=True)
    sqlite_paths = [
        args.root / "lobby/plugins/CMI/cmi.sqlite.db",
        args.root / "bingo/config/yet-another-minecraft-bingo/bingo.db",
    ]
    for path in sqlite_paths:
        assert path.is_file(), f"缺少数据库：{path}"
        connection = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
        with connection:
            assert connection.execute("PRAGMA integrity_check").fetchall() == [("ok",)], path
        connection.close()
    print(json.dumps({"worlds": len(levels), "regions": len(regions), "chunks": chunks,
                      "emptyRegionFiles": empty_regions,
                      "sqliteDatabases": len(sqlite_paths), "verified": True}))


if __name__ == "__main__":
    main()
