"""GPU module of the services replay (replay_services.py ``--only gpu``).

No real server is ever contacted: prepare_env() points ARK_GPU_SSH at a fake
``ssh`` (fakessh/fakessh.go, built with the Go toolchain) that answers the
probe, system, allocation and queue commands by target host, keeps a fake
remote job state per server and logs every argv.  The server list is written
into both data dirs (``private/gpu_servers.yaml``) by CALL steps between the
requests.  The queue worker thread does not run in a replay; a request with
``X-Replay-GpuTick`` runs one tick first (replayclock.py, and the Go server
under ARK_REPLAY_CLOCK=1), so placement happens at the same step on both
servers.  ``X-Replay-ClockOffset`` moves the replay clock of one step forward
(the queue's runtime guard).  After the replay the fake's argv lists and job
states of both servers are compared (fake_ssh_problems).
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

GPU = "gpu"
CALL = "__call__"  # (CALL, "", "", fn, headers): diffreplay.py calls fn(py_dir, go_dir)
FAKE_SSH_SOURCE = Path(__file__).resolve().parent / "fakessh" / "fakessh.go"
_FAKE_SSH: list = []


def fake_ssh() -> tuple[str, str]:
    """Build the fake ssh once: (binary, state dir)."""
    if not _FAKE_SSH:
        work = Path(tempfile.mkdtemp(prefix="ark-fake-ssh-"))
        binary = work / ("ssh.exe" if os.name == "nt" else "ssh")
        go = os.environ.get("ARK_REPLAY_GO") or shutil.which("go") or ""
        if go:
            subprocess.run([go, "build", "-o", str(binary), FAKE_SSH_SOURCE.name],
                           cwd=str(FAKE_SSH_SOURCE.parent), check=True, capture_output=True,
                           env={**os.environ, "CGO_ENABLED": "0"})
        # Without a Go toolchain the path stays missing: both servers then
        # report missing_ssh, and no real ssh is ever started.
        _FAKE_SSH.append((str(binary), str(work / "state")))
    return _FAKE_SSH[0]


def prepare_env() -> None:
    """The default server list under each data dir's private/, the fake ssh."""
    for name in ("ARK_GPU_SERVERS_CONFIG", "ARK_GPU_QUEUE_DB", "ARK_GPU_PROBE"):
        os.environ.pop(name, None)
    binary, state = fake_ssh()
    os.environ["ARK_GPU_SSH"] = binary
    os.environ["ARK_FAKE_SSH_DIR"] = state


def fake_ssh_problems() -> list:
    """Every argv both servers sent (sorted per server: probes run in
    parallel) and the fake remote job state, compared."""
    if not _FAKE_SSH:
        return []
    state = Path(_FAKE_SSH[0][1])
    sides = sorted(state.iterdir()) if state.is_dir() else []
    if not sides:
        return []
    if len(sides) != 2:
        return [f"fake ssh: expected two servers, got {[p.name for p in sides]}"]

    import go_only  # the Go system query's df section is Go-only (go_only.GO_ONLY_SSH_SUFFIX)

    def load(side: Path):
        calls = sorted(json.dumps(go_only.ssh_call_canonical(json.loads(p.read_text(encoding="utf-8"))[1:]),
                                  ensure_ascii=False)
                       for p in (side / "log").glob("*.json"))
        jobs = {p.name: json.loads(p.read_text(encoding="utf-8")) for p in (side / "jobs").glob("*.json")}
        return calls, jobs

    (ca, ja), (cb, jb) = load(sides[0]), load(sides[1])
    out = []
    if ca != cb:
        only_a = [c for c in ca if c not in cb][:3]
        only_b = [c for c in cb if c not in ca][:3]
        out.append(f"fake ssh calls differ ({len(ca)} vs {len(cb)}): {sides[0].name} only {only_a!r}; "
                   f"{sides[1].name} only {only_b!r}")
    if ja != jb:
        out.append(f"fake ssh job state differs: {ja!r} != {jb!r}")
    return out


def fake_ssh_calls() -> int:
    if not _FAKE_SSH or not Path(_FAKE_SSH[0][1]).is_dir():
        return 0
    return sum(len(list((side / "log").glob("*.json"))) for side in Path(_FAKE_SSH[0][1]).iterdir())


