"""Golden replay: the differential replay's request sequences against ark-server ALONE.

    python -B tests/python/contract/golden.py --go ark_studio/server/ark-server.exe            # check (local + auth)
    python -B tests/python/contract/golden.py --go ark_studio/server/ark-server.exe --record   # re-record the goldens
    python -B tests/python/contract/golden.py --go ... --mode local --only unit-writes

The Python server is frozen, so the primary end-to-end check of ark-server must
not need it.  golden.py starts one ark-server on a recorded fixture
(fixtures/<mode>.zip, record_fixtures.py), sends the same read AND write steps
diffreplay.py sends (replay_views, replay_writes, replay_sessions, ...), and
compares every reply in diffreplay's canonical form (status, compared headers,
normalized body; normalizations N1-N21, see diffreplay.py, plus G1-G3 in
run_mask: work folder, host fingerprint, loopback ports; G4 in fixed_temp_base:
temporary folders under a base path of fixed length) with
goldens/<mode>.json.gz.  After the run the data dir is compared with the
recorded state dump (statediff.py, S1-S12).  ``import arkstudio`` is blocked
(noarkstudio.py); step groups whose builders need the Python code (the
environment-setup steps) are skipped and listed, and so are a module's steps
after a CALL step that needs it (the confirmed-setup block of the skills
module) until that module's next CALL that works.

Recording runs the sequence twice on fresh copies and keeps a step's form
only when both runs agree; steps whose replies depend on timing (live agent
sessions, terminal output) are stored as "unstable" and reported, not
compared.  The goldens were first recorded from a build that matched the
Python reference with 0 mismatches on every diffreplay mode, so they encode
the Python-compatible behaviour.

When Go behaviour changes on purpose: change the Go code with a Go unit test,
run golden.py (it lists the steps that changed), check that only the intended
steps changed, then re-record with --record and commit the new goldens with
the change.  Re-record also after fixture.py / record_fixtures.py changes.
Differences from diffreplay, by design: no Python server; WAIT/HOOK/CALL/file
steps act on the one Go data dir (their Python-side half goes to a scratch
copy); probe HEAD steps are compared as plain replies (no HEAD-vs-GET resend);
the outbound fake-endpoint request log is not compared.
"""
from __future__ import annotations

import argparse
import gzip
import json
import os
import shutil
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import noarkstudio  # noqa: E402

noarkstudio.install()

import diffreplay as dr  # noqa: E402
import replay_agents  # noqa: E402
import replay_gpu  # noqa: E402
import replay_main_sync  # noqa: E402
import replay_probe  # noqa: E402
import replay_services  # noqa: E402
import replay_sessionctx  # noqa: E402
import replay_sessions  # noqa: E402
import replay_settings  # noqa: E402
import replay_setup  # noqa: E402
import replay_skills  # noqa: E402
import replay_writes  # noqa: E402
from replay_views import agent_steps, fixture_ids, view_steps  # noqa: E402
from serverctl import Client, Server, copy_fixture, recorded_fixture, temp_dir  # noqa: E402
from statediff import diff as state_diff, dump as state_dump  # noqa: E402

GOLDENS = HERE / "goldens"
MODES = ("local", "auth")
UNSTABLE = "unstable"
CLOCK0 = datetime(2030, 1, 1, tzinfo=timezone.utc).timestamp()
INFOHUB_NOW = 1794268800  # 2026-11-10T00:00:00Z: the InfoHub test databases' "now"
BIG = 2048  # bodies longer than this are stored as their SHA-256 plus a head
HASHED = "sha256:"
BIG_STATE = 4096  # state entries longer than this (JSON) are stored as their SHA-256


def stored_body(body: str) -> str:
    import hashlib
    if len(body) <= BIG:
        return body
    raw = body.encode("utf-8", "surrogateescape")
    return HASHED + hashlib.sha256(raw).hexdigest() + "\n" + body[:600]


def stored_state(value):
    """A state-dump entry, or its SHA-256 when its JSON is longer than BIG_STATE."""
    import hashlib
    text = json.dumps(value, sort_keys=True)
    if len(text) <= BIG_STATE:
        return value
    return {HASHED: hashlib.sha256(text.encode()).hexdigest()}


