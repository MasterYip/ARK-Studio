"""Ported / not-ported route tables for the Go port, driven by the contract.

    python -B tests/python/contract/route_table.py          # rewrite the tables
    python -B tests/python/contract/route_table.py --check  # exit 1 when stale

Reads docs/development/go-port/contract.json (every Python route) and the `Ported` list
in go/internal/server/routes.go (what the Go server implements), then writes
docs/development/go-port/routes.md (one row per route) and the per-area summary between
the ROUTE-SUMMARY markers in docs/development/go-port/README.md.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
CONTRACT = ROOT / "docs" / "development" / "go-port" / "contract.json"
ROUTES_GO = ROOT / "go" / "internal" / "server" / "routes.go"
ROUTES_MD = ROOT / "docs" / "development" / "go-port" / "routes.md"
README = ROOT / "docs" / "development" / "go-port" / "README.md"
sys.path.insert(0, str(Path(__file__).resolve().parent))
import replay_probe  # noqa: E402
BEGIN, END = "<!-- ROUTE-SUMMARY:BEGIN -->", "<!-- ROUTE-SUMMARY:END -->"


def ported_ids() -> dict[str, set[str]]:
    """{contract id: verbs} from the PortedRoute literals in routes.go and routes_*.go."""
    source = ROUTES_GO.read_text(encoding="utf-8")
    blocks = [source.split("var Ported = []PortedRoute{", 1)[1].split("\n}", 1)[0]]
    # Modules may register their routes in their own file, either as
    #     func init() { Ported = append(Ported, PortedRoute{"id", "GET,HEAD"}, ...) }
    # or as a ``var <name>Ported = []PortedRoute{`` list appended to it.
    for other in sorted(ROUTES_GO.parent.glob("routes_*.go")):
        text = other.read_text(encoding="utf-8")
        if "Ported = append(Ported," in text:
            blocks.append(text.split("Ported = append(Ported,", 1)[1].split("\n\t)", 1)[0])
        for m in re.finditer(r"\nvar \w+Ported = \[\]PortedRoute\{(.*?)\n\}", text, re.S):
            blocks.append(m.group(1))
    out: dict[str, set[str]] = {}
    pattern = r'\{(?:"((?:[^"\\]|\\.)*)"|`([^`]*)`),\s*"([^"]*)"\}'
    for block in blocks:
        for m in re.finditer(pattern, block):
            rid = json.loads(f'"{m.group(1)}"') if m.group(1) is not None else m.group(2)
            out.setdefault(rid, set()).update(m.group(3).split(","))
    return out


def area(route: dict) -> str:
    sample = route["sample"]
    if not sample.startswith("/api/"):
        return "static, login, downloads"
    parts = sample.split("/")
    if parts[2] in {"agent", "task-agent"} and len(parts) > 3:
        return "/api/" + parts[2] + "/" + parts[3]
    return "/api/" + parts[2]


def dispatch_rows() -> tuple[list[str], int, int, int]:
    """Dispatcher records and the dispatch keys inside handlers (replay_probe.py),
    with the statuses the Go server gave in the recorded probe (probe.json)."""
    rec = replay_probe.recorded()
    groups: dict[str, list] = {}
    for rids, method, path in replay_probe.probe_list():
        rid = rids.split(" ; ")[0]
        if rid.startswith(("exact:", "regex:", "fullmatch:", "static:")) or (
                rid.startswith("prefix:") and rid in ENDPOINT_IDS):
            continue
        groups.setdefault(rid, []).append(f"{method} {path}")
    tokened = sum(1 for k in rec if k.endswith(" [token]"))
    rows, total, answered, not_ported = [], 0, 0, 0
    for rid in sorted(groups):
        keys = groups[rid]
        got = [rec.get(k) for k in keys] + [rec[k + " [token]"] for k in keys if k + " [token]" in rec]
        n_np = sum(1 for g in got if g and "NOT_PORTED" in g[1:])
        n_ok = sum(1 for g in got if g and "NOT_PORTED" not in g[1:])
        total += len(got)
        answered += n_ok
        not_ported += n_np
        codes = sorted({str(x) for g in got if g for x in g[1:]})
        state = "not recorded" if n_ok + n_np < len(keys) else ("NOT_PORTED" if n_np else "ported")
        rows.append(f"| `{rid}` | {len(got)} | {state} | {', '.join(codes)} |")
    rows.append(f"| (of which with an agent / task-agent token) | {tokened} | | |")
    return rows, total, answered, not_ported


ENDPOINT_IDS: set[str] = set()


def build() -> tuple[str, str]:
    routes = [r for r in json.loads(CONTRACT.read_text(encoding="utf-8"))["routes"] if r["role"] == "endpoint"]
    ENDPOINT_IDS.update(r["id"] for r in routes)
    drows, dtotal, danswered, dnp = dispatch_rows()
    ported = ported_ids()
    rows, areas = [], {}
    for r in sorted(routes, key=lambda r: (area(r), r["sample"], r["methods"])):
        verbs = set(r["methods"]) & ported.get(r["id"], set())
        done = bool(r["methods"]) and verbs == set(r["methods"])
        a = areas.setdefault(area(r), [0, 0, 0, 0])
        a[0] += 1
        a[1] += done
        a[2] += len(r["methods"])
        a[3] += len(verbs)
        rows.append(f"| {area(r)} | `{r['id']}` | {','.join(r['methods'])} | {r['function']} | "
                    f"{'ported' if done else ('partly: ' + ','.join(sorted(verbs)) if verbs else 'not ported')} |")
    total = sum(a[0] for a in areas.values())
    done = sum(a[1] for a in areas.values())
    pairs = sum(a[2] for a in areas.values())
    done_pairs = sum(a[3] for a in areas.values())
    md = "\n".join([
        "# Route status (generated)", "",
        "Generated by `python -B tests/python/contract/route_table.py` from `docs/development/go-port/contract.json`",
        "and the `Ported` list in `go/internal/server/routes.go`. Do not edit by hand.", "",
        f"Ported: **{done} of {total}** endpoint records ({done_pairs} of {pairs} method+route pairs).",
        "Not-ported routes answer `501 NOT_PORTED` on the Go server; unknown paths answer 404 like Python.", "",
        "| area | contract id | methods | Python handler | Go |", "|---|---|---|---|---|", *rows, "",
        "## Dispatchers and dispatch keys (route probe)", "",
        "Endpoint records cover branches that test the request path. Dispatchers (`dispatch` records) and",
        "handlers that branch on a value taken from the path (`derived:<function>:<variable>`, e.g. the",
        "skills `action`) are covered by the route probe (`tests/python/contract/replay_probe.py`): one",
        "request per dispatcher method, per dispatch key and per near-miss path (`nearmiss:`), sent to the",
        "Go server signed out on a local root and signed in as the org owner, recorded in `probe.json`.",
        "The same probe runs at the end of every differential replay, compared with Python.", "",
        f"Probe requests answered without `NOT_PORTED`: **{danswered} of {dtotal}** ({dnp} NOT_PORTED).", "",
        "| probe group | requests | Go | statuses seen (local, owner) |", "|---|---:|---|---|", *drows, ""])
    summary = [BEGIN, "",
               f"Ported: **{done} of {total}** endpoint records ({done_pairs} of {pairs} method+route pairs). "
               "Full list: [routes.md](routes.md).", "",
               f"Dispatcher branches (route probe, `probe.json`): **{danswered} of {dtotal}** probe requests "
               f"answered without `NOT_PORTED` ({dnp} NOT_PORTED).", "",
               "| area | endpoints | ported | method+route pairs ported |", "|---|---:|---:|---:|"]
    for name, (n, d, p, dp) in sorted(areas.items()):
        summary.append(f"| `{name}` | {n} | {d} | {dp}/{p} |")
    summary += ["", END]
    return md, "\n".join(summary)


def main(argv: list[str]) -> int:
    md, summary = build()
    readme = README.read_text(encoding="utf-8")
    head, rest = readme.split(BEGIN, 1)
    tail = rest.split(END, 1)[1]
    new_readme = head + summary + tail
    if "--check" in argv:
        stale = []
        if not ROUTES_MD.is_file() or ROUTES_MD.read_text(encoding="utf-8") != md:
            stale.append(ROUTES_MD)
        if readme != new_readme:
            stale.append(README)
        missing = replay_probe.stale_probes()
        if missing:
            print(f"stale: docs/development/go-port/probe.json lacks {len(missing)} probe requests, e.g. "
                  f"{missing[0]!r} (run replay_probe.py --go go/ark-server.exe --write)")
            stale.append(replay_probe.PROBE_JSON)
        np_ = [k for k, v in replay_probe.recorded().items() if "NOT_PORTED" in v[1:]]
        if np_:
            print(f"probe.json: {len(np_)} probe requests answered NOT_PORTED, e.g. {np_[0]!r}")
            stale.append(replay_probe.PROBE_JSON)
        for key in replay_probe.unclassified():
            print(f"unclassified dispatch variable {key}: add it to replay_probe.DERIVED or NOT_DISPATCH")
            stale.append(replay_probe.SERVER)
        for path in stale:
            print(f"stale: {path.relative_to(ROOT)} (run tests/python/contract/route_table.py)")
        return 1 if stale else 0
    ROUTES_MD.write_text(md, encoding="utf-8", newline="\n")
    README.write_text(new_readme, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
