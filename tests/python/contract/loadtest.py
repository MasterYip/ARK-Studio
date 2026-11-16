"""Run the same load against the Python and the Go server on one machine.

    python -B tests/python/contract/loadtest.py --users 600 --nodes 2000 --duration 60 --think 1.0
    python -B tests/python/contract/loadtest.py --servers go --users 1000 ...
    python -B tests/python/contract/loadtest.py --users 600 --write-pct 15 --work-root ./runtime-qa

Builds one fixture (fixture.py: N signed-up members with a write grant and
a project with --nodes extra units), gives each server its own byte-identical
copy, starts it, and runs go/ark-load (signs every user in through /login,
then a weighted read/write mix with exponential think time) while sampling
the server process tree.  --write-pct sets the write share (comments, node
edits, topic layout saves and topic design; ark-load's default is 15).  The
Go server runs with ARK_PPROF on a free loopback port and ark-load reads its
SQLite writer-queue counters from /debug/vars (--no-server-stats turns that
off).  Results go to --out (JSON) and stdout (table).
"""
from __future__ import annotations

import argparse
import json
import shutil
import sqlite3
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from serverctl import REPO, Server, copy_fixture, free_port, make_fixture, temp_dir  # noqa: E402


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--users", type=int, default=600)
    parser.add_argument("--nodes", type=int, default=2000)
    parser.add_argument("--duration", type=float, default=60)
    parser.add_argument("--think", type=float, default=1.0)
    parser.add_argument("--servers", default="python,go")
    parser.add_argument("--go", default=str(REPO / "go" / "ark-server.exe"))
    parser.add_argument("--load", default=str(REPO / "go" / "ark-load.exe"))
    parser.add_argument("--go-args", default="", help="extra ark-server flags, e.g. '--heavy-limit 4'")
    parser.add_argument("--read-only", action="store_true", help="no writes in the mix")
    parser.add_argument("--write-pct", type=float, default=None,
                        help="share of writes in the mix, 0..50 (ark-load default: 15)")
    parser.add_argument("--timeout", type=float, default=None, help="client timeout per request, seconds")
    parser.add_argument("--no-server-stats", action="store_true",
                        help="do not expose the Go server's expvar counters (ARK_PPROF)")
    parser.add_argument("--load-args", default="", help="extra ark-load flags, e.g. '--show-errors 5'")
    parser.add_argument("--work-root", default="", help="create the temporary work directory here")
    parser.add_argument("--out", default="")
    parser.add_argument("--keep", action="store_true")
    args = parser.parse_args(argv)
    if args.work_root:
        Path(args.work_root).mkdir(parents=True, exist_ok=True)
        work = Path(tempfile.mkdtemp(prefix="ark-load-", dir=args.work_root))
    else:
        work = temp_dir("ark-load-")
    results = []
    try:
        fixture = make_fixture(work / "fixture", auth=args.users, nodes=args.nodes)
        data = Path(fixture["data_dir"])
        project = data / "projects" / fixture["folder"] / "ark.sqlite3"
        con = sqlite3.connect(str(project))
        uids = [row[0] for row in con.execute("SELECT uid FROM units WHERE archived=0 ORDER BY id")]
        con.close()
        (work / "users.txt").write_text("\n".join(fixture["users"]) + "\n", encoding="utf-8")
        (work / "uids.txt").write_text("\n".join(uids) + "\n", encoding="utf-8")
        for kind in [s.strip() for s in args.servers.split(",") if s.strip()]:
            spec = "python" if kind == "python" else args.go
            copy = copy_fixture(data, work / f"data-{kind}")
            extra = args.go_args.split() if kind != "python" else []
            env = {"ARK_BLACKBOX": "0"}
            load_args = []
            if kind != "python" and not args.no_server_stats:
                stats_port = free_port()
                env["ARK_PPROF"] = f"127.0.0.1:{stats_port}"
                load_args += ["--stats-url", f"http://127.0.0.1:{stats_port}/debug/vars"]
            if args.read_only:
                load_args.append("--read-only")
            if args.write_pct is not None:
                load_args += ["--write-pct", str(args.write_pct)]
            if args.timeout is not None:
                load_args += ["--timeout", f"{args.timeout}s"]
            load_args += args.load_args.split()
            with Server(spec, copy, env=env, args=extra) as server:
                out = work / f"result-{kind}.json"
                subprocess.run([args.load, "--url", f"http://127.0.0.1:{server.port}",
                                "--users", str(work / "users.txt"), "--uids", str(work / "uids.txt"),
                                "--password", fixture["password"], "--duration", f"{args.duration}s",
                                "--think", f"{args.think}s", "--pid", str(server.proc.pid),
                                "--label", kind, "--out", str(out)] + load_args,
                               check=True, stdout=subprocess.DEVNULL)
                result = json.loads(out.read_text(encoding="utf-8"))
                result["units"] = len(uids)
                results.append(result)
    finally:
        if args.keep:
            print("kept", work)
        else:
            shutil.rmtree(work, ignore_errors=True)
    print(f"{'server':8} {'users':>5} {'req':>7} {'rps':>7} {'p50':>8} {'p95':>8} {'p99':>8} {'err%':>6} "
          f"{'cpu avg':>8} {'cpu pk':>7} {'rss MB':>7} {'rss pk':>7} {'thr pk':>6}")
    for r in results:
        o, s = r["overall"], r["server"]
        print(f"{r['label']:8} {r['users']:5} {r['requests']:7} {r['throughput_rps']:7.1f} "
              f"{o['p50_ms']:8.1f} {o['p95_ms']:8.1f} {o['p99_ms']:8.1f} {100 * o['error_rate']:6.2f} "
              f"{s['cpu_cores_avg']:8.2f} {s['cpu_cores_peak']:7.2f} {s['rss_mb_avg']:7.1f} "
              f"{s['rss_mb_peak']:7.1f} {s['threads_peak']:6}")
        for part in ("reads", "writes"):
            st = r.get(part)
            if st and st["count"]:
                print(f"  {part:18} n={st['count']:6} rps={st['throughput_rps']:7.1f} p50={st['p50_ms']:8.1f} "
                      f"p95={st['p95_ms']:8.1f} p99={st['p99_ms']:8.1f} err={st['errors']} 409={st['conflicts']}")
        for name, st in sorted(r["per_endpoint"].items()):
            print(f"    {name:16} n={st['count']:6} p50={st['p50_ms']:8.1f} p95={st['p95_ms']:8.1f} "
                  f"p99={st['p99_ms']:8.1f} err={st['errors']} 409={st.get('conflicts', 0)}"
                  + (f" {st['errors_by_class']}" if st.get("errors_by_class") else ""))
        if r.get("client_contention"):
            print("  client contention:", r["client_contention"])
        for cls, q in sorted((r.get("server_sqlite") or {}).items()):
            print(f"  sqlite {cls:6} writes={q['writes']} queued={q['queued']} wait p50/p95/p99="
                  f"{q['wait_p50_ms']}/{q['wait_p95_ms']}/{q['wait_p99_ms']} ms "
                  f"max={q['since_start']['wait_max_ms']:.1f} ms depth max={q['since_start']['depth_max']} "
                  f"busy={q['busy_errors']} locked={q['locked_errors']} cancelled={q['wait_cancelled']} "
                  f"read-pool waits={q['read_pool']['wait_count']} hold mean={q.get('hold_mean_ms', 0):.2f} ms"
                  + (f" wal restarts={q.get('wal_checkpoints', 0)} (busy {q.get('wal_checkpoint_busy', 0)}) "
                     f"wal max={q.get('wal_max_bytes', 0) / 2**20:.1f} MiB" if cls == "wal" else ""))
    if args.out:
        Path(args.out).write_text(json.dumps({"args": vars(args), "results": results}, indent=2), encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