def unhash(golden_body: str, got_body: str) -> str:
    """The current body in the golden's form (hashed when the golden is)."""
    if golden_body.startswith(HASHED) and len(got_body) > BIG:
        return stored_body(got_body)
    return got_body


def run_mask(work: Path, client) -> "callable":
    """G1-G3, on top of diffreplay's N1-N21 (applied to the JSON text of a
    canonical reply, a step path and the state dump): G1 this run's work
    folder (fake home, InfoHub databases, setup repositories) and the fake
    ssh folder (raw, JSON- and URL-escaped, forward slashes) -> <WORK>; G2 this machine's setup host
    fingerprint (hostname | system | the fake home) -> <HOST>; G3 any other
    loopback port (the fake model endpoint) -> 127.0.0.1:<PORT>."""
    import re
    from urllib.parse import quote
    variants = set()
    # the work folder and the fake ssh folder (built once per process, replay_gpu)
    for folder in (Path(work).resolve(), Path(os.environ.get("ARK_GPU_SSH", work)).resolve().parent):
        raw = str(folder)
        variants |= {raw, raw.replace("\\", "/"), quote(raw, safe=""), quote(raw), quote(raw.replace("\\", "/"), safe="")}
    for _ in range(3):
        variants |= {json.dumps(v)[1:-1] for v in variants}
    ordered = sorted(variants, key=len, reverse=True)
    host = ""
    try:
        found = re.search(r'"host": \{"id": "([0-9a-f]{12})"', client.get("/api/skills/setup/state").text)
        host = found.group(1) if found else ""
    except OSError:
        pass

    def mask(text: str) -> str:
        for v in ordered:
            text = text.replace(v, "<WORK>")
        if host:
            text = text.replace(host, "<HOST>").replace("host-" + host[:6], "host-<HOST>")
        return re.sub(r"(127\.0\.0\.1|localhost)(:|%3A)\d+", r"\1\2<PORT>", text)
    return mask


def build_steps(fixture: dict, units: list, auth: bool) -> tuple[list, list[str]]:
    """diffreplay.replay's sequence; groups whose builder needs arkstudio are skipped."""
    ids = fixture_ids(Path(fixture["data_dir"]))
    skipped: list[str] = []

    def group(name, fn):
        try:
            return fn()
        except ImportError as exc:
            skipped.append(f"{name} ({exc})")
            return []

    refs = replay_services._refs(Path(fixture["data_dir"]))
    views = group("views", lambda: view_steps(fixture, ids, auth))
    relogin = [(dr.RELOGIN, "", "", None, {})]
    services = []
    for name, fn in (("assistant", lambda: replay_services.assistant_steps(refs)),
                     ("infohub", lambda: replay_services.infohub_steps(fixture)),
                     ("open", lambda: replay_services.open_steps(fixture)),
                     ("chat", lambda: replay_services.chat_steps(fixture)),
                     ("avatar", lambda: replay_services.avatar_steps(fixture)),
                     ("gpu", lambda: replay_gpu.gpu_steps(fixture)),
                     ("setup", lambda: replay_setup.setup_steps(fixture))):
        services += group(name, fn)
    steps = (group("agent-writes", lambda: replay_writes.agent_write_steps(fixture)) + dr.read_steps(units)
             + views + group("agent", lambda: agent_steps(fixture, ids, units, auth)) + relogin
             + dr.write_steps(units)
             + group("writes", lambda: replay_writes.write_steps(fixture))
             + group("main-sync", lambda: replay_main_sync.steps(fixture))
             + services
             + group("sessions", lambda: replay_sessions.session_steps(fixture))
             + group("session-contexts", lambda: replay_sessionctx.ctx_steps(fixture))
             + group("settings", lambda: replay_settings.settings_steps(fixture))
             + group("skills", lambda: replay_skills.skills_steps(fixture))
             + relogin + dr.read_steps(units) + views
             + group("probe", lambda: replay_probe.probe_steps(fixture, units[0]["uid"] if units else "x")))
    return steps, skipped


