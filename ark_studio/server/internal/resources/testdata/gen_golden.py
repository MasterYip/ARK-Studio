"""Regenerate golden.json and setup/ for go/internal/resources (Python reference).

    python -B go/internal/resources/testdata/gen_golden.py      (from the repo root)

Every case is a build_graph(sources, viewer) call: the cases of
tests/python/server/test_resources_graph.py plus edge cases.  Setup cases
read the folders under setup/ with setup_layers.read_layers/recent_history
(the Go test reads the same folders with ReadLayers/RecentHistory).  The Go
test compares json.dumps(graph, ensure_ascii=False) byte for byte.
"""
from __future__ import annotations

import json
import shutil
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]
sys.path.insert(0, str(REPO / "python"))  # ark_studio/python (arkstudio)

from arkstudio import resources_graph as rg  # noqa: E402
from arkstudio.skills import setup_layers  # noqa: E402
from tests.python.server import test_resources_graph as T  # noqa: E402

GPU, NOW, iso, layer, sources = T.GPU, T.NOW, T.iso, T.layer, T.sources


def unit_cases() -> list[tuple[str, dict, str]]:
    out = []
    add = lambda name, src, viewer="alice": out.append((name, src, viewer))  # noqa: E731
    add("ark_sessions", sources(sessions=[{"owner": "alice", "status": "running", "worktree": "/w/feat-a"},
                                          {"owner": "bob", "status": "done"},
                                          {"owner": "alice", "status": "waiting", "worktree": "/w/feat-a"},
                                          {"owner": "carol", "status": "starting", "worktree": "/w/b/"}]))
    add("gpu", sources(gpu=GPU))
    add("gpu_stale", sources(gpu=dict(GPU, last_probe={**GPU["last_probe"], "probed_at": iso(3 * 3600)})))
    add("gpu_disabled_probe_z", sources(gpu=dict(GPU, enabled=False, last_probe={
        **GPU["last_probe"], "probed_at": "2026-11-14T11:59:00Z"})))
    sys_only = {"enabled": True, "servers": [{"name": "solo", "host": "solo.lab", "tags": ["a"]}],
                "last_probe": {"probed_at": iso(30), "servers": [
                    {"server": "solo", "ok": True, "gpus": [
                        {"index": "1", "name": "", "memory_total_mb": "24000", "memory_used_mb": 2000.5,
                         "utilization_pct": 9.5, "temperature_c": 51}],
                     "system": {"cpu_load_pct": 12.25, "cores": 32, "load1": 1.5}}]},
                "jobs": [{"id": "j", "server": "solo", "gpu_indices": [1], "job": "eval"}]}
    add("gpu_job_no_process_memory", sources(gpu=sys_only))
    project = layer(resources=[
        {"id": "code", "kind": "repository", "name": "code", "details": {"remote": "git@github.com:lab/code.git"}},
        {"id": "data", "kind": "storage", "name": "datasets", "details": {"kind": "nfs"}},
        {"id": "db", "kind": "storage", "name": "results db", "details": {"kind": "database"}},
        {"id": "gpu", "kind": "compute", "name": "lab compute", "details": {"gpu_server": "lab-01"}},
        {"id": "deploy", "kind": "command", "name": "deploy", "details": {}},
    ], locations=[
        {"resource": "project/code", "host_id": "lab-01", "path_or_ref": "/srv/code", "verified_at": iso(60)},
        {"resource": "project/data", "host_id": "nas", "host_label": "NAS", "path_or_ref": "/mnt/data"},
    ])
    add("setup_resources", sources(gpu=GPU, layers={"project": project, "users": []}))
    alice = layer("alice", resources=[{"id": "nb", "kind": "repository", "name": "notebooks", "details": {}}],
                  locations=[{"resource": "u:alice/nb", "host_id": "alice-pc", "path_or_ref": "C:/nb"}],
                  hosts=[{"id": "alice-pc", "label": "Alice laptop"}, {"id": "alice-old", "label": "Old"},
                         {"id": "fp-ark", "label": "server"}])
    history = [{"at": iso(300), "event": "location", "host": "alice-pc"},
               {"at": iso(9 * 86400), "event": "host", "host": "alice-old"}]
    add("host_signals", sources(layers={"project": layer(), "users": [alice]}, history=history))
    project = layer(resources=[{"id": "code", "kind": "repository", "name": "code", "details": {}}],
                    locations=[{"resource": "project/code", "host_id": "lab", "path_or_ref": "/srv/code"}])
    alice = layer("alice", resources=[{"id": "nb", "kind": "repository", "name": "notebooks", "details": {}},
                                      {"id": "slurm", "kind": "compute", "name": "Slurm",
                                       "details": {"kind": "slurm", "gpus": "4", "use": "ask"}}],
                  locations=[{"resource": "u:alice/nb", "host_id": "alice-pc", "path_or_ref": "C:/Users/alice/nb"},
                             {"resource": "project/code", "host_id": "alice-pc", "path_or_ref": "C:/Users/alice/code",
                              "verified_at": iso(100)}],
                  hosts=[{"id": "alice-pc", "label": "Alice laptop"}])
    phones = [{"id": "ph1", "user": "alice", "label": "Android phone", "last_seen_at": NOW - 30, "permission": "full"},
              {"id": "ph2", "user": "bob", "label": "iPhone", "last_seen_at": None, "permission": "read_only"},
              {"id": "ph3", "user": "zed", "last_seen_at": NOW - 7200}]
    data = sources(layers={"project": project, "users": [alice]}, phones=phones,
                   history=[{"at": iso(60), "host": "alice-pc", "event": "location"}])
    for viewer in ("bob", "alice", "", "zed"):
        add(f"privacy_{viewer or 'anon'}", data, viewer)
    add("privacy_shared", {**data, "prefs": {"alice": {"host:alice-pc": {"share_details": True}}}}, "bob")
    hidden = {"alice": {"host:alice-pc": {"hidden": True}, "compute:slurm": {"hidden": 1},
                        "phone:ph1": {"hidden": True, "share_details": True}}}
    add("privacy_hidden_bob", {**data, "prefs": hidden}, "bob")
    add("privacy_hidden_alice", {**data, "prefs": hidden}, "alice")
    add("privacy_phone_shared", {**data, "prefs": {"bob": {"phone:ph2": {"share_details": True}}}}, "alice")
    verify = layer(resources=[{"id": "r", "kind": "repository", "name": "R",
                               "details": {"remote": "https://token@gitlab.example.org/a/b", "role": "primary"}}],
                   locations=[{"resource": "project/r", "host_id": "box", "path_or_ref": "/r",
                               "verified_at": "2026-11-14T11:58:00+00:00"},
                              {"resource": "project/r", "host_id": "LAB-02.example", "path_or_ref": "/x"},
                              {"resource": "project/r", "host_id": "q", "host_label": "lab-03", "path_or_ref": "/y"}],
                   verification={"host": "box2", "at": "2026-11-14T08:00:00"})
    add("verification_and_gpu_hosts", sources(gpu=GPU, layers={"project": verify, "users": []},
                                              history=[{"host": "box2", "at": 1794740000},
                                                       {"host": "box", "at": "bad"}, {"at": iso(5)}]))
    add("no_disk_int_load", sources(server={"hostname": "", "platform": "windows", "fingerprint": "x",
                                            "disk": None, "load1": 2}, sessions=None))
    add("disk_zero_total", sources(server={"hostname": "h", "platform": "linux", "fingerprint": "x",
                                           "disk": {"total": 0, "used": 0, "free": 0}, "load1": 0.123456}))
    add("disk_ints", sources(server={"hostname": "h", "platform": "linux", "fingerprint": "x",
                                     "disk": {"total": 999_653_638_144, "used": 123_456_789_012, "free": 1},
                                     "load1": None}))
    weird = layer(resources=[{"id": "x", "kind": "storage", "name": "", "details": {}},
                             {"id": "", "kind": "environment", "name": "Env 中文", "details": {}},
                             {"id": "t", "kind": "task_space", "name": "!!!", "details": {}}])
    add("unplaced_names", sources(layers={"project": weird, "users": []}))
    return out


