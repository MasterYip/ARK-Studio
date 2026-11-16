"""Optional POSIX account selection for task-agent processes.

ARK normally runs agents as the OS account that started the server.  Provider
CLIs such as Codex and Claude Code keep their login material in the OS account's
home directory (``~/.codex/auth.json``, ``~/.claude/.credentials.json``), which
means every ARK user shares that identity.  This module lets an *organization
admin* map an ARK user to an existing Linux account, and wraps the launched
command as::

    sudo -n -E -u <account> -- /usr/bin/env HOME=<home> USER=<account> \\
        LOGNAME=<account> XDG_CONFIG_HOME=<home>/.config <agent argv...>

Design rules:

* No passwords are ever collected or stored.  ``sudo -n`` fails closed when the
  sudoers policy is missing, so ARK reports an actionable setup error instead of
  silently running as the server account.
* ``root`` and system accounts (uid below ``ARK_LINUX_UID_MIN``, default 1000)
  are rejected.  The server's own account is rejected as a mapping because it
  provides no identity separation.
* The target account must be able to write the session workspace; a failed
  preflight (``sudo`` unavailable, ``sudo -n`` denied, or workspace not
  writable) aborts the launch.  There is no fallback.
* This module never validates or stores a password; it only executes the
  non-interactive wrapper after an admin assigned the account.

Sudoers example (replace ``ark`` and ``alice``)::

    ark ALL=(alice) NOPASSWD: SETENV: /usr/bin/env

The ``SETENV`` tag is required because ARK preserves the user's own filtered
task environment (personal credentials) while overriding HOME/LOGNAME.  The
filesystem must also allow the target account to write the session workspace,
for example ``setfacl -R -m u:alice:rwX <session-workspace>`` or a shared group.
"""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

ACCOUNT_RE = re.compile(r"^[a-z_][a-z0-9_-]{0,31}$")
DEFAULT_UID_MIN = 1000
SYSTEM_ACCOUNTS = frozenset({
    "root", "daemon", "bin", "sys", "sync", "games", "man", "lp", "mail",
    "news", "uucp", "proxy", "www-data", "backup", "list", "irc", "gnats",
    "nobody", "systemd-network", "systemd-resolve", "systemd-timesync",
    "messagebus", "sshd", "polkitd", "rtkit", "colord", "gdm", "saned",
})
SUDO_HINT = (
    "Configure passwordless sudo for the target account, e.g. "
    "`ark ALL=(alice) NOPASSWD: SETENV: /usr/bin/env` in /etc/sudoers.d/ark, "
    "and let that account write the session workspace "
    "(`setfacl -R -m u:alice:rwX <workspace>` or a shared group)."
)


class LinuxAccountError(ValueError):
    """A clean Linux-account error with an HTTP status and machine code."""

    def __init__(self, message: str, status: int = 400,
                 code: str = "LINUX_ACCOUNT_INVALID"):
        super().__init__(message)
        self.message = message
        self.status = status
        self.code = code


def is_linux() -> bool:
    return sys.platform.startswith("linux")


def uid_min() -> int:
    try:
        value = int(os.environ.get("ARK_LINUX_UID_MIN") or DEFAULT_UID_MIN)
    except (TypeError, ValueError):
        value = DEFAULT_UID_MIN
    return max(0, value)


def sudo_binary() -> str:
    override = str(os.environ.get("ARK_SUDO_BINARY") or "").strip()
    if override:
        return override
    return shutil.which("sudo") or ""


def _lookup(account: str, resolver=None) -> dict:
    if resolver is not None:
        entry = resolver(account)
        if not entry:
            return {}
        return dict(entry)
    if not is_linux():
        raise LinuxAccountError(
            "Linux account selection is only available on a Linux host.",
            400, "LINUX_ACCOUNT_UNAVAILABLE")
    try:
        import pwd  # POSIX only

        row = pwd.getpwnam(account)
    except (ImportError, KeyError):
        return {}
    return {"uid": row.pw_uid, "gid": row.pw_gid, "home": row.pw_dir,
            "name": row.pw_name}


