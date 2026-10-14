import argparse
import csv
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description="读取 MariaDB 原生账号导出结果")
    parser.add_argument("archive", type=Path)
    args = parser.parse_args()
    with (args.archive / "databases/mariadb-user.tsv").open(newline="") as stream:
        rows = list(csv.reader(stream, delimiter="\t", quoting=csv.QUOTE_NONE))
    assert len(rows) == 1 and len(rows[0]) == 1
    print(rows[0][0] + ";")
    with (args.archive / "databases/mariadb-grants.tsv").open(newline="") as stream:
        for row in csv.reader(stream, delimiter="\t", quoting=csv.QUOTE_NONE):
            assert len(row) == 1
            print(row[0] + ";")


if __name__ == "__main__":
    main()