TIMES = ["2026-11-14T12:00:00", "2026-11-14T12:00:00Z", "2026-11-14T12:00:00+00:00",
         "2026-11-14T12:00:00.5+01:00", "2026-11-14 12:00", "2026-11-14T12", "2026-11-14T12:", "2026-11-14T12x",
         "2026-11-14", "2026-11-14T12:00:00.123", "2026-11-14T12:00:00.1234", "2026-11-14T12:00:00:123456",
         "2026-11-14T12:00-05:30", "2026-11-14T12:00+24:00", "2026-11-14T12:00+23:59:59.999999",
         "2026-02-30T00:00", "2026-11-14中12:00", "2026-11-14T25:00", "20261114", "x", "", None, 0, 1.5, True,
         "0001-01-01T00:00:00", "9999-12-31T23:59:59.999999", "2026-11-14T12:00:00+0530",
         "2026-11-14T12:00:00-00:00", "2026-11-14T12:00:00Z+01:00", "2026-11-14T12:00:00+00",
         "2026-11-14T12:+00:00", "2026-11-14T12:00:00.5+00:00:00.000001", "2026-11-14T12:00:00+00:00:00.1",
         "2026-11-14T12:00:00.123456789", "2026-11-14T12.000000", "2026-11-14T1200", "1969-12-31T23:59:59.5",
         "2026-11-14T12:00:00+05:30:15", "2024-02-29T00:00", "2023-02-29T00:00", "１２３４-11-14", 1e300]


