#!/usr/bin/env python3
"""Generate or verify only the Settings adapter using the pinned sqlc version."""
import argparse
import pathlib
import shutil
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
ADAPTER = ROOT / "settings/internal/sqlite"
VERSION = "v1.31.1"


def generate(directory):
    subprocess.run(
        ["go", "run", f"github.com/sqlc-dev/sqlc/cmd/sqlc@{VERSION}",
         "generate", "-f", str(directory / "sqlc.yaml")],
        cwd=ROOT,
        check=True,
    )


def files(directory):
    return {path.relative_to(directory): path.read_bytes()
            for path in directory.rglob("*.go")}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("generate", "check"))
    args = parser.parse_args()
    if args.operation == "generate":
        generate(ADAPTER)
        return
    with tempfile.TemporaryDirectory(prefix="goren-settings-sqlc-") as temporary:
        scratch = pathlib.Path(temporary)
        shutil.copy(ADAPTER / "sqlc.yaml", scratch / "sqlc.yaml")
        shutil.copytree(ADAPTER / "sql", scratch / "sql")
        generate(scratch)
        if files(scratch / "internal/dbsql") != files(ADAPTER / "internal/dbsql"):
            raise SystemExit("Settings sqlc output differs; run scripts/settings-sqlc.py generate")
    print(f"Settings sqlc {VERSION}: no generated drift")


if __name__ == "__main__":
    main()