def validate_linux_account(account: str, *, resolver=None,
                           minimum_uid: int | None = None,
                           server_user: str | None = None) -> dict:
    """Validate an existing, non-root, non-system Linux account.

    Returns ``{account, uid, home}``; raises :class:`LinuxAccountError` with an
    actionable message otherwise.  ``resolver`` is injectable for tests and for
    hosts that expose accounts through a directory service.
    """
    text = str(account or "").strip()
    if not text:
        raise LinuxAccountError("Linux account is required.", 400,
                                "LINUX_ACCOUNT_REQUIRED")
    if len(text) > 32 or not ACCOUNT_RE.fullmatch(text):
        raise LinuxAccountError(
            "Linux account must be a plain lowercase account name "
            "(letters, digits, _ or -, up to 32 characters).",
            400, "LINUX_ACCOUNT_INVALID")
    if text in SYSTEM_ACCOUNTS:
        raise LinuxAccountError(
            f"'{text}' is a system account and cannot be used for agent isolation.",
            400, "LINUX_ACCOUNT_SYSTEM")
    entry = _lookup(text, resolver)
    if not entry:
        raise LinuxAccountError(
            f"Linux account '{text}' does not exist on this host.",
            400, "LINUX_ACCOUNT_NOT_FOUND")
    try:
        uid = int(entry.get("uid"))
    except (TypeError, ValueError) as exc:
        raise LinuxAccountError(
            f"Linux account '{text}' has no usable uid.", 400,
            "LINUX_ACCOUNT_INVALID") from exc
    if uid == 0:
        raise LinuxAccountError(
            "root cannot be used for agent isolation.", 400,
            "LINUX_ACCOUNT_ROOT")
    limit = uid_min() if minimum_uid is None else int(minimum_uid)
    if uid < limit:
        raise LinuxAccountError(
            f"'{text}' is a system account (uid {uid} < {limit}); choose a "
            "regular login account.", 400, "LINUX_ACCOUNT_SYSTEM")
    current = str(server_user or os.environ.get("USER") or
                  os.environ.get("LOGNAME") or "").strip()
    if current and text == current:
        raise LinuxAccountError(
            f"'{text}' is the account running ARK; choose a different account "
            "so provider credentials are isolated.", 400,
            "LINUX_ACCOUNT_SERVER_ACCOUNT")
    home = str(entry.get("home") or "").strip()
    if not home or not home.startswith("/"):
        raise LinuxAccountError(
            f"Linux account '{text}' has no absolute home directory.",
            400, "LINUX_ACCOUNT_INVALID")
    return {"account": text, "uid": uid, "home": home.rstrip("/") or "/"}


def wrap_command(argv: list[str], account: str, *, home: str | None = None) -> list[str]:
    """Return the fixed, password-free sudo wrapper around ``argv``.

    The target account's HOME/XDG_CONFIG_HOME are set *inside* sudo via
    ``env`` so provider CLIs read their own config while the caller's already
    filtered environment (personal credentials) is preserved by ``-E``.
    """
    target = str(account or "").strip()
    if not target:
        raise LinuxAccountError("Linux account is required.", 400,
                                "LINUX_ACCOUNT_REQUIRED")
    if not argv:
        raise LinuxAccountError("agent command is empty.", 400,
                                "LINUX_COMMAND_EMPTY")
    home = str(home or f"/home/{target}").rstrip("/") or "/"
    sudo = sudo_binary() or "sudo"
    return [
        sudo, "-n", "-E", "-u", target, "--", "/usr/bin/env",
        f"HOME={home}",
        f"USER={target}",
        f"LOGNAME={target}",
        f"XDG_CONFIG_HOME={home}/.config",
        *[str(part) for part in argv],
    ]


def preflight(account: str, *, workspace: str | Path | None = None,
              resolver=None, timeout: float = 8.0, runner=None) -> dict:
    """Prove the sudo wrapper works and the workspace is writable, or fail.

    Raises :class:`LinuxAccountError` with an actionable setup message.  This
    function is deliberately called before every launch so a revoked sudoers
    rule or a permission change never silently falls back to the server
    account.
    """
    info = validate_linux_account(account, resolver=resolver)
    sudo = sudo_binary()
    if not sudo:
        raise LinuxAccountError(
            "sudo is not installed or not on PATH on this host. " + SUDO_HINT,
            409, "LINUX_SUDO_UNAVAILABLE")
    run = runner or subprocess.run
    base = wrap_command(["/bin/true"], info["account"], home=info["home"])
    try:
        result = run(base, capture_output=True, text=True, timeout=timeout,
                     check=False)
    except (OSError, subprocess.SubprocessError) as exc:
        raise LinuxAccountError(
            f"could not execute sudo for '{info['account']}': {exc}. " + SUDO_HINT,
            409, "LINUX_SUDO_FAILED") from exc
    returncode = int(getattr(result, "returncode", 1))
    if returncode != 0:
        detail = str(getattr(result, "stderr", "") or "").strip()[:400]
        raise LinuxAccountError(
            f"passwordless sudo as '{info['account']}' failed"
            + (f": {detail}" if detail else ".")
            + " " + SUDO_HINT,
            409, "LINUX_SUDO_DENIED")
    if workspace is not None:
        target = Path(workspace)
        argv = wrap_command(["/bin/test", "-w", str(target)],
                            info["account"], home=info["home"])
        try:
            probe = run(argv, capture_output=True, text=True, timeout=timeout,
                        check=False)
        except (OSError, subprocess.SubprocessError) as exc:
            raise LinuxAccountError(
                f"could not verify workspace permissions: {exc}. " + SUDO_HINT,
                409, "LINUX_WORKSPACE_CHECK_FAILED") from exc
        if int(getattr(probe, "returncode", 1)) != 0:
            raise LinuxAccountError(
                f"Linux account '{info['account']}' cannot write the session "
                f"workspace ({target}). " + SUDO_HINT,
                409, "LINUX_WORKSPACE_NOT_WRITABLE")
    info["sudo"] = sudo
    return info


def available() -> dict:
    """Capability view for the Personal/Admin settings UI."""
    sudo = sudo_binary()
    return {
        "platform": sys.platform,
        "available": is_linux() and bool(sudo),
        "linux": is_linux(),
        "sudo": sudo,
        "uid_min": uid_min(),
        "hint": SUDO_HINT,
    }