def run_once(go: str, mode: str) -> dict:
    """One Go-only pass: canonical replies per compared step and the state dump."""
    replay_services.prepare_env()
    if not Path(os.environ.get("ARK_GPU_SSH", "")).is_file():
        # Without it the GPU replies say "no ssh" and differ from the goldens.
        raise SystemExit("golden.py needs the Go toolchain on PATH (or ARK_REPLAY_GO) to build the fake ssh "
                         "(tests/python/contract/fakessh)")
    # The fake ssh keeps its remote job state in one folder per process: start
    # every run from an empty one, as a fresh diffreplay process does.
    state = Path(os.environ["ARK_FAKE_SSH_DIR"])
    shutil.rmtree(state, ignore_errors=True)
    state.mkdir(parents=True, exist_ok=True)
    replay_services.INFOHUB_NOW = INFOHUB_NOW
    work = temp_dir("ark-golden-")
    out = {"forms": {}, "skipped_groups": [], "steps": 0, "blocked_calls": 0, "blocked_steps": 0}
    try:
        fixture = recorded_fixture(work / "fixture", mode)
        if mode == "local":
            dr.downgrade_v10(Path(fixture["data_dir"]))  # both servers migrate at startup, as in diffreplay
        time.sleep(1.1)
        start = datetime.now(timezone.utc).isoformat(timespec="seconds")
        go_dir = copy_fixture(Path(fixture["data_dir"]), work / "go")
        scratch = copy_fixture(Path(fixture["data_dir"]), work / "scratch")  # Python-side half of file steps
        env = {"ARK_ALLOWED_HOSTS": "contract.example", "ARK_REPLAY_CLOCK": "1",
               "ARK_BLACKBOX": "1", "ARK_BLACKBOX_INTERVAL": str(10 ** 12), "ARK_RG": "none",
               **replay_agents.agent_env(work), **replay_sessions.session_env(work), **replay_sessionctx.ctx_env()}
        # A fixed replay clock (N8): ids and stamps derived from it (session
        # ids, change-log stamps, named random streams) repeat run after run.
        # It lies after any wall-clock start, so N4 still masks its stamps.
        clock0 = CLOCK0
        auth = mode == "auth"
        with Server(go, go_dir, env=env) as gs:
            cg = Client(gs)
            ported = dr.go_ported(go)
            dr.REPLAY_AS.clear()
            user = None
            if auth:
                dr.REPLAY_AS.update(users=fixture["users"], password=fixture["password"])
                user = fixture["users"][0]
                cg.login(user, fixture["password"])
            norm = dr.Normalizer(go_dir, start, gs.port)
            mask = run_mask(work, cg)
            units = dr.unit_refs(cg)
            steps, out["skipped_groups"] = build_steps(fixture, units, auth)
            vars_: dict = {}
            clock_now = str(int(clock0))
            memory: dict = {}
            blocked: set = set()
            for n, step in enumerate(steps):
                if step[0] == replay_writes.WAIT_TERMINAL:
                    dr.wait_terminal((cg,), replay_writes.placeholders(vars_, step[2]))
                    continue
                if step[0] == replay_sessions.WAIT_SESSION:
                    replay_sessions.wait_session((cg,), str(vars_.get(step[1], step[1])), step[2])
                    continue
                if step[0] == replay_services.CALL:
                    call_module = step[4].get("X-Replay-Module", "")
                    try:
                        step[3](scratch, go_dir)
                    except ImportError:
                        # The call needs arkstudio (e.g. a hand-written setup
                        # record with its digest): skip this module's steps
                        # until its next call that works.
                        blocked.add(call_module)
                        out["blocked_calls"] += 1
                    else:
                        blocked.discard(call_module)
                    continue
                if step[0] == replay_agents.HOOK:
                    replay_agents.run_hook(step[1], [go_dir], fixture)
                    continue
                if step[0] == dr.RELOGIN:
                    if auth:
                        cg.cookies.pop("ark_session", None)
                        cg.login(user, fixture["password"])
                    continue
                if callable(step[0]):
                    if step[1] in ported:
                        step[0](scratch, go_dir)
                    continue
                out["steps"] += 1
                if step[4].get("X-Replay-Module", "") in blocked:
                    out["blocked_steps"] += 1
                    continue
                if step[0] not in ported and not step[0].startswith(replay_probe.ROUTE_PREFIX):
                    continue
                module = step[4].get("X-Replay-Module") or dr.module_of(step[0])
                same_second = bool(step[4].get("X-Replay-SameSecond"))
                step = dr.resolve(step, vars_)
                step[4].pop("X-Replay-SameSecond", None)
                offset = int(step[4].pop("X-Replay-ClockOffset", 0) or 0)
                if not same_second:
                    clock_now = str(int(clock0 + n + offset))
                step[4]["X-Replay-Clock"] = clock_now
                save = step[4].pop("X-Replay-Save", None)
                learn_as = step[4].pop("X-Replay-Learn", None)
                events = step[4].pop("X-Replay-Events", None)
                replies = []
                if events:
                    during = [dr.resolve(s, vars_) for s in events.get("during", [])]
                    for s in during:
                        s[4]["X-Replay-Clock"] = clock_now
                    resp, during_replies = dr.events_exchange(
                        cg, dr.per_server(step, go_dir), [dr.per_server(s, go_dir) for s in during],
                        events.get("before", 2), events.get("after", 1), events.get("wait", 6.0))
                    replies.append((step, resp, None))
                    replies += [(s, r, None) for s, r in zip(during, during_replies)]
                else:
                    sent = memory.get(step[2])
                    try:
                        resp = dr.send(cg, dr.per_server((step[0], step[1], step[2], step[3],
                                                          dr.fill_etags(step[4], step[2], memory)), go_dir))
                    except OSError as exc:
                        raise RuntimeError(f"step #{n} ({module}) {step[1]} {step[2]}: {exc!r}; "
                                           f"server log: {gs.log_path}") from exc
                    dr.remember(memory, step[2], resp)
                    replies.append((step, resp, sent))
                for k, (step_, resp, sent) in enumerate(replies):
                    replay_writes.learn(vars_, step_[1], step_[2], resp.status, resp.body)
                    if learn_as and step_ is step:
                        replay_main_sync.learn_cursor(vars_, learn_as, resp.status, resp.body)
                    if save and step_ is step:
                        replay_sessions.learn(vars_, save, resp.body)
                    form = dr.canonical_json(dr.canonical(step_, resp, norm, sent))
                    form = json.loads(mask(json.dumps(form)))
                    out["forms"][f"{n}.{k}"] = {"module": module, "method": step_[1], "path": mask(step_[2]), **form}
        out["state"] = json.loads(mask(json.dumps(state_dump(go_dir, start))))
    finally:
        dr._rmtree(work)
    return out


