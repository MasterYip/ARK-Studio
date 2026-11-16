"""Git worktree manager for project-scoped agent sessions.

Worktrees are created under a **safe root** derived from the project's Git
repository, never at an arbitrary path supplied by the caller:

    ``$ARK_AGENT_WORKTREE_ROOT`` (absolute) when set, otherwise
    ``<repository-parent>/<repository-name>-worktrees``

The default sibling root keeps the main checkout clean: a root inside the
repository would itself show up as an untracked directory and make later
"is the base clean?" checks noisy.  Every mutating operation validates that the
target is a *linked* worktree under that root, and deletion requires explicit
confirmation from the caller (which the UI collects in a danger dialog).  ARK
never falls back to ``rmdir``/``rmtree``: removal is delegated to
``git worktree remove``.

All Git access goes through an injectable ``run_git(args, cwd)`` callable so the
manager can be unit-tested without touching the filesystem or a real repository.
"""
from __future__ import annotations

import os
import re
import subprocess
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

DEFAULT_BRANCH_PREFIX = "agent/"
MAX_BRANCH_LENGTH = 100
MAX_NAME_LENGTH = 48
MAX_WORKTREES = 12
GIT_TIMEOUT = 20

_SAFE_BRANCH = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$")
_BAD_SEGMENT = re.compile(r"(^\.)|(\.\.)|(\.lock$)|(@\{)|(//)|([\\\s~^:?*\[\]])")


class WorktreeError(ValueError):
    """A worktree request that the API can report as a clean 4xx."""

    def __init__(self, message: str, status: int = 400, code: str = "WORKTREE_ERROR"):
        super().__init__(message)
        self.status = status
        self.code = code


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _git_result(returncode: int, stdout: str = "", stderr: str = "") -> dict:
    return {"returncode": int(returncode), "stdout": stdout, "stderr": stderr}


