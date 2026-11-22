// Open a resource location in VS Code / Cursor or over SSH (Resources graph
// and Settings > Resources).  Pure, no DOM: which actions a location has,
// and the URLs and commands they use.  The desktop app builds the terminal
// command itself from the same parts (desktop/src-tauri/src/remote_open.rs);
// both refuse the same bad input.
//
// A connection is { alias?, host?, user?, port? }: an ~/.ssh/config alias
// and/or the address (GET /api/resources/connect; never a key or password).

const HOST_RE = /^[A-Za-z0-9_][A-Za-z0-9._:%-]{0,252}$/;
const USER_RE = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$/;
const ALIAS_RE = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$/;
// Control characters (newlines included) are never part of a path we use.
const CONTROL_RE = /[\u0000-\u001f\u007f-\u009f]/;
const MAX_PATH = 1024;

/** Device kinds one can SSH into. */
export const SSH_KINDS = new Set(["ark", "gpu-server", "host"]);

export const validHost = (s) => typeof s === "string" && HOST_RE.test(s);
export const validUser = (s) => typeof s === "string" && USER_RE.test(s);
export const validAlias = (s) => typeof s === "string" && ALIAS_RE.test(s);

const text = (v) => (typeof v === "string" ? v.trim() : v === null || v === undefined ? "" : String(v).trim());

/** A checked connection, or null when it has no usable alias / host. */
export function cleanConnection(raw) {
  if (!raw || typeof raw !== "object") return null;
  const alias = text(raw.alias);
  const host = text(raw.host);
  const user = text(raw.user);
  const port = raw.port === null || raw.port === undefined || raw.port === "" ? null : Number(raw.port);
  if (alias && !validAlias(alias)) return null;
  if (host && !validHost(host)) return null;
  if (user && !validUser(user)) return null;
  if (port !== null && !(Number.isInteger(port) && port >= 1 && port <= 65535)) return null;
  if (!alias && !host) return null;
  const out = {};
  if (alias) out.alias = alias;
  if (host) out.host = host;
  if (user) out.user = user;
  if (port !== null) out.port = port;
  return out;
}

/** A usable path: text, not too long, no control characters. */
export function validPath(path) {
  return typeof path === "string" && path.length > 0 && path.length <= MAX_PATH && !CONTROL_RE.test(path);
}

/** A POSIX path on a remote host: absolute, or ~ / ~/... */
export function isRemotePosixPath(path) {
  return validPath(path) && (path.startsWith("/") || path === "~" || path.startsWith("~/"));
}

/** A Windows path: E:\x or E:/x. */
const isWindowsPath = (path) => /^[A-Za-z]:[\\/]/.test(path);

/** One word for a POSIX shell. */
export function shQuote(s) {
  return `'${String(s).replace(/'/g, "'\\''")}'`;
}

/** One word for PowerShell. */
export function psQuote(s) {
  return `'${String(s).replace(/'/g, "''")}'`;
}

/** What the remote shell runs: cd to the path, then a login shell (null for home). */
export function remoteCommand(path) {
  if (!path) return null;
  if (!isRemotePosixPath(path)) return null;
  if (path.replace(/\/+$/, "") === "~") return null;
  const target = path.startsWith("~/") ? `~/${shQuote(path.slice(2))}` : shQuote(path);
  return `cd -- ${target} && exec \${SHELL:-/bin/sh} -l`;
}

/** The ssh destination: the alias, else [user@]host. */
export function destination(conn) {
  const c = cleanConnection(conn);
  if (!c) return null;
  if (c.alias) return c.alias;
  return c.user ? `${c.user}@${c.host}` : c.host;
}

/** ssh's arguments, as the desktop app builds them (remote_open.rs ssh_args). */
export function sshArgs(conn, path = null) {
  const c = cleanConnection(conn);
  if (!c) return null;
  if (path && !isRemotePosixPath(path)) path = null;
  const args = ["-t"];
  if (!c.alias && c.port && c.port !== 22) args.push("-p", String(c.port));
  args.push("--", destination(c));
  const cmd = remoteCommand(path);
  if (cmd) args.push(cmd);
  return args;
}

/**
 * The command to paste into a terminal.  shell "powershell" (Windows) or
 * "posix" (macOS, Linux, Git Bash).
 */
export function sshCommand(conn, path = null, { shell = "posix" } = {}) {
  const args = sshArgs(conn, path);
  if (!args) return null;
  const quote = shell === "powershell" ? psQuote : shQuote;
  // Plain words (options, destination) need no quotes.
  return ["ssh", ...args.map((a) => (/^[A-Za-z0-9_@%+=:,./-]+$/.test(a) ? a : quote(a)))].join(" ");
}

const hex = (s) => Array.from(new TextEncoder().encode(s), (b) => b.toString(16).padStart(2, "0")).join("");

/**
 * VS Code Remote-SSH authority (after "ssh-remote+"): the alias, user@host
 * on port 22, else the hex-encoded {"hostName", "user", "port"} form
 * Remote-SSH accepts for hosts that are not in ~/.ssh/config.
 */
