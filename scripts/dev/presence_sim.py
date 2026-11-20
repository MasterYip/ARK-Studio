#!/usr/bin/env python3
"""Simulate a few agents working on a project, to see agent presence (T-0043).

Each simulated agent uses the agent API with its own ``X-Agent-Name``: it
walks from node to node along the links (jumping elsewhere now and then),
reads each node, sometimes announces it works on one
(POST /api/agent/v1/presence), edits it (a tag it removes again at the end),
comments on it, and says "done".  The graphs of every browser that shows the
project draw the agents' badges as they go.

Stdlib only.  Examples:

  # a local ark-server started with --root <scratch folder>
  python scripts/dev/presence_sim.py --base http://127.0.0.1:8890 --root <scratch folder>

  # a server with accounts: a personal agent token and the project id
  python scripts/dev/presence_sim.py --base https://ark.example --token arka_... --project prj_abc --agents 4

Writes are real: ``--no-writes`` keeps it to reads and announcements,
``--no-comments`` skips the comments.  Every tag the simulation adds is
removed before it exits (also on Ctrl+C).
"""
from __future__ import annotations

import argparse
import json
import os
import random
import ssl
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

NAMES = ["claude", "codex", "gemini", "aider", "cursor-agent"]
TAG = "agent-sim"
NOTES = ["Reviewing the evidence", "Drafting the summary", "Checking the links", "Updating the plan",
         "Comparing the results"]
COMMENTS = ["Read this; the claim still holds.", "Linked evidence looks consistent.",
            "Needs one more source before review.", "Checked the numbers against the table."]


class Api:
    def __init__(self, base: str, token: str, project: str = "", insecure: bool = False):
        self.base = base.rstrip("/")
        self.token = token
        self.project = project
        self.ctx = ssl._create_unverified_context() if insecure else None  # noqa: S323 (dev tool)

    def call(self, method: str, path: str, agent: str, body=None):
        headers = {"Authorization": f"Bearer {self.token}", "X-Agent-Name": agent}
        if self.project:
            headers["X-ARK-Project"] = self.project
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=30, context=self.ctx) as resp:
                return json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as err:
            raise RuntimeError(f"{method} {path}: {err.code} {err.read()[:300]!r}") from None


def neighbours(unit: dict) -> list:
    out = []
    for key in ("links_out", "links_in"):
        for link in unit.get(key) or []:
            other = link.get("target") or link.get("source") or {}
            uid = other.get("uid") if isinstance(other, dict) else None
            if uid:
                out.append(uid)
    return out


class Agent(threading.Thread):
    def __init__(self, sim: "Sim", name: str, start_uid: str, seed: int):
        super().__init__(daemon=True, name=name)
        self.sim, self.agent, self.uid = sim, name, start_uid
        self.rand = random.Random(seed)
        self.working = ""  # the unit announced and not yet done

    def pause(self, low: float, high: float) -> bool:
        return not self.sim.stop.wait(self.rand.uniform(low, high) / self.sim.speed)

    def run(self):
        try:
            self.walk()
        finally:
            if self.working:  # stopped mid-task: say so, or the badge stays 5 min
                try:
                    self.sim.api.call("POST", "/api/agent/v1/presence", self.agent, {"unit": self.working, "state": "done"})
                except Exception:
                    pass

    def walk(self):
        api, rand = self.sim.api, self.rand
        while not self.sim.stop.is_set():
            try:
                unit = api.call("GET", f"/api/agent/v1/units/{self.uid}", self.agent)["unit"]
                self.sim.log(f"{self.agent:>12} reads   {self.uid}  {unit.get('title', '')[:50]}")
                if not self.pause(1.2, 2.6):
                    break
                roll = rand.random()
                if roll < 0.35:
                    api.call("POST", "/api/agent/v1/presence", self.agent,
                             {"unit": self.uid, "state": "working", "note": rand.choice(NOTES)})
                    self.working = self.uid
                    self.sim.log(f"{self.agent:>12} works   {self.uid}")
                    if not self.pause(2.0, 4.0):
                        break
                    if self.sim.writes:
                        self.sim.toggle_tag(self.agent, unit)
                        self.sim.log(f"{self.agent:>12} edits   {self.uid}")
                        if not self.pause(1.0, 2.0):
                            break
                    if self.sim.writes and self.sim.comments and rand.random() < 0.6:
                        api.call("POST", f"/api/agent/v1/units/{self.uid}/comments", self.agent,
                                 {"body": f"{rand.choice(COMMENTS)} ({self.agent}, simulated)"})
                        self.sim.log(f"{self.agent:>12} comment {self.uid}")
                        if not self.pause(1.0, 2.0):
                            break
                    api.call("POST", "/api/agent/v1/presence", self.agent, {"unit": self.uid, "state": "done"})
                    self.working = ""
                    self.sim.log(f"{self.agent:>12} done    {self.uid}")
                elif roll < 0.55 and self.sim.writes:
                    self.sim.toggle_tag(self.agent, unit)
                    self.sim.log(f"{self.agent:>12} edits   {self.uid}")
                if not self.pause(0.6, 1.4):
                    break
                near = neighbours(unit)
                self.uid = rand.choice(near) if near and rand.random() < 0.75 else rand.choice(self.sim.uids)
            except Exception as err:  # keep walking; a node may have been archived meanwhile
                self.sim.log(f"{self.agent:>12} error   {err}")
                self.uid = rand.choice(self.sim.uids)
                if not self.pause(1.0, 2.0):
                    break