PROBE_CONFIG = """# replay: fake hosts only (fakessh.go)
enabled: true
defaults:
  port: 2222
  user: replay
  timeout_seconds: 8
servers:
  - name: a
    host: a.fake
    tags: [4090, primary, 4090]
    note: "两张 4090 \\"quoted\\""
  - name: b
    host: b.fake
    port: 22
    user: ''
    tags: [1.5, h20]
  - name: odd
    host: odd.fake
    timeout_seconds: 3
  - {servers}
  - name: off1
    host: off1.fake
    enabled: false
"""
MORE_HOSTS = ["warn", "empty", "auth", "unreach", "kex", "fail", "fail2", "badutf8", "refused"]

QUEUE_CONFIG = """enabled: true
defaults:
  timeout_seconds: 5
servers:
  - name: q1
    host: q1.fake
    tags: [a100, main]
  - name: q2
    host: q2.fake
    tags: [h20]
  - name: q3
    host: q3.fake
    tags: [h20]
  - name: qoff
    host: q1.fake
    enabled: false
queue:
  enabled: {enabled}
  poll_seconds: 300
  state_dir: /tmp/ark-gpu-queue/
  max_jobs_per_gpu: {per_gpu}
  min_free_memory_mb: 4096
  max_utilization_pct: 10
  allow_colocation: {coloc}
  setup: "source /data/venv/bin/activate && echo 'ready'"
  env:
    HF_HOME: /data/hf
    FLAG: true
    N: 3
  path_map:
    - from: /data
      to: /mnt/data/
    - from: /data/sub
      to: /scratch
      servers: [q2]
"""