def golden_path(mode: str) -> Path:
    return GOLDENS / f"{mode}.json.gz"


def same(a: dict, b: dict) -> bool:
    """Two runs agree on a reply as far as diffreplay.combine compares it."""
    return (a["method"], a["path"]) == (b["method"], b["path"]) and not dr.combine(
        (None, a["method"], a["path"]), dr.canonical_load(a), dr.canonical_load(b))


def record(go: str, mode: str) -> dict:
    first, second = run_once(go, mode), run_once(go, mode)
    forms, unstable = {}, 0
    for key, form in first["forms"].items():
        other = second["forms"].get(key)
        if other is not None and same(form, other):
            forms[key] = dict(form, body=stored_body(form["body"]))
        else:
            forms[key] = {"module": form["module"], "method": form["method"], "path": form["path"], UNSTABLE: True}
            unstable += 1
    state_unstable = sorted(rel for rel in set(first["state"]) | set(second["state"])
                            if first["state"].get(rel) != second["state"].get(rel))
    state = {rel: stored_state(value) for rel, value in first["state"].items() if rel not in state_unstable}
    golden = {"mode": mode, "recorded_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
              "skipped_groups": first["skipped_groups"], "blocked_steps": first["blocked_steps"], "forms": forms, "state": state,
              "state_unstable": state_unstable}
    GOLDENS.mkdir(exist_ok=True)
    data = json.dumps(golden, sort_keys=True, separators=(",", ":")).encode()
    golden_path(mode).write_bytes(gzip.compress(data, compresslevel=9, mtime=0))
    print(f"{mode}: recorded {len(forms)} replies ({unstable} unstable), "
          f"{len(state)} state entries ({len(state_unstable)} unstable)")
    return golden