class Sim:
    def __init__(self, api: Api, uids: list, writes: bool, comments: bool, speed: float, quiet: bool):
        self.api, self.uids, self.writes, self.comments, self.speed, self.quiet = api, uids, writes, comments, speed, quiet
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.tagged: dict[str, tuple] = {}  # uid -> (agent, original tags), restored at the end

    def log(self, text: str):
        if not self.quiet:
            with self.lock:
                print(time.strftime("%H:%M:%S"), text, flush=True)

    def toggle_tag(self, agent: str, unit: dict):
        uid = unit["uid"]
        tags = [t for t in unit.get("tags") or [] if not str(t).startswith(("type:", "status:", "importance:"))]
        with self.lock:
            self.tagged.setdefault(uid, (agent, [t for t in tags if t != TAG]))
        new = [t for t in tags if t != TAG] if TAG in tags else tags + [TAG]
        self.api.call("POST", f"/api/agent/v1/units/{uid}", agent, {"tags": new})

    def restore(self):
        for uid, (agent, tags) in self.tagged.items():
            try:
                self.api.call("POST", f"/api/agent/v1/units/{uid}", agent, {"tags": tags})
            except Exception as err:
                print(f"could not restore the tags of {uid}: {err}", file=sys.stderr)


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--base", default="http://127.0.0.1:8890", help="ARK server URL")
    ap.add_argument("--token", default=os.environ.get("ARK_AGENT_TOKEN", ""),
                    help="agent token (server token or a personal arka_ token; env ARK_AGENT_TOKEN)")
    ap.add_argument("--root", help="read the server agent token from <root>/.agent-token")
    ap.add_argument("--project", default="", help="project id (X-ARK-Project, for personal tokens)")
    ap.add_argument("--agents", type=int, default=4, help="number of agents, 1-5 (default 4)")
    ap.add_argument("--names", help="comma-separated agent names")
    ap.add_argument("--duration", type=float, default=90, help="seconds to run (0: until Ctrl+C)")
    ap.add_argument("--speed", type=float, default=1.0, help="time scale (2 = twice as fast)")
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--no-writes", action="store_true", help="reads and announcements only")
    ap.add_argument("--no-comments", action="store_true")
    ap.add_argument("--insecure", action="store_true", help="skip TLS verification (self-signed test servers)")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args(argv)
    token = args.token
    if args.root:
        token = Path(args.root, ".agent-token").read_text(encoding="utf-8").strip()
    if not token:
        ap.error("an agent token is required (--token, --root or ARK_AGENT_TOKEN)")
    api = Api(args.base, token, args.project, args.insecure)
    units = api.call("GET", "/api/agent/v1/units?limit=500", "presence-sim").get("items") or []
    uids = [u["uid"] for u in units if u.get("uid") and u.get("type") != "topic"]
    if not uids:
        print("The project has no nodes to walk over.", file=sys.stderr)
        return 1
    names = [n.strip() for n in (args.names or "").split(",") if n.strip()] or NAMES
    count = max(1, min(5, args.agents, len(names)))
    sim = Sim(api, uids, not args.no_writes, not args.no_comments, max(0.1, args.speed), args.quiet)
    rand = random.Random(args.seed)
    agents = [Agent(sim, names[i], rand.choice(uids), args.seed + i) for i in range(count)]
    print(f"{count} agents on {len(uids)} nodes at {args.base} (Ctrl+C stops)", flush=True)
    for i, agent in enumerate(agents):
        agent.start()
        time.sleep(0.4 / sim.speed)
    try:
        deadline = time.monotonic() + args.duration if args.duration > 0 else None
        while not sim.stop.is_set() and (deadline is None or time.monotonic() < deadline):
            time.sleep(0.2)
    except KeyboardInterrupt:
        pass
    sim.stop.set()
    for agent in agents:
        agent.join(timeout=10)
    sim.restore()
    print("stopped; tags restored on", len(sim.tagged), "nodes", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
