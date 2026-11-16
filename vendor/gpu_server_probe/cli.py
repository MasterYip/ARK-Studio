"""Standalone CLI for the vendored GPU server probe adapter.

    python -m vendor.gpu_server_probe status
    python -m vendor.gpu_server_probe servers
    python -m vendor.gpu_server_probe probe            # dry run: print the plan
    python -m vendor.gpu_server_probe probe --live     # read-only nvidia-smi
    python -m vendor.gpu_server_probe dispatch --command "python train.py"

``probe --live`` is the only command that opens an SSH connection, and it runs
only ``nvidia-smi``. ``dispatch`` never executes anything.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

from . import config as server_config
from . import dispatch, probe, ssh


def default_config_path() -> Path:
    override = (os.environ.get("ARK_GPU_SERVERS_CONFIG") or "").strip()
    if override:
        return Path(override).expanduser()
    private = (os.environ.get("ARK_PRIVATE_DIR") or "").strip()
    base = Path(private).expanduser() if private else Path.home() / ".ark"
    return base / "gpu_servers.yaml"


def _load(args) -> dict:
    raw = getattr(args, "config", "")
    path = Path(raw).expanduser() if raw else default_config_path()
    try:
        return server_config.load_config(path)
    except server_config.ConfigNotFound:
        print(f"error: no server list at {path}", file=sys.stderr)
        print("Copy servers_rp.example.yaml there and set enabled: true.", file=sys.stderr)
        raise SystemExit(2)
    except server_config.ConfigError as exc:
        print(f"error: invalid server list: {exc}", file=sys.stderr)
        raise SystemExit(2)


def _status(args) -> int:
    config = _load(args)
    raw = getattr(args, "config", "")
    path = Path(raw).expanduser() if raw else default_config_path()
    deps = ssh.dependency_status()
    print(f"config:   {path} ({'present' if path.is_file() else 'missing'})")
    print(f"enabled:  {config['enabled']} (set enabled: true or ARK_GPU_PROBE=1 in ARK)")
    print(f"servers:  {len(config['servers'])}")
    print(f"ssh:      {deps['binary'] or 'missing — ' + deps['hint']}")
    print(f"asyncssh: {'installed (optional)' if ssh.asyncssh_available() else 'not installed (not required)'}")
    return 0


def _servers(args) -> int:
    config = _load(args)
    if not config["servers"]:
        print("No servers configured.")
        return 0
    for server in config["servers"]:
        tags = ",".join(server["tags"]) if server["tags"] else "-"
        user = server["user"] or "-"
        state = "enabled" if server["enabled"] else "disabled"
        print(f"{server['name']:<16} {user}@{server['host']}:{server['port']:<6} "
              f"{state:<9} tags={tags}")
    return 0


def _probe(args) -> int:
    config = _load(args)
    selected = [server for server in config["servers"]
                if not args.server or server["name"] in set(args.server)]
    if args.server:
        missing = set(args.server) - {server["name"] for server in selected}
        if missing:
            print(f"error: unknown server(s): {', '.join(sorted(missing))}", file=sys.stderr)
            return 2
    if not selected:
        print("error: no servers configured", file=sys.stderr)
        return 2
    if not args.live:
        print("Dry run — no SSH connection will be opened. Use --live to probe.")
        for server in selected:
            if not server["enabled"]:
                print(f"  skip {server['name']}: disabled in the server list")
                continue
            argv = ssh.build_ssh_argv(server, ssh.ssh_binary() or "ssh", server["timeout_seconds"])
            print(f"  {server['name']}: {' '.join(argv)}")
        return 0
    if not config["enabled"]:
        print("error: probing is disabled by default; set enabled: true in the server list",
              file=sys.stderr)
        return 2

    results = []
    runner = ssh.run_remote
    for server in selected:
        if not server["enabled"]:
            continue
        result = probe.probe_server(server, runner, timeout=args.timeout)
        results.append(result)
        if result["ok"]:
            print(f"{server['name']}: {len(result['gpus'])} GPU(s)")
            for gpu in result["gpus"]:
                print(f"  [{gpu['index']}] {gpu['name']}  free={gpu['free_memory_mb']} MB "
                      f"util={gpu['utilization_pct']}%  score={gpu['score']}")
        else:
            print(f"{server['name']}: {result['error']['kind']}: {result['error']['message']}",
                  file=sys.stderr)
    ranked = probe.rank_gpus(results)
    if ranked:
        print("\nRanked free GPUs:")
        for item in ranked:
            print(f"  {item['score']:>10.1f}  {item['server']}[{item['gpu_index']}] "
                  f"{item['gpu_name']} free={item['free_memory_mb']} MB "
                  f"util={item['utilization_pct']}%")
    if args.json:
        print(json.dumps({"servers": results, "ranked": ranked}, ensure_ascii=False, indent=2))
    return 0


def _dispatch(args) -> int:
    config = _load(args)
    body = {
        "server": args.server or "",
        "gpus": args.gpus,
        "job": args.job,
        "project": args.project,
        "timeout_minutes": args.timeout_minutes,
        "work_dir": args.work_dir,
        "command": args.command,
    }
    try:
        request = dispatch.build_request(body, config["servers"])
    except dispatch.DispatchError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(request, ensure_ascii=False, indent=2))
    return 0


def main(argv: list[str] | None = None) -> int:
    # ``--config`` is accepted before or after the subcommand.
    root_options = argparse.ArgumentParser(add_help=False)
    root_options.add_argument("--config", default="",
                              help="server list path (default: ARK_PRIVATE_DIR/gpu_servers.yaml)")
    sub_options = argparse.ArgumentParser(add_help=False)
    sub_options.add_argument("--config", default=argparse.SUPPRESS,
                             help="server list path (default: ARK_PRIVATE_DIR/gpu_servers.yaml)")

    parser = argparse.ArgumentParser(prog="gpu_server_probe", description=__doc__,
                                     parents=[root_options])
    sub = parser.add_subparsers(dest="action")

    sub.add_parser("status", parents=[sub_options], help="show config and SSH dependency state")
    sub.add_parser("servers", parents=[sub_options], help="list configured servers")

    p_probe = sub.add_parser("probe", parents=[sub_options],
                             help="dry-run plan or read-only live GPU probe")
    p_probe.add_argument("--live", action="store_true", help="open SSH and run nvidia-smi")
    p_probe.add_argument("--server", action="append", default=[], help="limit to a server (repeatable)")
    p_probe.add_argument("--timeout", type=int, default=None, help="per-server SSH timeout in seconds")
    p_probe.add_argument("--json", action="store_true", help="also print machine-readable results")

    p_dispatch = sub.add_parser("dispatch", parents=[sub_options],
                                help="print a bounded, dry-run dispatch request")
    p_dispatch.add_argument("--server", default="")
    p_dispatch.add_argument("--gpus", default="", help="comma-separated GPU indices, e.g. 0,1")
    p_dispatch.add_argument("--job", default="ark-job")
    p_dispatch.add_argument("--project", default="ark")
    p_dispatch.add_argument("--timeout-minutes", type=int, default=60)
    p_dispatch.add_argument("--work-dir", default="")
    p_dispatch.add_argument("--command", required=True)

    args = parser.parse_args(argv)
    if not args.action:
        parser.print_help()
        return 0
    handlers = {"status": _status, "servers": _servers, "probe": _probe, "dispatch": _dispatch}
    return handlers[args.action](args)


if __name__ == "__main__":
    raise SystemExit(main())
