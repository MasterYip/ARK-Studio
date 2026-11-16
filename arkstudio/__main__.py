"""Command line entry point.

    python -m arkstudio serve --port 8890
    python -m arkstudio new "My paper" --template paper
    python -m arkstudio export --project runtime/projects/My-paper.ark --format markdown
    python -m arkstudio agent context
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

from . import auth as auth_mod
from . import export, mobile, org as org_mod, server_config
from .assistant import context
from .projects import ProjectManager, ProjectError
from .server import App, serve
from .store import StoreError


def _env_bool(name: str, default: bool = False) -> bool:
    raw = os.environ.get(name)
    if raw is None:
        return default
    return str(raw).strip().lower() in {"1", "true", "yes", "on"}


def _env_int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name) or default)
    except (TypeError, ValueError):
        return default


def _build_auth(args, environ=None) -> auth_mod.AuthManager | None:
    """Resolve CLI + env auth settings into an in-memory manager.

    CLI password sources win as a group over env sources, so setting
    ``ARK_AUTH_PASSWORD`` cannot accidentally combine with an explicit
    ``--auth-password-hash``.  Env is the recommended place for the plaintext;
    ``ARK_AUTH_PASSWORD_HASH`` keeps even that out of the environment.
    """
    env = os.environ if environ is None else environ
    cli_sources = (args.auth_password, args.auth_password_file, args.auth_password_hash)
    use_cli = any(bool(item) for item in cli_sources)
    password = args.auth_password if use_cli else env.get("ARK_AUTH_PASSWORD", "")
    password_file = (args.auth_password_file if use_cli
                     else env.get("ARK_AUTH_PASSWORD_FILE", ""))
    password_hash = (args.auth_password_hash if use_cli
                     else env.get("ARK_AUTH_PASSWORD_HASH", ""))
    username = str(args.auth_user or env.get("ARK_AUTH_USER", "") or "").strip()
    session_ttl = int(args.auth_session_ttl or _env_int(
        "ARK_AUTH_SESSION_TTL", auth_mod.SESSION_TTL_DEFAULT))
    if not any((password, password_file, password_hash)) and hasattr(args, "root"):
        saved = server_config.persisted_auth(org_mod.default_org_path(args.root), username)
        if saved is not None:
            saved.session_ttl = max(auth_mod.SESSION_TTL_MIN, min(auth_mod.SESSION_TTL_MAX, session_ttl))
            return saved
        if (Path(args.root).expanduser() / ".auth-secret").exists():
            raise auth_mod.AuthConfigError("Existing sign-in state has no organization accounts; restore the organization database before starting")
    return auth_mod.build_manager(
        username=username,
        password=password or "",
        password_file=password_file or "",
        password_hash=password_hash or "",
        session_ttl=session_ttl,
    )


def _default_root() -> Path:
    import os
    return Path(os.environ.get("ARK_ROOT") or Path.cwd() / "runtime" / "projects")


def _open_store(path: str) -> ProjectManager:
    from .db import read_manifest
    from .store import Store
    folder = Path(path).resolve()
    manifest = read_manifest(folder)
    if not manifest:
        raise ProjectError(f"not an ARK project: {folder}")
    return ProjectManager(folder.parent, seed_demo=False)


def _json_arg(value: str) -> dict:
    """Read a JSON object from a file path or an inline JSON string."""
    text = str(value or "").strip()
    if not text:
        return {}
    path = Path(text)
    if path.is_file():
        text = path.read_text(encoding="utf-8-sig")
    payload = json.loads(text.lstrip("\ufeff"))
    if not isinstance(payload, dict):
        raise ValueError("JSON payload must be an object")
    return payload


def _run_skills_cli(args, skills_pkg) -> int:
    command = getattr(args, "skills_command", None) or "list"
    if command == "list":
        print(json.dumps(skills_pkg.list_skills(), ensure_ascii=False, indent=2))
        return 0
    if command == "show":
        print(json.dumps(skills_pkg.get_skill(args.skill), ensure_ascii=False, indent=2))
        return 0
    if command == "render":
        data = skills_pkg.render_template(args.skill, args.template,
                                           _json_arg(args.context))
        if args.out:
            Path(args.out).write_text(data["text"], encoding="utf-8")
            print(args.out)
        else:
            print(data["text"])
        return 0
    if command == "validate":
        result = skills_pkg.validate_payload(
            args.skill, args.kind, _json_arg(args.payload),
            project_root=args.project_root or None)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0 if result.get("ok") else 1
    if command == "interface":
        print(json.dumps(
            skills_pkg.interface.describe(
                skill_ids=[skill["id"] for skill in skills_pkg.SKILLS]),
            ensure_ascii=False, indent=2))
        return 0
    if command == "brief":
        result = skills_pkg.interface.validate_brief(_json_arg(args.payload))
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0 if result.get("ok") else 1
    if command == "setup":
        return _run_setup_cli(args, skills_pkg)
    print(f"unknown skills command: {command}", file=sys.stderr)
    return 2


def _run_setup_cli(args, skills_pkg) -> int:
    """Local setup lifecycle; the CLI user at the terminal is the human actor."""
    setup = skills_pkg.setup
    action = getattr(args, "setup_action", None) or "state"
    if action == "questions" and not args.project:
        result = {"questions": setup.questions(
            setup.normalize_profile(_json_arg(args.payload))[0])}
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    if not args.project:
        print("--project <path.ark> is required", file=sys.stderr)
        return 2
    project = Path(args.project).resolve()
    if not (project / "project.json").is_file():
        print(f"not an ARK project: {project}", file=sys.stderr)
        return 2
    try:
        if action == "state":
            result = setup.state_payload(project)
        elif action == "summary":
            print(setup.state_payload(project)["summary_markdown"])
            return 0
        elif action == "questions":
            result = {"questions": setup.state_payload(project)["questions"]}
        elif action == "discover":
            result = setup.discover(args.paths or [], project={"path": str(project)})
        elif action == "draft":
            result = setup.save_draft(project, _json_arg(args.payload),
                                      actor="agent:cli", propose=args.propose)
        elif action == "confirm":
            import getpass
            result = setup.confirm(project, actor="human:" + getpass.getuser(),
                                   digest_value=args.digest or "", note=args.note or "")
        elif action == "verify":
            record = setup.load(project)
            result = setup.record_verification(project, setup.verify(record["profile"]),
                                               actor="agent:cli")
        else:
            print(f"unknown setup action: {action}", file=sys.stderr)
            return 2
    except setup.SetupError as exc:
        print(f"setup error ({exc.code}): {exc}", file=sys.stderr)
        return 1
    result.pop("summary_markdown", None)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if result.get("ok", True) else 1


_DEPRECATION_WARNED = False


def _warn_python_server_deprecated() -> None:
    """One-time notice at serve start; nothing else changes."""
    global _DEPRECATION_WARNED
    if _DEPRECATION_WARNED:
        return
    _DEPRECATION_WARNED = True
    print("warning: the Python server is deprecated; run ark-server (the Go server, same flags "
          "and data) instead. See docs/development/go-port/switch-over.md.", file=sys.stderr)


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else list(argv)
    parser = argparse.ArgumentParser(prog="arkstudio", description="ARK Studio research workbench")
    sub = parser.add_subparsers(dest="command")

    p_init = sub.add_parser("server-init", help="initialize persistent server configuration and an owner account")
    p_init.add_argument("--config", required=True)
    p_init.add_argument("--data-dir", required=True)
    p_init.add_argument("--owner", default="admin")
    p_init.add_argument("--password-file", default="", help="initial password file for unattended setup; otherwise prompt securely")
    p_init.add_argument("--port", type=int, default=8890)
    p_init.add_argument("--public-url", default="")

    p_serve = sub.add_parser("serve", help="run the local workbench")
    p_serve.add_argument("--config", default="", help="persistent server JSON configuration")
    p_serve.add_argument("--require-auth", action="store_true", help="refuse to start without account authentication")
    p_serve.add_argument("--root", default=str(_default_root()))
    p_serve.add_argument(
        "--host", default=None,
        help="bind address (default: 127.0.0.1; 0.0.0.0 with --expose or ARK_PUBLIC)")
    p_serve.add_argument("--port", type=int, default=_env_int("ARK_PORT", 8890))
    p_serve.add_argument("--no-demo", action="store_true", help="do not create the starter demo project")
    p_serve.add_argument("--media-root", action="append", default=[],
                         help="allow local media under this folder (repeatable)")
    p_serve.add_argument("--sessions-root", default="",
                         help="task-agent session logs (default: <root>/../agent-sessions)")
    p_serve.add_argument("--public-url", default=os.environ.get("ARK_PUBLIC_URL", ""),
                         help="advertised base URL for phone pairing (or ARK_PUBLIC_URL)")
    p_serve.add_argument(
        "--expose", action="store_true", default=_env_bool("ARK_PUBLIC"),
        help="bind 0.0.0.0 for phone/public access; requires account authentication "
             "(or ARK_PUBLIC=1)")
    p_serve.add_argument("--auth-user", default="",
                         help="sign-in username (or ARK_AUTH_USER)")
    p_serve.add_argument(
        "--auth-password", default="",
        help="sign-in password; visible in the process list, prefer ARK_AUTH_PASSWORD")
    p_serve.add_argument("--auth-password-file", default="",
                         help="read the sign-in password from a file (or ARK_AUTH_PASSWORD_FILE)")
    p_serve.add_argument(
        "--auth-password-hash", default="",
        help="PBKDF2 hash instead of a plaintext password (or ARK_AUTH_PASSWORD_HASH)")
    p_serve.add_argument(
        "--auth-session-ttl", type=int, default=0,
        help=f"sign-in session seconds (default: {auth_mod.SESSION_TTL_DEFAULT}; "
             "or ARK_AUTH_SESSION_TTL)")
    p_serve.add_argument("--public-ip", default=os.environ.get("ARK_PUBLIC_IP", ""),
                         help="explicit public IP for pairing URLs (or ARK_PUBLIC_IP)")
    p_serve.add_argument(
        "--detect-public-ip", action="store_true",
        default=_env_bool("ARK_DETECT_PUBLIC_IP"),
        help="opt in to one short public-IP lookup for pairing (or ARK_DETECT_PUBLIC_IP=1)")
    p_serve.add_argument(
        "--trust-proxy", action="store_true", default=_env_bool("ARK_TRUST_PROXY"),
        help="trust X-Forwarded-For/Proto/Prefix from a reverse proxy "
             "(or ARK_TRUST_PROXY=1)")

    p_desktop = sub.add_parser("desktop", help="run the frameless system-tray window")
    p_desktop.add_argument("--root", default=str(_default_root()))
    p_desktop.add_argument("--host", default="127.0.0.1")
    p_desktop.add_argument("--port", type=int, default=8890)
    p_desktop.add_argument("--no-server", action="store_true",
                           help="use an already running ARK server")
    p_desktop.add_argument("--remote-url", default=os.environ.get("ARK_REMOTE_URL", ""),
                           help="remote ARK URL to load in the desktop window")
    p_desktop.add_argument("--local", action="store_true",
                           help="ignore the saved remote URL and open local ARK")

    p_remote = sub.add_parser("remote", help="open a remote ARK project through SSH")
    p_remote.add_argument("--ssh", required=True)
    p_remote.add_argument("--user", default="")
    p_remote.add_argument("--ssh-port", type=int, default=22)
    p_remote.add_argument("--remote-port", type=int, default=8890)
    p_remote.add_argument("--local-port", type=int, default=8890)
    p_remote.add_argument("--identity", default="")
    p_remote.add_argument("--remote-path", default="")
    p_remote.add_argument("--mount", default="")
    p_remote.add_argument("--no-open", action="store_true")

    p_tunnel = sub.add_parser(
        "mobile-tunnel",
        help="publish a loopback ARK server to the LAN for phone pairing (SSH local forward)")
    p_tunnel.add_argument("--ssh", default=os.environ.get("ARK_SSH_TARGET", "user@server"),
                          help="SSH target, e.g. user@gpu-server-1 (default: ARK_SSH_TARGET)")
    p_tunnel.add_argument("--ssh-port", type=int, default=22)
    p_tunnel.add_argument("--ark-port", type=int, default=8890,
                          help="ARK server port on the SSH server")
    p_tunnel.add_argument("--local-port", type=int, default=8891,
                          help="port this computer opens on its LAN interface")
    p_tunnel.add_argument("--bind", default="0.0.0.0",
                          help="local bind address (0.0.0.0 exposes the LAN)")
    p_tunnel.add_argument("--identity", default="")

    p_new = sub.add_parser("new", help="create a project folder")
    p_new.add_argument("name")
    p_new.add_argument("--root", default=str(_default_root()))
    p_new.add_argument("--template", choices=["blank", "paper", "demo"], default="blank")
    p_new.add_argument("--description", default="")

    p_demo = sub.add_parser("demo", help="create the PredActor demo project")
    p_demo.add_argument("--root", default=str(_default_root()))

    p_export = sub.add_parser("export", help="export a project")
    p_export.add_argument("--project", required=True, help="project folder")
    p_export.add_argument("--format", choices=["json", "markdown", "csv", "svg", "bundle"], default="json",
                          help="bundle: ZIP of Markdown notes with media for --units")
    p_export.add_argument("--units", default="", help="bundle: comma-separated node UIDs")
    p_export.add_argument("--depth", type=int, default=1,
                          help="bundle: levels of referenced nodes to include (0-3, default 1)")
    p_export.add_argument("--media-root", action="append", default=[],
                          help="bundle: extra folder local media may be read from")
    p_export.add_argument("--out", default="")

    p_agent = sub.add_parser("agent", help="local agent context")
    agent_sub = p_agent.add_subparsers(dest="agent_command")
    p_context = agent_sub.add_parser("context", help="print compact project context")
    p_context.add_argument("--project", default="", help="project folder (default: first project)")
    p_context.add_argument("--root", default=str(_default_root()))

    from . import session_watch
    session_watch.build_parser(sub.add_parser(
        "session-watch",
        help="upload an external agent's session JSONL to ARK at an interval (opt-in)"))

    p_skills = sub.add_parser(
        "skills", help="discover ARK skills and render/validate their templates")
    skills_sub = p_skills.add_subparsers(dest="skills_command")
    skills_sub.add_parser("list", help="list available ARK skills")
    p_skill_show = skills_sub.add_parser("show", help="show one skill with its documents")
    p_skill_show.add_argument("skill")
    p_skill_render = skills_sub.add_parser("render", help="render a skill template")
    p_skill_render.add_argument("skill")
    p_skill_render.add_argument("template")
    p_skill_render.add_argument("--context", default="",
                                help="JSON file or inline JSON object")
    p_skill_render.add_argument("--out", default="")
    p_skill_validate = skills_sub.add_parser("validate", help="validate a skill payload")
    p_skill_validate.add_argument("skill")
    p_skill_validate.add_argument("kind")
    p_skill_validate.add_argument("--payload", default="",
                                  help="JSON file or inline JSON object")
    p_skill_validate.add_argument("--project-root", default="")
    skills_sub.add_parser("interface", help="print the agent interface descriptor (v2)")
    p_skill_brief = skills_sub.add_parser("brief", help="validate a delegation brief")
    p_skill_brief.add_argument("--payload", default="",
                               help="JSON file or inline JSON object")
    p_skill_setup = skills_sub.add_parser(
        "setup", help="environment setup: state|summary|questions|discover|draft|confirm|verify")
    p_skill_setup.add_argument("setup_action", nargs="?", default="state",
                               choices=["state", "summary", "questions", "discover",
                                        "draft", "confirm", "verify"])
    p_skill_setup.add_argument("--project", default="", help="path to the .ark project folder")
    p_skill_setup.add_argument("--paths", nargs="*", default=[],
                               help="candidate repository / task-space folders to discover")
    p_skill_setup.add_argument("--payload", default="", help="profile JSON file or inline JSON")
    p_skill_setup.add_argument("--propose", action="store_true",
                               help="mark the draft as proposed for human confirmation")
    p_skill_setup.add_argument("--digest", default="", help="digest the human reviewed")
    p_skill_setup.add_argument("--note", default="")

    args = parser.parse_args(argv)
    if not args.command:
        args = parser.parse_args(["serve"] + (argv or []))

    try:
        if args.command == "server-init":
            if args.password_file:
                password = auth_mod.read_password_file(args.password_file)
            else:
                import getpass
                password = getpass.getpass("Initial owner password: ")
                if password != getpass.getpass("Confirm password: "):
                    raise auth_mod.AuthConfigError("Passwords do not match")
            print(json.dumps(server_config.initialize(args.config, args.data_dir, args.owner,
                                                     password, port=args.port, public_url=args.public_url)))
            return 0
        if args.command == "serve":
            if args.config:
                defaults, environment = server_config.load(args.config)
                source_flags = {"--auth-password", "--auth-password-file", "--auth-password-hash"}
                if any(part.split("=", 1)[0] in source_flags for part in argv):
                    for name in ("auth_password", "auth_password_file", "auth_password_hash"):
                        defaults.pop(name, None)
                for key in ("ARK_AUTH_USER", "ARK_AUTH_PASSWORD", "ARK_AUTH_PASSWORD_FILE", "ARK_AUTH_PASSWORD_HASH", "ARK_AUTH_SYNC"):
                    os.environ.pop(key, None)
                os.environ.update(environment)
                p_serve.set_defaults(**defaults)
                args = parser.parse_args(argv)
            expose = bool(args.expose or _env_bool("ARK_PUBLIC"))
            host = (args.host or os.environ.get("ARK_HOST") or "").strip()
            if not host:
                host = "0.0.0.0" if expose else "127.0.0.1"
            if expose and mobile.is_loopback_host(host):
                print("warning: --expose was requested together with a loopback "
                      "--host; binding loopback only.", file=sys.stderr)
                expose = False
            auth_manager = _build_auth(args)
            if args.require_auth and auth_manager is None:
                raise auth_mod.AuthConfigError("Account authentication is required; configure an initial owner or restore the organization database")
            if not mobile.is_loopback_host(host) and auth_manager is None:
                print(
                    f"error: refusing to bind ARK to {host} without account "
                    "authentication. Set --auth-user and ARK_AUTH_PASSWORD (or "
                    "--auth-password-hash / --auth-password-file), or leave the "
                    "default 127.0.0.1 bind. ARK never exposes itself publicly "
                    "without a sign-in.", file=sys.stderr)
                return 2
            if args.auth_password:
                print("warning: --auth-password is visible to other processes; "
                      "prefer ARK_AUTH_PASSWORD or ARK_AUTH_PASSWORD_HASH.",
                      file=sys.stderr)
            _warn_python_server_deprecated()
            app = App(args.root, seed_demo=not args.no_demo, media_roots=args.media_root,
                      task_sessions_root=args.sessions_root or None,
                      public_url=args.public_url or None, auth=auth_manager,
                      expose=expose, public_ip=args.public_ip,
                      detect_public_ip=args.detect_public_ip,
                      trust_proxy=args.trust_proxy)
            serve(app, host=host, port=args.port)
            return 0
        if args.command == "mobile-tunnel":
            from .mobile import run_tunnel
            return run_tunnel(ssh_target=args.ssh, ssh_port=args.ssh_port,
                              ark_port=args.ark_port, local_port=args.local_port,
                              bind=args.bind, identity=args.identity)
        if args.command == "desktop":
            from .desktop import run_desktop
            return run_desktop(root=args.root, host=args.host, port=args.port,
                               server=not args.no_server, remote_url=args.remote_url,
                               force_local=args.local)

        if args.command == "remote":
            from .remote import run as run_remote
            return run_remote(ssh_host=args.ssh, user=args.user, ssh_port=args.ssh_port,
                              remote_port=args.remote_port, local_port=args.local_port,
                              identity=args.identity, remote_path=args.remote_path,
                              mount=args.mount, open_browser=not args.no_open)

        if args.command == "new":
            manager = ProjectManager(args.root, seed_demo=False)
            project = manager.create(args.name, description=args.description, template=args.template)
            print(json.dumps(project.summary(), ensure_ascii=False, indent=2))
            return 0
        if args.command == "demo":
            manager = ProjectManager(args.root, seed_demo=False)
            project = manager.create("PredActor", template="demo", demo=True,
                                     description="A demo research project: from claims to agent tasks.")
            print(json.dumps(project.summary(), ensure_ascii=False, indent=2))
            return 0
        if args.command == "export":
            manager = _open_store(args.project)
            project = manager.open(str(Path(args.project).resolve()))
            if args.format == "bundle":
                from . import markdown_bundle
                bundle = markdown_bundle.MarkdownBundle(
                    project.store, project_path=project.path, project_name=project.name,
                    resolve_media=markdown_bundle.local_media_resolver(
                        project.store, project.path, [Path(p) for p in args.media_root]))
                bundle.collect(markdown_bundle.parse_refs(args.units), depth=args.depth)
                out = Path(args.out) if args.out else None
                if out is None or out.is_dir():
                    folder = out or Path.cwd()
                    out = folder / "bundle.zip"
                    manifest = bundle.write_zip(out)
                    final = folder / markdown_bundle.bundle_filename(manifest)
                    out.replace(final)
                    out = final
                else:
                    manifest = bundle.write_zip(out)
                missing = [f for f in manifest["files"] if f.get("error")]
                print(json.dumps({"path": str(out), "nodes": len(manifest["nodes"]),
                                  "files": len(manifest["files"]) - len(missing), "missing": missing},
                                 ensure_ascii=False, indent=2))
                return 0
            if args.format == "json":
                data = json.dumps(export.project_json(project.store), ensure_ascii=False, indent=2)
            elif args.format == "markdown":
                data = export.project_markdown(project.store)
            elif args.format == "csv":
                data = export.project_csv(project.store)
            else:
                data = export.graph_svg(project.store)
            if args.out:
                Path(args.out).write_text(data, encoding="utf-8")
                print(args.out)
            else:
                print(data)
            return 0
        if args.command == "agent" and getattr(args, "agent_command", None) == "context":
            if args.project:
                manager = _open_store(args.project)
                project = manager.open(str(Path(args.project).resolve()))
                print(json.dumps(context(project.store), ensure_ascii=False, indent=2))
                return 0
            manager = ProjectManager(args.root, seed_demo=False)
            projects = manager.list_projects()
            if not projects:
                print("No projects. Run `python -m arkstudio serve` to create the demo.", file=sys.stderr)
                return 2
            project = manager.open(projects[0]["path"])
            print(json.dumps(context(project.store), ensure_ascii=False, indent=2))
            return 0
        if args.command == "skills":
            from . import skills as skills_pkg
            return _run_skills_cli(args, skills_pkg)
        if args.command == "session-watch":
            try:
                return session_watch.run(args)
            except KeyboardInterrupt:
                return 0
    except (ProjectError, StoreError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    parser.print_help()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