def write_setup_fixtures(root: Path) -> list[str]:
    if root.exists():
        shutil.rmtree(root)
    cases = []

    def project(name: str) -> Path:
        path = root / name
        (path / "setup" / "users").mkdir(parents=True)
        cases.append(name)
        return path

    def write(path: Path, rel: str, data, raw: bool = False) -> None:
        target = path / "setup" / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        if raw:
            target.write_bytes(data if isinstance(data, bytes) else data.encode("utf-8"))
        else:
            target.write_text(json.dumps(data, ensure_ascii=False, indent=2), encoding="utf-8", newline="\n")

    def user(username: str) -> str:
        return f"users/{setup_layers._user_slug(username)}.json"

    p = project("full")
    write(p, "project.json", {
        "state": "confirmed", "confirmation": {"digest": "nope"},
        "verification": {"host": "alice-pc", "at": "2026-11-14T11:50:00+00:00"},
        "profile": {
            "objective": "x",
            "repositories": [{"id": "Legacy", "path": "/srv/legacy", "remote": "https://bob:pw@github.com/lab/legacy",
                              "role": "PRIMARY"}],
            "compute": [{"id": "lab", "kind": "ssh", "gpu_server": "lab-01", "gpus": 4}],
            "resources": [
                {"id": "code", "kind": "repositories", "name": "Code", "details": {"remote": "git@gitlab.com:a/b.git"}},
                {"id": "data", "kind": "storage", "name": "Data", "details": {"kind": "database"}},
                {"kind": "bogus", "name": "Fallback"},
                {"id": "flat", "type": "compute", "subkind": "slurm", "name": "Cluster", "gpus": "8xA100", "use": "ANY"},
                {"id": "code", "kind": "repository", "name": "dup"},
                {"id": "env", "kind": "environment", "details": {}},
                {"id": "cmd", "kind": "command", "details": {"run": "make"}},
                "not a dict",
            ]},
        "locations": [
            {"resource": "project/code", "host_id": "alice-pc", "path_or_ref": "C:/code", "verified_at": "2026-11-14T11:00:00Z"},
            {"resource": "project/data", "host_id": "bad host!", "path_or_ref": "/d"},
            {"resource": "project/data", "host_id": "nas01", "path": "/mnt/d", "host_label": ""},
            {"resource": "project/legacy", "host_id": " lab-01 ", "path_or_ref": "/srv/legacy"},
            {"resource": "", "host_id": "x", "path_or_ref": "/y"},
            "junk"],
        "hosts": [{"id": "nas01", "label": "NAS"}],
    })
    write(p, user("alice"), {
        "owner": "alice",
        "profile": {"resources": [{"id": "nb", "kind": "repository", "name": "Notebooks", "details": {"role": "data"}},
                                  {"id": "laptop-gpu", "kind": "compute", "name": "Laptop GPU",
                                   "details": {"kind": "local", "gpus": "1"}}]},
        "locations": [{"resource": "u:alice/nb", "host_id": "alice-pc", "path_or_ref": "C:/Users/alice/nb"}],
        "hosts": [{"id": "alice-pc", "label": "Alice laptop"}, {"id": "alice-old"}, {"id": "!bad"}],
    })
    write(p, user("Bob"), {"owner": "Bob", "hosts": [{"id": "bob-box", "label": "Bob box"}],
                           "verification": {"host": "bob-box", "at": "2026-11-14T11:59:30"}})
    write(p, user("carol"), {"owner": "dave", "hosts": [{"id": "carol-pc"}]})
    write(p, "users/zz-stale.json", "{not json", raw=True)
    write(p, "users/notes.txt", "ignored", raw=True)
    lines = ['{"at": "2026-11-14T11:55:00+00:00", "event": "location", "host": "alice-pc"}',
             '', 'garbage', '[1, 2]', '\ufeff{"host": "bob-box", "at": "2026-11-14T11:59:00"}',
             '{"host": "alice-old", "at": "2026-11-01T00:00:00Z"}', '{"host": "nas01", "at": 1794700000.5}']
    write(p, "history.jsonl", "\r\n".join(lines) + "\r\n", raw=True)

    p = project("empty")
    p = project("stale_project")
    write(p, "project.json", b"\xef\xbb\xbf{}", raw=True)
    write(p, "history.jsonl", "\n".join(json.dumps({"host": f"h{i % 3}", "at": NOW - 4000 + i},
                                                   separators=(",", ":")) for i in range(2003)), raw=True)
    return cases