export function remoteAuthority(conn) {
  const c = cleanConnection(conn);
  if (!c) return null;
  if (c.alias) return c.alias;
  if (!c.port || c.port === 22) return c.user ? `${c.user}@${c.host}` : c.host;
  const spec = { hostName: c.host };
  if (c.user) spec.user = c.user;
  spec.port = c.port;
  return hex(JSON.stringify(spec));
}

/**
 * The absolute folder VS Code opens on the remote host.  ~ paths need the
 * home folder: /root for root, /home/<user> otherwise (Linux servers); null
 * when the user is unknown.
 */
export function remoteFolder(path, conn) {
  if (!isRemotePosixPath(path)) return null;
  if (path.startsWith("/")) return path;
  const user = cleanConnection(conn)?.user;
  if (!user) return null;
  const home = user === "root" ? "/root" : `/home/${user}`;
  return path === "~" || path === "~/" ? home : `${home}/${path.slice(2)}`;
}

/** Percent-encode a path for a URL, keeping "/" and a drive's ":". */
function encodePath(path) {
  return path.split("/").map((part, i) => (i === 1 && /^[A-Za-z]:$/.test(part) ? part : encodeURIComponent(part))).join("/");
}

/** A folder on this computer as a vscode:// / cursor:// link (null for ~ or relative paths). */
export function localEditorUrl(path, editor = "vscode") {
  if (!validPath(path)) return null;
  let p = path;
  if (isWindowsPath(p)) p = `/${p.replace(/\\/g, "/")}`;
  else if (!p.startsWith("/")) return null;
  return `${editor}://file${encodePath(p)}`;
}

/** A folder on a remote host as a Remote-SSH link (null when it cannot be opened). */
export function remoteEditorUrl(conn, path, editor = "vscode") {
  const authority = remoteAuthority(conn);
  const folder = remoteFolder(path, conn);
  if (!authority || !folder) return null;
  return `${editor}://vscode-remote/ssh-remote+${authority}${encodePath(folder)}`;
}

/** The ~/.ssh/config names to look up for a device: its label and its host. */
export function configNames(device, conn) {
  const names = [device?.label, conn?.host].map(text).filter((n) => n && validHost(n));
  return [...new Set(names)];
}

/**
 * The connection to use: the server's (with the ~/.ssh/config alias of its
 * host when there is one), else an ~/.ssh/config alias matching the device.
 * `matches`: Map name (lower case) -> alias (desktop app only).
 */
export function effectiveConnection(conn, device, matches = null) {
  const c = cleanConnection(conn);
  const alias = (name) => (name && matches ? matches.get(String(name).toLowerCase()) : null) || null;
  if (c) {
    if (!c.alias && alias(c.host)) return { ...c, alias: alias(c.host), source: conn.source || "" };
    return { ...c, source: conn.source || "" };
  }
  const found = alias(device?.label);
  return found ? { alias: found, source: "ssh-config" } : null;
}

/**
 * The open actions of a device or one of its locations.
 * @param {{device: object, path?: string|null, local?: boolean, conn?: object|null,
 *   terminal?: boolean, canSave?: boolean}} input
 *   local: the location is on this computer; terminal: the desktop app can
 *   open a terminal; conn: effectiveConnection(...)
 * @returns {Array<{id: string, url?: string, path?: string, command?: object}>}
 *   ids: vscode, cursor, ssh-terminal, copy-ssh, copy-path, ssh-setup
 */
export function openActions({ device, path = null, local = false, conn = null, terminal = false, canSave = false } = {}) {
  const out = [];
  const fsPath = validPath(path) && (path.startsWith("/") || path.startsWith("~") || isWindowsPath(path)) ? path : null;
  const sshable = SSH_KINDS.has(device?.kind);
  if (local) {
    for (const editor of ["vscode", "cursor"]) {
      const url = fsPath ? localEditorUrl(fsPath, editor) : null;
      if (url) out.push({ id: editor, url });
    }
  } else if (sshable && conn && cleanConnection(conn)) {
    const remotePath = fsPath && isRemotePosixPath(fsPath) ? fsPath : null;
    // A device (no location) opens its home folder.
    const folder = path === null || path === undefined ? "~" : remotePath;
    for (const editor of ["vscode", "cursor"]) {
      const url = folder ? remoteEditorUrl(conn, folder, editor) : null;
      if (url) out.push({ id: editor, url });
    }
    const args = sshArgs(conn, remotePath);
    if (args) {
      if (terminal) out.push({ id: "ssh-terminal", command: { conn: cleanConnection(conn), path: remotePath } });
      out.push({ id: "copy-ssh", command: { conn: cleanConnection(conn), path: remotePath } });
    }
  }
  if (fsPath) out.push({ id: "copy-path", path: fsPath });
  // Set up (no connection yet: a hint) or change the viewer's own entry.
  if (!local && sshable) {
    const known = Boolean(conn && cleanConnection(conn));
    if (!known || canSave) out.push({ id: "ssh-setup", canSave, known });
  }
  return out;
}
