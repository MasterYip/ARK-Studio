"""Record the contract fixtures the Go-only suite starts from.

    python -B tests/python/contract/record_fixtures.py          # rewrite fixtures/*.zip
    python -B tests/python/contract/record_fixtures.py --check  # fixtures present and loadable

fixture.py builds a data dir with the frozen Python reference store, so the
Go-only runs (harness.py against ark-server, golden.py) cannot call it.  This
script runs fixture.py once per mode (``local``: no accounts, ``auth``: 3
members + owner) and stores the data dir plus fixture.json in
``fixtures/<mode>.zip``.  ``serverctl.recorded_fixture`` extracts one and
points the org registry at the new location.  Strings inside the databases
and logs that name the recording-time directory stay as recorded; they are
the same in every extraction, so goldens recorded from an archive stay
comparable on any machine.

Re-record only when fixture.py changes, then re-record the goldens
(golden.py --record), since every golden depends on the fixture bytes.
"""
from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
FIXTURES = HERE / "fixtures"
MODES = {"local": 0, "auth": 3}


def record(mode: str, auth: int) -> Path:
    work = Path(tempfile.mkdtemp(prefix="ark-fixture-"))
    try:
        data = work / "data"
        out = subprocess.run([sys.executable, "-B", str(HERE / "fixture.py"), str(data), "--auth", str(auth)],
                             cwd=str(HERE.parents[2]), capture_output=True, text=True, check=True)
        fixture = json.loads(out.stdout.strip().splitlines()[-1])
        recorded = str(Path(fixture["data_dir"]).resolve())
        meta = {"mode": mode, "auth": auth, "recorded_data_dir": recorded,
                "fixture": json.loads(json.dumps(fixture).replace(json.dumps(recorded)[1:-1], "@DATA@"))}
        FIXTURES.mkdir(exist_ok=True)
        target = FIXTURES / f"{mode}.zip"
        with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
            z.writestr("fixture.json", json.dumps(meta, indent=1, sort_keys=True) + "\n")
            for path in sorted(data.rglob("*")):
                if path.is_file():
                    info = zipfile.ZipInfo("data/" + path.relative_to(data).as_posix(), (2026, 1, 1, 0, 0, 0))
                    info.compress_type = zipfile.ZIP_DEFLATED
                    z.writestr(info, path.read_bytes())
        return target
    finally:
        shutil.rmtree(work, ignore_errors=True)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args(argv)
    if args.check:
        sys.path.insert(0, str(HERE))
        from serverctl import recorded_fixture
        for mode in MODES:
            with tempfile.TemporaryDirectory() as tmp:
                fixture = recorded_fixture(Path(tmp) / "f", mode)
                assert (Path(fixture["data_dir"]) / "org.sqlite3").is_file(), mode
        print("fixtures ok:", ", ".join(MODES))
        return 0
    for mode, auth in MODES.items():
        print("recorded", record(mode, auth))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