def run_git_command(args: list[str], cwd: str | Path) -> dict:
    """Run Git with an argument vector.  No shell, no environment injection."""
    try:
        completed = subprocess.run(
            ["git", *[str(arg) for arg in args]],
            cwd=str(cwd),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=GIT_TIMEOUT,
            shell=False,
            check=False,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
    except subprocess.TimeoutExpired:
        return _git_result(124, "", "git timed out")
    except (OSError, ValueError) as exc:
        return _git_result(127, "", str(exc))
    return _git_result(completed.returncode, completed.stdout or "", completed.stderr or "")


def _inside(path: Path, root: Path) -> bool:
    """True when ``path`` is ``root`` or a descendant (case-safe on Windows)."""
    try:
        return path == root or root in path.parents
    except (OSError, ValueError):  # pragma: no cover - defensive
        return False


def _resolve(path: str | Path) -> Path:
    try:
        return Path(path).expanduser().resolve()
    except (OSError, ValueError) as exc:
        raise WorktreeError(f"invalid worktree path: {exc}") from exc


def slugify_name(value: Any) -> str:
    text = re.sub(r"[^a-z0-9]+", "-", str(value or "").lower()).strip("-")
    text = re.sub(r"-{2,}", "-", text)[:MAX_NAME_LENGTH].strip("-")
    if not text:
        raise WorktreeError("a worktree name with at least one letter or digit is required",
                            code="INVALID_WORKTREE_NAME")
    return text


def validate_branch(value: Any) -> str:
    branch = str(value or "").strip()
    if not branch:
        raise WorktreeError("branch name is required", code="INVALID_BRANCH")
    if len(branch) > MAX_BRANCH_LENGTH:
        raise WorktreeError("branch name is too long", code="INVALID_BRANCH")
    if (
        branch.startswith("-")
        or branch.startswith("/")
        or branch.endswith("/")
        or branch.endswith(".")
        or branch == "HEAD"
        or _BAD_SEGMENT.search(branch)
        or not _SAFE_BRANCH.fullmatch(branch)
    ):
        raise WorktreeError(f"invalid branch name: {branch}", code="INVALID_BRANCH")
    for segment in branch.split("/"):
        if not segment or segment.startswith(".") or segment.endswith(".") or segment.endswith(".lock"):
            raise WorktreeError(f"invalid branch name: {branch}", code="INVALID_BRANCH")
    return branch


def sanitize_directory(branch: str) -> str:
    """``agent/feature-x`` -> ``agent-feature-x`` (filesystem-safe, stable)."""
    value = re.sub(r"[\\/:*?\"<>|\s]+", "-", branch).strip("-.")
    value = re.sub(r"-{2,}", "-", value)[:80].strip("-")
    if not value:
        raise WorktreeError("branch name cannot be converted to a folder name",
                            code="INVALID_WORKTREE_FOLDER")
    return value


def parse_worktree_porcelain(text: str) -> list[dict]:
    """Parse ``git worktree list --porcelain`` into ordered entries."""
    entries: list[dict] = []
    current: dict[str, Any] = {}
    for raw in str(text or "").splitlines() + [""]:
        line = raw.rstrip("\r")
        if not line.strip():
            if current:
                entries.append(current)
                current = {}
            continue
        key, _, value = line.partition(" ")
        if key == "worktree":
            if current:
                entries.append(current)
                current = {}
            current["path"] = value.strip()
        elif key == "HEAD":
            current["head"] = value.strip()
        elif key == "branch":
            current["ref"] = value.strip()
        elif key == "detached":
            current["detached"] = True
        elif key == "locked":
            current["locked"] = True
            current["locked_reason"] = value.strip()
        elif key == "prunable":
            current["prunable"] = True
            current["prunable_reason"] = value.strip()
    for entry in entries:
        ref = str(entry.pop("ref", "") or "")
        entry["branch"] = ref[len("refs/heads/"):] if ref.startswith("refs/heads/") else ref
        entry["detached"] = bool(entry.get("detached")) or not entry["branch"]
        entry["head"] = entry.get("head") or ""
        entry["head_short"] = entry["head"][:12]
    return entries


class WorktreeManager:
    """Project-scoped linked-worktree lifecycle."""

    def __init__(
        self,
        *,
        project_provider: Callable[[], dict],
        root: str | Path | None = None,
        env: dict | None = None,
        run_git: Callable[[list[str], str | Path], dict] | None = None,
        branch_prefix: str | None = None,
        max_worktrees: int = MAX_WORKTREES,
    ):
        self.project_provider = project_provider
        self.env = dict(os.environ if env is None else env)
        self._run_git = run_git or run_git_command
        self.branch_prefix = str(branch_prefix or self.env.get("ARK_AGENT_WORKTREE_BRANCH_PREFIX")
                                 or DEFAULT_BRANCH_PREFIX)
        self.root_override = Path(str(root)).expanduser() if root else None
        self.max_worktrees = max(1, min(int(max_worktrees), 100))
        self._lock = threading.RLock()

    # ---------------------------------------------------------------- helpers
    def _project_path(self) -> Path:
        project = dict(self.project_provider() or {})
        raw = str(project.get("path") or "").strip()
        if not raw:
            raise WorktreeError("no ARK project is open", 409, "NO_PROJECT")
        return _resolve(raw)

    def _git(self, args: list[str], cwd: str | Path) -> dict:
        result = self._run_git([str(arg) for arg in args], Path(cwd))
        if not isinstance(result, dict):
            raise WorktreeError("invalid git runner result", 500, "GIT_RUNNER")
        return result

    def _require_ok(self, result: dict, what: str, code: str = "GIT_FAILED") -> str:
        if result.get("returncode") != 0:
            detail = str(result.get("stderr") or result.get("stdout") or "").strip()
            detail = detail.splitlines()[-1][:240] if detail else f"exit {result.get('returncode')}"
            raise WorktreeError(f"{what} failed: {detail}", 409, code)
        return str(result.get("stdout") or "")

    def repository(self) -> dict:
        """Resolve the Git repository containing the open ARK project."""
        project = self._project_path()
        result = self._git(["rev-parse", "--show-toplevel"], project)
        if result.get("returncode") != 0:
            raise WorktreeError(
                "The current ARK project is not inside a Git repository. "
                "Open a project that lives in a checkout to create agent worktrees.",
                409,
                "NOT_A_GIT_REPOSITORY",
            )
        root = _resolve(str(result.get("stdout") or "").strip())
        if not root.is_dir():
            raise WorktreeError("git reported a repository root that does not exist", 409, "NOT_A_GIT_REPOSITORY")
        branch = ""
        head = ""
        current = self._git(["rev-parse", "--abbrev-ref", "HEAD"], root)
        if current.get("returncode") == 0:
            branch = str(current.get("stdout") or "").strip()
        head_result = self._git(["rev-parse", "--short", "HEAD"], root)
        if head_result.get("returncode") == 0:
            head = str(head_result.get("stdout") or "").strip()
        return {"root": str(root), "name": root.name, "branch": "" if branch == "HEAD" else branch, "head": head}

    def safe_root(self, repository: dict | None = None) -> Path:
        repo = repository or self.repository()
        root = self.root_override
        if root is None:
            env_root = str(self.env.get("ARK_AGENT_WORKTREE_ROOT") or "").strip()
            if env_root:
                root = Path(env_root).expanduser()
                if not root.is_absolute():
                    root = Path(repo["root"]) / root
            else:
                repo_root = Path(repo["root"])
                root = repo_root.parent / f"{repo_root.name}-worktrees"
        resolved = _resolve(root)
        if resolved == Path(repo["root"]).resolve():
            raise WorktreeError("the worktree root cannot be the repository root", 409, "UNSAFE_ROOT")
        return resolved

    def _entries(self, repo_root: Path) -> list[dict]:
        result = self._git(["worktree", "list", "--porcelain"], repo_root)
        self._require_ok(result, "listing worktrees", "GIT_WORKTREE_LIST")
        return parse_worktree_porcelain(str(result.get("stdout") or ""))

    def _status_count(self, path: Path) -> int | None:
        if not path.exists():
            return None
        result = self._git(["status", "--porcelain=v1", "-z"], path)
        if result.get("returncode") != 0:
            return None
        raw = str(result.get("stdout") or "")
        return len([entry for entry in raw.split("\x00") if entry.strip()])

    def _describe(self, entry: dict, *, repo_root: Path, root: Path, main_branch: str = "") -> dict:
        path = _resolve(entry.get("path") or "")
        dirty = self._status_count(path)
        managed = _inside(path, root) and path != repo_root
        main = path == repo_root
        return {
            "path": str(path),
            "name": path.name,
            "branch": "" if main else entry.get("branch") or "",
            "main_branch": main_branch if main else "",
            "head": entry.get("head_short") or "",
            "main": main,
            "managed": managed,
            "detached": bool(entry.get("detached")),
            "locked": bool(entry.get("locked")),
            "prunable": bool(entry.get("prunable")),
            "missing": not path.exists(),
            "dirty": dirty,
            "clean": dirty == 0,
            "active_sessions": [],
            "status": "main" if main else ("missing" if not path.exists() else ("dirty" if dirty else "clean")),
        }

    # ------------------------------------------------------------------- read
    def list(self) -> dict:
        try:
            repo = self.repository()
        except WorktreeError as exc:
            return {
                "available": False,
                "reason": str(exc),
                "code": exc.code,
                "repository": None,
                "root": "",
                "worktrees": [],
                "total": 0,
                "generated_at": utc_now(),
            }
        repo_root = _resolve(repo["root"])
        try:
            root = self.safe_root(repo)
        except WorktreeError as exc:
            return {
                "available": False,
                "reason": str(exc),
                "code": exc.code,
                "repository": repo,
                "root": "",
                "worktrees": [],
                "total": 0,
                "generated_at": utc_now(),
            }
        entries = self._entries(repo_root)
        main_branch = next((entry.get("branch") or "" for entry in entries if _resolve(entry["path"]) == repo_root), "")
        worktrees = [
            self._describe(entry, repo_root=repo_root, root=root, main_branch=main_branch)
            for entry in entries
        ]
        managed = [item for item in worktrees if item["managed"]]
        return {
            "available": True,
            "reason": "",
            "repository": {**repo, "root": str(repo_root), "main_branch": main_branch},
            "root": str(root),
            "root_exists": root.exists(),
            "worktrees": worktrees,
            "managed": managed,
            "total": len(worktrees),
            "managed_total": len(managed),
            "dirty_total": sum(1 for item in managed if item.get("dirty")),
            "generated_at": utc_now(),
        }

    def resolve_session_target(self, ref: Any) -> dict:
        """Resolve a UI worktree reference (path, branch or folder name).

        Only linked worktrees under the safe root are accepted; the main
        checkout is always rejected so an agent can never be launched into it by
        accident.
        """
        raw = str(ref or "").strip()
        if not raw:
            raise WorktreeError("a worktree reference is required", code="WORKTREE_REQUIRED")
        listing = self.list()
        if not listing.get("available"):
            raise WorktreeError(listing.get("reason") or "worktrees are unavailable",
                               409, listing.get("code") or "WORKTREES_UNAVAILABLE")
        target = Path(raw)
        candidates = []
        for item in listing["worktrees"]:
            if item["main"]:
                continue
            if not item.get("managed"):
                continue
            if raw in {item["path"], item["name"], item.get("branch") or ""}:
                candidates.append(item)
            elif target.is_absolute():
                try:
                    if _resolve(item["path"]) == target.resolve():
                        candidates.append(item)
                except (OSError, ValueError):
                    continue
        if not candidates:
            raise WorktreeError(f"worktree not found under the managed root: {raw}", 404, "WORKTREE_NOT_FOUND")
        item = dict(candidates[0])
        if item.get("missing"):
            raise WorktreeError(f"worktree folder is missing: {item['path']}", 409, "WORKTREE_MISSING")
        item["repository"] = listing.get("repository") or {}
        item["root"] = listing.get("root") or ""
        return item

    # ----------------------------------------------------------------- create
    def create(self, body: dict) -> dict:
        body = body if isinstance(body, dict) else {}
        with self._lock:
            repo = self.repository()
            repo_root = _resolve(repo["root"])
            root = self.safe_root(repo)
            entries = self._entries(repo_root)
            main_entry = next((entry for entry in entries if _resolve(entry["path"]) == repo_root), None)
            if main_entry is None:
                raise WorktreeError("cannot locate the main worktree", 409, "GIT_WORKTREE_LIST")
            main_branch = main_entry.get("branch") or repo.get("branch") or ""
            base = str(body.get("base") or "").strip()
            if base:
                base = validate_branch(base)
            else:
                base = main_branch
                if not base:
                    base = str(main_entry.get("head") or "HEAD")

            name = str(body.get("name") or "").strip()
            branch_input = str(body.get("branch") or "").strip()
            if branch_input:
                branch = validate_branch(branch_input)
            else:
                branch = validate_branch(f"{self.branch_prefix}{slugify_name(name)}")
            if branch == main_branch:
                raise WorktreeError("a worktree cannot check out the main branch", 409, "BRANCH_IS_MAIN")

            managed = [item for item in entries
                       if _inside(_resolve(item.get("path") or ""), root)
                       and _resolve(item.get("path") or "") != repo_root]
            if len(managed) >= self.max_worktrees:
                raise WorktreeError(f"the managed root already has {len(managed)} worktrees "
                                    f"(limit {self.max_worktrees})", 409, "WORKTREE_LIMIT")

            checked_out = next((item for item in entries
                                if not item.get("detached") and item.get("branch") == branch), None)
            if checked_out:
                where = checked_out.get("path") or "another worktree"
                raise WorktreeError(f"branch {branch} is already checked out at {where}",
                                    409, "BRANCH_ALREADY_CHECKED_OUT")
            target = _resolve(root / sanitize_directory(branch))
            if not _inside(target, root):
                raise WorktreeError("resolved worktree escaped the safe root", 409, "UNSAFE_PATH")
            if target.exists():
                raise WorktreeError(f"folder already exists: {target}", 409, "WORKTREE_FOLDER_EXISTS")
            dirty = self._status_count(repo_root)
            if dirty is None:
                raise WorktreeError("could not check whether the repository is clean", 409, "GIT_STATUS")
            if dirty:
                raise WorktreeError(
                    f"the base checkout has {dirty} uncommitted change{'s' if dirty != 1 else ''}; "
                    "commit or stash them before creating an agent worktree",
                    409,
                    "WORKTREE_BASE_DIRTY",
                )
            base_check = self._git(["rev-parse", "--verify", f"{base}^{{commit}}"], repo_root)
            if base_check.get("returncode") != 0:
                raise WorktreeError(f"base ref not found: {base}", 400, "BASE_NOT_FOUND")
            branch_list = self._git(["branch", "--list", branch, "--format=%(refname:short)"], repo_root)
            self._require_ok(branch_list, "checking the branch", "GIT_BRANCH_LIST")
            branch_exists = any(line.strip() == branch for line in str(branch_list.get("stdout") or "").splitlines())
            root.mkdir(parents=True, exist_ok=True)
            if branch_exists:
                result = self._git(["worktree", "add", str(target), branch], repo_root)
            else:
                result = self._git(["worktree", "add", str(target), "-b", branch, base], repo_root)
            self._require_ok(result, f"creating worktree {branch}", "GIT_WORKTREE_ADD")
            if not target.is_dir():
                raise WorktreeError("git reported success but the worktree folder is missing", 500, "WORKTREE_VERIFY")
            listing = self.list()
            created = next((item for item in listing.get("worktrees", [])
                            if _resolve(item["path"]) == target), None)
            if created is None:
                created = {
                    "path": str(target), "name": target.name, "branch": branch,
                    "head": "", "main": False, "managed": True, "detached": False,
                    "locked": False, "prunable": False, "missing": False,
                    "dirty": 0, "clean": True, "status": "clean", "active_sessions": [],
                }
            return {
                "created": True,
                "created_branch": not branch_exists,
                "reused_branch": branch_exists,
                "base": base,
                "repository": listing.get("repository") or repo,
                "root": str(root),
                "worktree": created,
            }

    # ----------------------------------------------------------------- remove
    def remove(self, body: dict) -> dict:
        body = body if isinstance(body, dict) else {}
        if body.get("confirm") is not True:
            raise WorktreeError(
                "removing a worktree requires explicit confirmation", 400, "CONFIRM_REQUIRED")
        raw = str(body.get("path") or body.get("name") or body.get("branch") or "").strip()
        if not raw:
            raise WorktreeError("worktree path is required", 400, "WORKTREE_REQUIRED")
        force = bool(body.get("force"))
        delete_branch = bool(body.get("delete_branch"))
        with self._lock:
            listing = self.list()
            if not listing.get("available"):
                raise WorktreeError(listing.get("reason") or "worktrees are unavailable",
                                    409, listing.get("code") or "WORKTREES_UNAVAILABLE")
            repo_root = _resolve(listing["repository"]["root"])
            target = None
            for item in listing["worktrees"]:
                if item["main"] or not item.get("managed"):
                    continue
                if raw in {item["path"], item["name"], item.get("branch") or ""}:
                    target = item
                    break
            if target is None:
                raise WorktreeError(f"managed worktree not found: {raw}", 404, "WORKTREE_NOT_FOUND")
            if _resolve(target["path"]) == repo_root:
                raise WorktreeError("the main worktree cannot be removed", 400, "WORKTREE_MAIN")
            if not _inside(_resolve(target["path"]), _resolve(listing["root"])):
                raise WorktreeError("refusing to remove a path outside the managed root",
                                    409, "UNSAFE_PATH")
            if target.get("missing"):
                raise WorktreeError("the worktree folder is already missing; use Git to prune it",
                                    409, "WORKTREE_MISSING")
            dirty = self._status_count(_resolve(target["path"]))
            if dirty is None:
                raise WorktreeError("could not check the worktree status", 409, "GIT_STATUS")
            if dirty and not force:
                raise WorktreeError(
                    f"worktree has {dirty} uncommitted change{'s' if dirty != 1 else ''}; "
                    "commit or discard them, or confirm a forced removal",
                    409,
                    "WORKTREE_DIRTY",
                )
            args = ["worktree", "remove"]
            if force:
                args.append("--force")
            args.append(str(target["path"]))
            result = self._git(args, repo_root)
            self._require_ok(result, "removing the worktree", "GIT_WORKTREE_REMOVE")
            branch_deleted = False
            branch = str(target.get("branch") or "")
            if delete_branch and branch:
                branch_result = self._git(["branch", "-D", branch], repo_root)
                branch_deleted = branch_result.get("returncode") == 0
            return {
                "removed": True,
                "forced": force,
                "path": target["path"],
                "branch": branch,
                "branch_deleted": branch_deleted,
                "dirty_at_removal": dirty,
                "generated_at": utc_now(),
            }