def main() -> None:
    golden = {"times": [], "graphs": []}
    for value in TIMES:
        ts = rg._parse_time(value)
        golden["times"].append({"value": value, "ts": ts, "iso": rg._iso(ts) if ts is not None and
                                0 < ts < 253402300799 else None})
    golden["ago"] = [[s, rg._ago(s)] for s in (0, 89.9, 90, 149.9, 150, 5399, 5400, 9000, 172799, 172800, 1e7)]
    golden["git_host"] = [[r, rg.git_host(r)] for r in (
        "git@github.com:lab/x.git", "https://token@gitlab.example.org/a/b", "/local/path", " ssh://git@host.io/x ",
        "github.com/a", "https://a@b@c.org/x", "git+ssh://x.y.zz:22/r", "C:/repo", "", None, "HTTPS://Example.COM/x")]
    for name, src, viewer in unit_cases():
        golden["graphs"].append({"name": name, "viewer": viewer, "sources": src,
                                 "graph": json.dumps(rg.build_graph(src, viewer), ensure_ascii=False)})
    root = HERE / "setup"
    for case in write_setup_fixtures(root):
        path = root / case
        layers = setup_layers.read_layers(path)
        history = setup_layers.recent_history(path)
        src = sources(gpu=GPU, layers=layers, history=history,
                      users={"alice": {"username": "alice", "display_name": "Alice"},
                             "Bob": {"username": "Bob", "display_name": "Bob B."}})
        for viewer in ("alice", "Bob", "carol"):
            golden["graphs"].append({"name": f"setup_{case}_{viewer}", "viewer": viewer, "setup": case,
                                     "sources": {k: v for k, v in src.items() if k not in ("layers", "history")},
                                     "graph": json.dumps(rg.build_graph(src, viewer), ensure_ascii=False)})
    (HERE / "golden.json").write_text(json.dumps(golden, ensure_ascii=False, indent=1) + "\n",
                                      encoding="utf-8", newline="\n")


if __name__ == "__main__":
    main()