def config(text) -> tuple:
    """A CALL step writing (None: removing) both servers' GPU server list."""
    def write(*dirs):
        for d in dirs:
            target = Path(d) / "private" / "gpu_servers.yaml"
            if text is None:
                target.unlink(missing_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(text if isinstance(text, bytes) else text.encode("utf-8"))
    return (CALL, "", "", write, {"X-Replay-Module": GPU})


def probe_config() -> str:
    more = "\n  - ".join(f"name: {n}\n    host: {n}.fake" for n in MORE_HOSTS)
    return PROBE_CONFIG.replace("{servers}", more)


def queue_config(enabled="true", per_gpu=2, coloc="true") -> str:
    return (QUEUE_CONFIG.replace("{enabled}", str(enabled)).replace("{per_gpu}", str(per_gpu))
            .replace("{coloc}", coloc))


INVALID_CONFIGS = [
    "", "# only a comment\n", "enabled: true\n\tservers: []\n", "---\nenabled: true\n", "- a\n- b\n",
    "enabled: true\nservers:\n  - name: a\n    host: a.fake\n    password: x\n",
    "enabled: true\nenabled: false\n", "enabled: yes\n", "bogus: 1\n",
    "servers:\n  - name: a\n    host: 'user@a.fake'\n", "servers: &x []\n",
    "servers:\n  - name: a\n    host: a.fake\n  - name: a\n    host: b.fake\n",
    "defaults:\n  port: 70000\n", "queue:\n  state_dir: relative/dir\n",
    "servers:\n  - name: a\n    host: a.fake\n    tags: [\"unterminated]\n",
    b"\xef\xbb\xbfenabled: false\n", b"enabled: \xff\n", "﻿enabled: true\nservers: []\n",
]


def gpu_steps(fixture: dict) -> list:
    """GPU status, probe, monitoring, dispatch builder and queue."""
    h = {"X-Replay-Module": GPU}
    status, servers, queue = "exact:/api/gpu/status", "exact:/api/gpu/servers", "exact:/api/gpu/queue"
    dash, jobs, probe = "exact:/api/gpu/dashboard", "exact:/api/gpu/jobs", "exact:/api/gpu/probe"
    prep = "exact:/api/gpu/dispatch/prepare"
    overview = ("exact:/api/project/overview", "GET", "/api/project/overview", None, h)
    # The Resources graph reads the GPU service (servers, last probe, queue jobs).
    graph = ("exact:/api/resources/graph", "GET", "/api/resources/graph", None, h)

    def reads():
        return [(status, "GET", "/api/gpu/status", None, h), (servers, "GET", "/api/gpu/servers", None, h),
                (queue, "GET", "/api/gpu/queue", None, h)]

    steps = [config(None)] + reads() + [
        (dash, "GET", "/api/gpu/dashboard", None, h), (probe, "POST", "/api/gpu/probe", {}, h),
        (prep, "POST", "/api/gpu/dispatch/prepare", {"command": "x"}, h), overview,
        (jobs, "GET", "/api/gpu/jobs", None, h), (queue, "GET", "/api/gpu/queue", None, h)]
    for text in INVALID_CONFIGS:
        steps += [config(text)] + reads() + [(probe, "POST", "/api/gpu/probe", {}, h)]
    steps += [config(probe_config().replace("enabled: true", "enabled: false", 1))] + reads()
    steps += [(probe, "POST", "/api/gpu/probe", {}, h), (dash, "GET", "/api/gpu/dashboard", None, h)]
    # Enabled: probe the fake fleet.
    steps += [config(probe_config())] + reads() + [graph]
    bodies = [{}, {"server": "a"}, {"servers": "a, b"}, {"servers": ["a", "a", " b ", 5]}, {"servers": []},
              {"servers": ""}, {"servers": None, "server": "odd"}, {"servers": 5}, {"servers": {"a": 1}},
              {"servers": True}, {"servers": ["zzz", "a", "yyy"]}, {"server": "off1"}, {"servers": "off1,a"},
              {"server": "a", "timeout_seconds": "5"}, {"server": "a", "timeout_seconds": 2.9},
              {"server": "a", "timeout_seconds": True}, {"server": "a", "timeout_seconds": "x"},
              {"server": "a", "timeout_seconds": 1}, {"server": "a", "timeout_seconds": 31},
              {"server": "a", "timeout_seconds": None}, {"server": "a", "timeout_seconds": ""},
              {"server": "a", "timeout_seconds": [1]}, {"server": "a", "timeout_seconds": " 30 "},
              {"server": "a", "timeout_seconds": "１０"}, '{"server": "a", "timeout_seconds": 1e400}',
              '{"server": "a", "timeout_seconds": NaN}', "[1]", "{"]
    for body in bodies:
        steps.append((probe, "POST", "/api/gpu/probe", body, h))
    steps.append((probe, "POST", "/api/gpu/probe", {}, {**h, "Origin": "http://evil.example"}))
    steps += [(dash, "GET", "/api/gpu/dashboard", None, h),
              (dash, "GET", "/api/gpu/dashboard", None, {**h, "Accept-Encoding": "gzip"}), overview, graph]
    x = {"server": "a", "command": "x"}
    dispatch = [{}, {"command": "python train.py"}, {"server": "a"},
                {"server": "a", "command": "python train.py --lr 1e-3", "gpus": "0, 1", "env": {"B": "x y", "A": 1},
                 "work_dir": "/data/my project", "job": "train-1", "project": "proj.x", "timeout_minutes": "90"},
                {"server": "b", "command": "echo 'hi' \"there\" $HOME 中文\n\tsecond", "gpu_indices": [3, "2"],
                 "env": {"FLAG": True, "F": 0.5, "Z": False}},
                {**x, "execute": True}, {**x, "launch": 1}, {**x, "gpus": "0,0"}, {**x, "gpus": "16"},
                {**x, "gpus": [True]}, {**x, "gpus": {"a": 1}}, {**x, "gpus": list(range(9))}, {**x, "gpus": "a,b"},
                {**x, "gpus": [1.7]}, {**x, "timeout_minutes": 0}, {**x, "timeout_minutes": "abc"},
                {**x, "timeout_minutes": None}, {**x, "timeout_minutes": 1441}, {**x, "job": "bad job"},
                {**x, "project": "p" * 65}, {**x, "work_dir": "rel"}, {**x, "work_dir": "/a/../b"},
                {**x, "work_dir": 5}, {**x, "env": {"1BAD": "x"}}, {**x, "env": {"A": [1]}},
                {**x, "env": {f"V{i}": "x" for i in range(17)}}, {**x, "env": {"A": "x" * 201}},
                {**x, "env": "A=1"}, {"server": "a", "command": "  "}, {"server": "a", "command": "a\x00b"},
                {"server": "a", "command": "a\x07b"}, {"server": "a", "command": 5}, {"server": "off1", "command": "x"},
                {"server": "zzz", "command": "x"}, {"server": ["a"], "command": "x"},
                {"server": "a", "command": "x" * 16385}, "[]"]
    for body in dispatch:
        steps.append((prep, "POST", "/api/gpu/dispatch/prepare", body, h))
    # Slow, huge and broken hosts, one at a time.
    steps.append(config("enabled: true\nservers:\n  - name: slow\n    host: slow.fake\n    timeout_seconds: 2\n"
                        "  - name: big\n    host: big.fake\n  - name: nan\n    host: nan.fake\n"
                        "  - name: inf\n    host: inf.fake\n"))
    for name in ("slow", "big", "nan", "inf"):
        steps.append((probe, "POST", "/api/gpu/probe", {"server": name}, h))
    steps.append((prep, "POST", "/api/gpu/dispatch/prepare", {"server": "big", "command": "x"}, h))
    steps += [config("enabled: true\nservers:\n  - name: solo\n    host: a.fake\n    user: ubuntu\n"),
              (prep, "POST", "/api/gpu/dispatch/prepare", {"command": "run it"}, h),
              (probe, "POST", "/api/gpu/probe", {"servers": ["solo"]}, h)]
    steps += [config("enabled: true\nservers: []\n"), (probe, "POST", "/api/gpu/probe", {}, h),
              (prep, "POST", "/api/gpu/dispatch/prepare", {"command": "x"}, h)]
    return steps + queue_steps(fixture, h)


def queue_steps(fixture: dict, h: dict) -> list:
    tick = {**h, "X-Replay-GpuTick": "1"}
    jobs, queue = "exact:/api/gpu/jobs", "exact:/api/gpu/queue"
    job_r, logs_r = r"regex:^/api/gpu/jobs/([^/]+)$", r"regex:^/api/gpu/jobs/([^/]+)/logs$"
    cancel_r = r"regex:^/api/gpu/jobs/([^/]+)/cancel$"
    tokens = fixture["tokens"]
    nocookie = {**h, "X-Replay-NoCookie": "1"}
    agent = {**nocookie, "Authorization": "Bearer " + tokens["server"]}
    full = {**nocookie, "Authorization": "Bearer " + tokens["full_token"]}
    read = {**nocookie, "Authorization": "Bearer " + tokens["read_token"]}
    a, t = "/api/agent/v1/gpu/jobs", "/api/task-agent/v1/gpu/jobs"
    a_r, ac_r = r"regex:^/api/agent/v1/gpu/jobs/([^/]+)(/logs)?$", r"regex:^/api/agent/v1/gpu/jobs/([^/]+)/cancel$"
    t_r, tl_r = r"regex:^/api/task-agent/v1/gpu/jobs/([^/]+)$", r"regex:^/api/task-agent/v1/gpu/jobs/([^/]+)/logs$"
    tc_r = r"regex:^/api/task-agent/v1/gpu/jobs/([^/]+)/cancel$"

    def post(body, headers=h, path="/api/gpu/jobs", route=jobs):
        return (route, "POST", path, body, headers)

    def get(path, route=jobs, headers=h):
        return (route, "GET", path, None, headers)

    steps = [config(queue_config(enabled="false")), get("/api/gpu/queue", queue),
             post({"command": "x"}), get("/api/gpu/jobs"),
             config(queue_config()), get("/api/gpu/queue", queue), get("/api/gpu/jobs")]
    x = {"command": "x"}
    for body in ({}, {"command": ""}, {**x, "work_dir": "rel"}, {**x, "env": {"bad name": 1}}, {**x, "job": "a b"},
                 {**x, "gpu_count": 9}, {**x, "gpu_count": True}, {**x, "gpus": "abc"},
                 {**x, "gpu_count": 2.5, "job": "fractional", "min_free_memory_mb": 10 ** 9},
                 '{"command": "x", "min_free_memory_mb": 1e999}', {**x, "max_utilization_pct": 101},
                 {**x, "timeout_minutes": 0}, {**x, "server": "nope"}, {**x, "tags": "a100"},
                 {**x, "gpu_count": [1]}, {**x, "memory_mb": "-1"}, "[1]"):
        steps.append(post(body))
    steps.append(post(x, {**h, "Origin": "http://evil.example"}))
    submits = [
        {"command": "python train.py --data /data/sets replay:run=1", "work_dir": "/data/sub/proj",
         "env": {"DATA": "/data/sets", "B": True, "C": 1.5, "HF_HOME": "/data/own"}, "min_free_memory_mb": 6000,
         "job": "train-a", "tags": ["a100", " ", 7], "session": "s-1", "task": "T-0001", "project": "ignored"},
        {"command": "torchrun x.py replay:run=2 replay:mem=2500", "gpu_count": 2, "memory_mb": 4000, "job": "train-b"},
        {"command": "python fail.py replay:exit=3", "job": "fail-3", "gpus": 1, "task_ref": "T-9"},
        {"command": "python lost.py replay:lost", "job": "lost-1"},
        {"command": "echo replay:launchfail", "job": "launchfail"},
        {"command": "echo replay:nopid", "job": "nopid"},
        {"command": "python h20.py replay:run=1", "tags": ["h20"], "max_utilization_pct": 60, "job": "h20-a",
         "work_dir": "/data/sub/x"},
        {"command": "sleep replay:run=99", "timeout_minutes": 1, "job": "timeout-1"},
        {"command": "x replay:run=99", "server": "q1", "gpu_count": 8, "job": "too-big"},
        {"command": "x", "min_free_memory_mb": 1000000, "job": "cancel-queued", "owner": "spoofed"},
        {"command": "long replay:run=99 replay:mem=3000", "job": "cancel-me", "gpu_memory_mb": 1000},
    ]
    steps += [post(body) for body in submits[:6]]
    steps += [get("/api/gpu/jobs"), get("/api/gpu/queue", queue, tick), get("/api/gpu/jobs?limit=50")]
    steps += [post(body) for body in submits[6:]]
    steps += [get("/api/gpu/queue", queue, tick), get("/api/gpu/jobs"),
              ("exact:/api/resources/graph", "GET", "/api/resources/graph", None, h)]
    for name in ("train-a", "fail-3", "launchfail", "cancel-me"):
        steps += [get(f"/api/gpu/jobs/@gpujob_{name}@", job_r),
                  get(f"/api/gpu/jobs/@gpujob_{name}@/logs?tail=5", logs_r)]
    for q in ("tail=0", "tail=abc", "tail=500", "tail=1&tail=2", "tail=%EF%BC%95"):
        steps.append(get(f"/api/gpu/jobs/@gpujob_train-a@/logs?{q}", logs_r))
    steps += [get("/api/gpu/jobs/gpu_nope", job_r), get("/api/gpu/jobs/gpu_nope/logs", logs_r),
              post({}, h, "/api/gpu/jobs/@gpujob_cancel-me@/cancel", cancel_r),
              post({}, h, "/api/gpu/jobs/@gpujob_cancel-queued@/cancel", cancel_r),
              post({}, h, "/api/gpu/jobs/@gpujob_cancel-queued@/cancel", cancel_r),
              post({}, h, "/api/gpu/jobs/gpu_nope/cancel", cancel_r)]
    for _ in range(3):
        steps += [get("/api/gpu/queue", queue, tick), get("/api/gpu/jobs?limit=500")]
    steps += [get("/api/gpu/queue", queue, {**tick, "X-Replay-ClockOffset": "600"}), get("/api/gpu/jobs")]
    filters = ["status=queued,running", "status=queued&status=errored", "status=bogus", "status=,", "status=",
               "name=train-a", "name=a&name=b", "name_prefix=train", "name_prefix=bad*", "name_prefix=" + "x" * 65,
               "since=2000-01-01T00:00:00Z", "since=2000-01-01", "since=garbage",
               "since=2000-01-01T00:00:00.5%2B00:00", "since=2000-01-01T00:00:00.123%2B05:30",
               "since=2999-01-01T00:00:00%2B00:00", "since=2000-01-01T24:00:00Z", "since=2000-01-01T12%2B00:00",
               "since=2000-01-01T12:30x", "since=2000-01-01%2000:00:00-23:59", "since=2000-02-30T00:00:00Z",
               "server=q1", "server=q2", "ids=@gpujob_train-a@,@gpujob_fail-3@", "ids=bad", "ids=", "ids=,",
               "limit=1&offset=1", "limit=2&offset=2", "limit=0", "limit=501", "limit=abc", "limit=1&limit=2",
               "offset=-1", "offset=1000001", "limit=%EF%BC%91", "limit=0001"]
    steps += [get("/api/gpu/jobs?" + q) for q in filters]
    # Agent API (server token) and task-agent sessions (full access / read only / none).
    steps += [("exact:" + a, "GET", a, None, agent), ("exact:" + a, "GET", a + "?status=errored", None, agent),
              (a_r, "GET", a + "/@gpujob_train-a@", None, agent),
              (a_r, "GET", a + "/@gpujob_fail-3@/logs?tail=3", None, agent), (a_r, "GET", a + "/gpu_nope", None, agent),
              ("exact:" + a, "POST", a, {"command": "agent replay:run=1", "job": "agent-job", "session": "s-agent"}, agent),
              (ac_r, "POST", a + "/@gpujob_agent-job@/cancel", {}, agent), (ac_r, "POST", a + "/gpu_nope/cancel", {}, agent)]
    te = "exact:" + t
    steps += [(te, "GET", t, None, full), (te, "GET", t, None, read), (te, "GET", t, None, nocookie),
              (te, "POST", t, {"command": "task replay:run=1", "job": "task-job", "task": "ignored"}, full),
              (te, "POST", t, {"command": "task"}, read), (te, "POST", t, {"command": "task"}, nocookie),
              (te, "POST", t, "[1]", full), (te, "POST", t, {"command": ""}, full),
              (te, "GET", t, None, full), (te, "GET", t + "?status=queued", None, full),
              (t_r, "GET", t + "/@gpujob_task-job@", None, full), (tl_r, "GET", t + "/@gpujob_task-job@/logs", None, full),
              (t_r, "GET", t + "/@gpujob_train-a@", None, full), (t_r, "GET", t + "/gpu_nope", None, full),
              (te, "GET", "/api/task-agent/v1/gpu/other/x", None, full),
              (tc_r, "POST", t + "/@gpujob_train-a@/cancel", {}, full),
              (tc_r, "POST", t + "/@gpujob_task-job@/cancel", {}, read),
              (tc_r, "POST", t + "/@gpujob_task-job@/cancel", {}, full),
              (tc_r, "POST", t + "/@gpujob_task-job@/cancel", {}, {**full, "Origin": "http://evil.example"})]
    # Colocation off and one job per GPU, a broken queue config, then the queue switched off.
    steps += [config(queue_config(per_gpu=1, coloc="false")), get("/api/gpu/queue", queue)]
    steps += [post({"command": f"coloc replay:run=5 n{i}", "job": f"coloc-{i}", "min_free_memory_mb": 100,
                    "max_utilization_pct": 100}) for i in range(3)]
    steps += [get("/api/gpu/queue", queue, tick), get("/api/gpu/jobs?name_prefix=coloc"),
              get("/api/gpu/queue", queue, tick), get("/api/gpu/jobs?status=running,queued"),
              ("exact:/api/project/overview", "GET", "/api/project/overview", None, h),
              ("exact:/api/gpu/dashboard", "GET", "/api/gpu/dashboard", None, h)]
    steps += [config("enabled: true\nservers: []\nqueue:\n  enabled: true\n  poll_seconds: 0\n"),
              get("/api/gpu/jobs"), get("/api/gpu/queue", queue, tick), get("/api/gpu/jobs/@gpujob_train-a@", job_r),
              get("/api/gpu/jobs/@gpujob_train-a@/logs", logs_r),
              post({}, h, "/api/gpu/jobs/@gpujob_coloc-2@/cancel", cancel_r)]
    steps += [config(queue_config(enabled="false")), post(x), get("/api/gpu/jobs"),
              get("/api/gpu/queue", queue, tick), get("/api/gpu/jobs?status=queued"),
              ("exact:/api/project/overview", "GET", "/api/project/overview", None, h)]
    return steps