def check(go: str, mode: str, only: str, show: int) -> int:
    golden = json.loads(gzip.decompress(golden_path(mode).read_bytes()))
    run = run_once(go, mode)  # always the full sequence: later steps depend on earlier state
    problems, compared, unstable = [], 0, 0
    modules: dict[str, list[int]] = {}
    for key, form in golden["forms"].items():
        if only and form["module"] not in only.split(","):
            continue
        if form.get(UNSTABLE):
            unstable += 1
            continue
        got = run["forms"].get(key)
        counts = modules.setdefault(form["module"], [0, 0])
        counts[0] += 1
        compared += 1
        if got is None:
            issues = ["step missing from this run (the step sequence changed: re-record)"]
        elif (got["method"], got["path"]) != (form["method"], form["path"]):
            issues = [f"step is now {got['method']} {got['path']} (the step sequence changed: re-record)"]
        else:
            got = dict(got, body=unhash(form["body"], got["body"]))
            issues = dr.combine((None, form["method"], form["path"]), dr.canonical_load(form), dr.canonical_load(got))
        if issues:
            counts[1] += 1
            problems.append((key, form, [i.replace("py=", "golden=") for i in issues]))
    extra = [k for k in run["forms"] if k not in golden["forms"]
             and not (only and run["forms"][k]["module"] not in only.split(","))]
    state_problems = []
    if not only:
        recorded = golden["state"]
        current = {rel: v for rel, v in run["state"].items() if rel not in golden["state_unstable"]}
        hashed = {rel for rel, v in recorded.items() if isinstance(v, dict) and set(v) == {HASHED}}
        state_problems = [f"{rel}: differs from the golden (stored as a hash; diffreplay --legacy shows rows)"
                          for rel in sorted(hashed) if rel in current and stored_state(current[rel]) != recorded[rel]]
        state_problems += [p.replace("file only in python", "file only in golden")
                           for p in state_diff({r: v for r, v in recorded.items() if r not in hashed or r not in current},
                                               {r: v for r, v in current.items() if r not in hashed})]
    for key, form, issues in problems[:show]:
        print(f"#{key} {form['method']} {form['path']}")
        for issue in issues:
            print("    " + issue[:600])
    for key in extra[:show]:
        print(f"  new step #{key} {run['forms'][key]['method']} {run['forms'][key]['path']} (not in the goldens: re-record)")
    for problem in state_problems[:show]:
        print("  state: " + problem[:600])
    for name, (n, bad) in sorted(modules.items()):
        print(f"  module {name:20} compared={n:5} differences={bad}")
    for name in golden["skipped_groups"]:
        print(f"  skipped group: {name}")
    if run["blocked_steps"]:
        print(f"  skipped {run['blocked_steps']} steps after {run['blocked_calls']} calls that need arkstudio")
    print(f"{mode}: compared={compared} unstable={unstable} differences={len(problems)} "
          f"new_steps={len(extra)} state_diffs={len(state_problems)}")
    return 1 if problems or extra or state_problems else 0


TEMP_LEN = 64  # G4: length of the folder every temporary folder of the run is made in


def fixed_temp_base() -> None:
    """G4: make every temporary folder of the run (work folder, fixture, fake
    ssh, fake dsh, InfoHub databases) inside one base folder whose path has
    TEMP_LEN characters.  G1 masks those paths in replies and state, but their
    length still shows in Content-Length and in counted output sizes (agent
    session ``counts.bytes``), so without it the goldens would depend on the
    length of the machine's temp path.  A system temp path longer than
    TEMP_LEN - 20 characters is used as is (with a warning)."""
    base = Path(tempfile.gettempdir()).resolve()
    name = "ark-golden-"
    pad = TEMP_LEN - len(str(base)) - 1 - len(name)
    if pad < 8:
        print(f"warning: temp path {base} too long for G4; Content-Length checks may fail", file=sys.stderr)
        return
    root = base / (name + "x" * pad)
    root.mkdir(exist_ok=True)
    tempfile.tempdir = str(root)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--go", required=True, help="path to the ark-server binary")
    parser.add_argument("--mode", choices=MODES + ("all",), default="all")
    parser.add_argument("--record", action="store_true", help="re-record the goldens (two runs per mode)")
    parser.add_argument("--only", default="",
                        help="report only these modules (comma-separated); the whole sequence still runs")
    parser.add_argument("--show", type=int, default=15)
    args = parser.parse_args(argv)
    modes = MODES if args.mode == "all" else (args.mode,)
    fixed_temp_base()
    if args.record:
        for mode in modes:
            record(args.go, mode)
        return 0
    rc = 0
    for mode in modes:
        rc |= check(args.go, mode, args.only, args.show)
    return rc


if __name__ == "__main__":
    raise SystemExit(main())
