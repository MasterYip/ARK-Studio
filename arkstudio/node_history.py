"""Immutable, text-only node revisions in a project-local bare Git repository."""
from __future__ import annotations

import os
import re
import shutil
import subprocess
from pathlib import Path


class HistoryError(ValueError):
    pass


class NodeHistory:
    def __init__(self, path: Path):
        self.path = path

    @property
    def available(self) -> bool:
        return bool(shutil.which("git"))

    def _git(self, *args: str, data: bytes | None = None, actor: str = "ARK") -> str:
        if not self.available:
            raise HistoryError("Node history requires Git on the ARK server")
        # Neither a user's checkout settings nor inherited Git routing may
        # redirect project history, run filters, or choose the commit identity.
        env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        name = "".join(c for c in actor if c.isprintable() and c not in "<>").strip()[:120] or "ARK"
        env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                   GIT_AUTHOR_NAME=name, GIT_COMMITTER_NAME=name,
                   GIT_AUTHOR_EMAIL="ark@localhost", GIT_COMMITTER_EMAIL="ark@localhost")
        try:
            result = subprocess.run(
                ["git", "--no-pager", "-c", "core.hooksPath=" + os.devnull,
                 "-c", "core.attributesFile=" + os.devnull, "-c", "core.fsmonitor=false",
                 "--git-dir=" + str(self.path), *args],
                input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                env=env, timeout=20, check=False,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise HistoryError("Could not access node history: " + str(exc)) from exc
        if result.returncode:
            message = result.stderr.decode("utf-8", errors="replace").strip()[:500]
            raise HistoryError("Git node history failed: " + message)
        return result.stdout.decode("utf-8", errors="strict")

    def _init(self) -> None:
        if not (self.path / "HEAD").is_file():
            self._git("init", "--bare", "--quiet", "--template=", str(self.path))

    def record(self, uid: str, title: str, body: str, parent: str | None,
               actor: str, message: str = "Save text") -> str:
        self._init()
        if parent:
            self._validate_hash(parent)
        files = []
        for name, text in (("body.md", body), ("title.txt", title)):
            blob = self._git("hash-object", "-w", "--stdin", data=text.encode("utf-8")).strip()
            files.append(f"100644 blob {blob}\t{name}\n")
        tree = self._git("mktree", data="".join(files).encode("ascii")).strip()
        if parent and self._git("rev-parse", parent + "^{tree}").strip() == tree:
            return parent
        args = ["commit-tree", tree]
        if parent:
            args.extend(("-p", parent))
        commit = self._git(*args, data=f"{uid}: {message}\n".encode("utf-8"), actor=actor).strip()
        # Pin the immutable object before SQLite publishes its hash. A failed
        # SQL transaction can leave an unlisted object, never a missing revision.
        self._git("update-ref", "refs/ark/revisions/" + commit, commit)
        return commit

    @staticmethod
    def _validate_hash(commit: str) -> None:
        if not isinstance(commit, str) or not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", commit):
            raise HistoryError("A full revision hash is required")

    def validate(self, head: str, commit: str) -> str:
        self._validate_hash(commit)
        self._validate_hash(head)
        # A hash from another node (or an orphan from a failed save) is private
        # to that history and cannot be read through this node's endpoint.
        ancestor = self._git("merge-base", head, commit).strip()
        if ancestor != commit:
            raise HistoryError("Revision does not belong to this node's history")
        return commit

    def versions(self, head: str, limit: int, offset: int) -> list[dict]:
        self._validate_hash(head)
        raw = self._git("log", "--format=%H%x00%aI%x00%an%x00%s", f"--skip={offset}",
                        f"--max-count={limit}", head, "--")
        return [dict(zip(("id", "created_at", "author", "message"), line.split("\0", 3)))
                for line in raw.splitlines()]

    def revision(self, head: str, commit: str) -> dict:
        self.validate(head, commit)
        return {"id": commit, "title": self._git("show", commit + ":title.txt"),
                "body": self._git("show", commit + ":body.md")}

    def diff(self, head: str, before: str | None, after: str | None) -> dict:
        after = self.validate(head, after or head)
        if before:
            before = self.validate(head, before)
        else:
            parents = self._git("rev-list", "--parents", "--max-count=1", after).split()
            before = parents[1] if len(parents) > 1 else None
        empty = self._git("hash-object", "-t", "tree", "--stdin", data=b"").strip()
        diff = self._git("diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3",
                         before or empty, after, "--", "title.txt", "body.md")
        return {"from": before, "to": after, "diff": diff, "changes": self.change_blocks(diff)}

    @staticmethod
    def change_blocks(diff: str) -> list[dict]:
        """Locate contiguous changes using Git's own line alignment."""
        changes = []
        file = None
        in_hunk = False
        block = None
        before = after = index = 0
        for line in diff.split("\n"):
            if line.startswith("diff --git "):
                file = line.rsplit(" b/", 1)[-1]
                index = 0
                block = None
                in_hunk = False
            elif line.startswith("@@ "):
                match = re.match(r"@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@", line)
                if not match:
                    raise HistoryError("Invalid Git diff hunk")
                old, old_count, new, new_count = match.groups()
                before = int(old) - (old_count != "0")
                after = int(new) - (new_count != "0")
                block = None
                in_hunk = True
            elif in_hunk and line[:1] in ("-", "+"):
                if block is None:
                    block = {"file": file, "index": index, "before_start": before,
                             "after_start": after, "before_count": 0, "after_count": 0}
                    changes.append(block)
                    index += 1
                if line.startswith("-"):
                    block["before_count"] += 1
                    before += 1
                else:
                    block["after_count"] += 1
                    after += 1
            elif in_hunk and line.startswith(" "):
                before += 1
                after += 1
                block = None
        return changes

    @staticmethod
    def reapply_block(old: str, new: str, block: dict) -> str:
        # Split on LF exactly as Git does, retaining CRLF and a missing final LF.
        def lines(text):
            parts = text.split("\n")
            return [part + "\n" for part in parts[:-1]] + ([parts[-1]] if parts[-1] else [])
        source, target = lines(old), lines(new)
        start, count = block["after_start"], block["after_count"]
        source_start, source_count = block["before_start"], block["before_count"]
        target[start:start + count] = source[source_start:source_start + source_count]
        return "".join(target)
