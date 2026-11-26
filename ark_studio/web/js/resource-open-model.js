// Open a resource location in VS Code / Cursor or over SSH (Resources graph
// and Settings > Resources).  Pure, no DOM: which actions a location has,
// and the URLs and commands they use.  The desktop app builds the terminal
// command itself from the same parts (desktop/src-tauri/src/remote_open.rs);
// both refuse the same bad input.
//
// A connection is { alias?, host?, user?, port?, auth? }: an ~/.ssh/config
// alias and/or the address, and "key" or "password" login
// (GET /api/resources/connect; never a key or password).

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
  // How the person logs in (a mode only; see authMode).
  if (raw.auth === "key" || raw.auth === "password") out.auth = raw.auth;
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
  // Key login without an alias: this key only (an alias brings its own).
  // Password login: nothing; ssh prompts in the terminal.
  const key = text(conn?.identityFile);
  if (key && !c.alias && authMode(conn) === "key") {
    if (!validIdentity(key)) return null;
    args.push("-i", key, "-o", "IdentitiesOnly=yes");
  }
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

// ---------------------------------------------------- managed ~/.ssh/config hosts
// VS Code Remote-SSH (and plain ssh) only use a key, a port and a user that
// ~/.ssh/config gives a Host alias.  ARK keeps `Host ark-<name>` entries in
// a marked block; the desktop app writes it (remote_open.rs upsert_managed),
// a browser shows the same text to paste.

export const MANAGED_BEGIN = "# >>> ARK Studio managed hosts (edits inside are replaced) >>>";
export const MANAGED_END = "# <<< ARK Studio managed hosts <<<";

/**
 * How a person logs in to a server: "key" or "password".  An explicit
 * choice ({auth}) wins; else a key file path means "key".  ARK only ever
 * stores the mode and the key's path: passwords are typed into ssh or VS
 * Code and never pass through ARK.
 */
export function authMode({ auth = "", identityFile = "" } = {}) {
  const a = text(auth);
  if (a === "key" || a === "password") return a;
  return text(identityFile) ? "key" : "password";
}

/** A key file path for `ssh -i`: absolute, no control characters, never option-like. */
export function validIdentity(raw) {
  const p = text(raw);
  if (!p || p.length > MAX_PATH || CONTROL_RE.test(p) || p.startsWith("-")) return false;
  if (p.startsWith("//") || p.startsWith("\\\\")) return false;
  return p.startsWith("/") || p.startsWith("~/") || /^[A-Za-z]:[\\/]/.test(p);
}

/**
 * Commands that set up key login for a password server: make a key, then
 * append its public half to the server's authorized_keys (what ssh-copy-id
 * does; Windows has no ssh-copy-id).  The person runs them; the second asks
 * for the password once.  null for an invalid connection.
 */
export function keySetupCommands(conn, alias, { shell = "posix" } = {}) {
  const c = cleanConnection(conn);
  const a = text(alias);
  if (!c?.host || !validAlias(a)) return null;
  const dest = c.user ? `${c.user}@${c.host}` : c.host;
  const port = c.port && c.port !== 22 ? ` -p ${c.port}` : "";
  const remote = '"umask 077; mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys"';
  if (shell === "powershell") {
    const key = `$env:USERPROFILE\\.ssh\\id_ed25519_${a}`;
    return [
      `ssh-keygen -t ed25519 -f "${key}"`,
      `Get-Content "${key}.pub" | ssh${port} ${dest} ${remote}`,
    ];
  }
  const key = `~/.ssh/id_ed25519_${a}`;
  return [
    `ssh-keygen -t ed25519 -f ${key}`,
    c.port && c.port !== 22 ? `ssh-copy-id -i ${key}.pub -p ${c.port} ${dest}` : `ssh-copy-id -i ${key}.pub ${dest}`,
  ];
}

/** The managed alias for a server / device name: ark-<name> (null when nothing usable is left). */
export function managedAlias(name) {
  const slug = text(name).toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^[-.]+|[-.]+$/g, "").slice(0, 90);
  if (!slug) return null;
  const alias = slug.startsWith("ark-") ? slug : `ark-${slug}`;
  return validAlias(alias) ? alias : null;
}

/**
 * A key file path as written after IdentityFile: absolute (/..., ~/..., C:\...),
 * no control characters or double quotes; "\" becomes "/"; quoted when it
 * has spaces.  null when unusable.
 */
export function identityForConfig(raw) {
  const p = text(raw);
  if (!p || p.length > MAX_PATH || CONTROL_RE.test(p) || p.includes('"')) return null;
  const s = p.replace(/\\/g, "/");
  if (s.startsWith("//") || !(s.startsWith("/") || s.startsWith("~/") || /^[A-Za-z]:\//.test(s))) return null;
  return /\s/.test(s) ? `"${s}"` : s;
}

/**
 * One managed entry's lines: { alias, host, user?, port?, identityFile? }.
 * null when a part is invalid (never a partial entry).
 */
export function sshConfigEntry({ alias, host, user = "", port = null, identityFile = "", auth = "" } = {}) {
  const a = text(alias);
  const c = cleanConnection({ host, user, port });
  if (!validAlias(a) || !a.startsWith("ark-") || !c?.host) return null;
  // The address is a Host name too: ssh matches Host patterns against what
  // is typed, so VS Code's direct form (user@<address>) only gets this key
  // when the address is listed (2026-11-26: "Permission denied (publickey)").
  const names = c.host.toLowerCase() === a.toLowerCase() ? a : `${a} ${c.host}`;
  const lines = [`Host ${names}`, `  HostName ${c.host}`]; // i18n-ignore: ssh_config keywords
  if (c.port) lines.push(`  Port ${c.port}`); // i18n-ignore: ssh_config keyword
  if (c.user) lines.push(`  User ${c.user}`); // i18n-ignore: ssh_config keyword
  // Password login: no IdentityFile, so ssh / VS Code show their own prompt.
  if (text(identityFile) && authMode({ identityFile, auth }) === "key") {
    const key = identityForConfig(identityFile);
    if (!key) return null;
    lines.push(`  IdentityFile ${key}`, "  IdentitiesOnly yes");
  }
  return lines.join("\n");
}

/** The managed block with these entries, ready to paste at the end of ~/.ssh/config. */
export function sshConfigSnippet(entries) {
  const lines = (entries || []).map(sshConfigEntry);
  if (!lines.length || lines.some((l) => !l)) return null;
  return [MANAGED_BEGIN, ...lines, MANAGED_END].join("\n") + "\n";
}

/**
 * Whether an ~/.ssh/config match ({alias, hostname?, user?, port?}) reaches
 * the same account as `conn`: same port (22 when unset) and, when both
 * name one, the same user.
 */
export function configMatchFits(match, conn) {
  const c = cleanConnection(conn);
  if (!match || !validAlias(match.alias) || !c) return false;
  if ((Number(match.port) || 22) !== (c.port || 22)) return false;
  return !(match.user && c.user && match.user !== c.user);
}

/** The key file whose name mentions the server (id_rsa_lab_1 for "lab-1"), else "". */
export function keySuggestion(paths, name) {
  const norm = (s) => text(s).toLowerCase().replace(/[^a-z0-9]+/g, "");
  const want = norm(String(name || "").replace(/^ark-/i, ""));
  if (want.length < 2) return "";
  return (paths || []).find((p) => norm(String(p).split(/[\\/]/).pop()).includes(want)) || "";
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
 *   terminal?: boolean, localTerminal?: boolean, canSave?: boolean}} input
 *   local: the location is on this computer; terminal: the desktop app can
 *   open an SSH terminal (else "terminal" copies the command);
 *   localTerminal: it can open a terminal in a local folder;
 *   conn: effectiveConnection(...)
 * @returns {Array<{id: string, url?: string, path?: string, command?: object, copy?: boolean}>}
 *   ids: vscode, cursor, terminal, copy-ssh, vscode-setup, copy-path, ssh-setup
 */
export function openActions({ device, path = null, local = false, conn = null, terminal = false, localTerminal = false,
  canSave = false } = {}) {
  const out = [];
  const fsPath = validPath(path) && (path.startsWith("/") || path.startsWith("~") || isWindowsPath(path)) ? path : null;
  const sshable = SSH_KINDS.has(device?.kind);
  if (local) {
    for (const editor of ["vscode", "cursor"]) {
      const url = fsPath ? localEditorUrl(fsPath, editor) : null;
      if (url) out.push({ id: editor, url });
    }
    if (fsPath && localTerminal && (fsPath.startsWith("/") || isWindowsPath(fsPath))) out.push({ id: "terminal", local: true, path: fsPath });
  } else if (sshable && conn && cleanConnection(conn)) {
    const remotePath = fsPath && isRemotePosixPath(fsPath) ? fsPath : null;
    // A device (no location) opens its home folder.
    const folder = path === null || path === undefined ? "~" : remotePath;
    for (const editor of ["vscode", "cursor"]) {
      const url = folder ? remoteEditorUrl(conn, folder, editor) : null;
      if (url) out.push({ id: editor, url, folder, conn: cleanConnection(conn) });
    }
    const args = sshArgs(conn, remotePath);
    if (args) {
      // In a browser "Open in terminal" copies the command (no separate copy item).
      out.push({ id: "terminal", copy: !terminal, command: { conn: cleanConnection(conn), path: remotePath } });
      if (terminal) out.push({ id: "copy-ssh", command: { conn: cleanConnection(conn), path: remotePath } });
    }
  }
  const known = Boolean(conn && cleanConnection(conn));
  // No connection yet: "Open in VS Code…" asks for it (prefilled) and then opens.
  if (!local && sshable && !known) {
    const folder = path === null || path === undefined ? "~" : (fsPath && isRemotePosixPath(fsPath) ? fsPath : null);
    if (folder) out.push({ id: "vscode-setup", folder, canSave });
  }
  if (fsPath) out.push({ id: "copy-path", path: fsPath });
  // Change the connection (the viewer's entry; admins also the shared one).
  if (!local && sshable && known && canSave) out.push({ id: "ssh-setup", canSave, known });
  return out;
}

// ------------------------------------------------ one machine, one SSH record
// GET /api/resources/connect also returns the project's shared machine
// records (host, port, default user, name; admins edit them), each device's
// link to a machine and the viewer's own entries (alias, user, auth, and
// host / port overrides).  The device SSH dialog prefills from these; key
// files stay in this computer's storage, passwords never reach ARK.

/** The graph device id of a GPU server name (graph.go slug; "" when the name has no letters or digits). */
export function gpuDeviceId(name) {
  const slug = text(name).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 48);
  return slug ? `gpu:${slug}` : "";
}

const portText = (v) => (v === null || v === undefined || v === "" ? "" : String(v));

/**
 * What the device SSH dialog shows.
 * @param {{device: object, data?: object, link?: string|null, matches?: Map|null,
 *   added?: object, localAuth?: object, localKeys?: object}} input
 *   data: GET /api/resources/connect; link: the machine picked in the
 *   dialog (null: the saved / matched one); matches: the desktop app's
 *   ~/.ssh/config matches (lower-case name -> alias); added: aliases a
 *   browser user pasted (alias -> "user@host:port#mode"); localAuth /
 *   localKeys: this computer's login mode and key file per alias.
 * @returns {{device: string, machine: string, how: string, own: string, shared: object, personal: object,
 *   values: object, aliasPlaceholder: string, options: Array<{id: string, name: string}>}}
 *   machine: the linked machine ("" when none); own: where the shared and
 *   personal values are saved (the machine, else the device itself).
 *   Personal values win over shared ones; the alias is the saved one, else
 *   ark-<name> or another ~/.ssh/config entry for the host when it exists.
 */
export function sshFormPrefill({ device, data = {}, link = null, matches = null, added = {}, localAuth = {}, localKeys = {} } = {}) {
  const id = device?.id || "";
  const machines = data.machines || {};
  const saved = data.links?.[id] || null;
  let machine = saved?.machine || "";
  let how = saved?.how || "";
  if (link !== null && link !== undefined) {
    machine = link && machines[link] ? link : (machines[id] ? id : "");
    how = link ? "picked" : (machine ? "self" : "unlinked");
  }
  const own = machine || id;
  const record = machines[own] || {};
  const shared = { name: text(record.name) || text(device?.label) || id, host: text(record.host), port: portText(record.port), user: text(record.user) };
  const mine = data.mine || {};
  // An older entry for the device itself still wins over the machine's.
  const personal = { ...(mine[own] || {}), ...(own !== id ? mine[id] || {} : {}) };
  // ark-<machine name> only: never a device's hostname or id (machineName).
  const managed = managedAlias(machineName(device, data, machine)) || "";
  const host = text(personal.host) || shared.host || text(data.suggest?.[id]?.host);
  const found = (name) => (name && matches ? matches.get(String(name).toLowerCase()) || "" : "");
  let alias = text(personal.alias);
  if (!alias && managed && (found(managed) === managed || added?.[managed])) alias = managed;
  if (!alias) alias = found(host);
  const localKey = alias || managed || host;
  const options = Object.entries(machines)
    .filter(([mid]) => mid !== id)
    .map(([mid, m]) => ({ id: mid, name: text(m?.name) || mid }));
  return {
    device: id, machine, how, own, shared, personal,
    values: {
      name: shared.name,
      host,
      port: portText(personal.port) || shared.port,
      user: text(personal.user) || shared.user,
      alias,
      auth: personal.auth === "key" || personal.auth === "password" ? personal.auth
        : (localAuth?.[localKey] === "key" || localAuth?.[localKey] === "password" ? localAuth[localKey] : ""),
      keyFile: text(localKeys?.[localKey]),
    },
    aliasPlaceholder: managed || "gpu-a",
    managedAlias: managed,
    options,
  };
}

/**
 * The POST /api/resources/connect/<device> body for the dialog's values.
 * Admins save name, host, port and user as the machine's shared record
 * (and the link when it changed); everyone else saves the host, port and
 * user only where they differ from the shared values.  Alias and login
 * mode are always personal; the key file never leaves this computer.
 * @param {object} prefill sshFormPrefill(...) of the machine shown
 * @param {object} form {name, host, port, user, alias, auth}
 * @param {{admin?: boolean, personal?: boolean, linkChanged?: boolean}} opts
 */
export function sshFormBody(prefill, form, { admin = false, personal = true, linkChanged = false } = {}) {
  const f = Object.fromEntries(["name", "host", "port", "user", "alias", "auth"].map((k) => [k, text(form?.[k])]));
  const body = {};
  if (personal) {
    if (f.alias) body.alias = f.alias;
    if (f.auth) body.auth = f.auth;
  }
  if (admin) {
    const shared = {};
    for (const k of ["name", "host", "port", "user"]) if (f[k]) shared[k] = f[k];
    body.shared = Object.keys(shared).length ? shared : null;
    if (linkChanged) body.link = prefill.machine && prefill.machine !== prefill.device ? prefill.machine : "";
  } else if (personal) {
    for (const k of ["host", "port", "user"]) if (f[k] && f[k] !== text(prefill.shared?.[k])) body[k] = f[k];
  }
  return body;
}

/**
 * The display name of the machine a device is linked to, for its managed
 * ~/.ssh/config alias (ark-<name>): the linked machine's name, the device's
 * own shared record's name, a GPU server's name; "" otherwise.  A setup
 * host's label (a hostname such as deskhost7k2pq) is never used.
 * @param {object} device graph device
 * @param {object} data GET /api/resources/connect ({machines, links, devices})
 * @param {string|null} machine the machine picked in a dialog (null: the saved / matched one)
 */
export function machineName(device, data = {}, machine = null) {
  const machines = data?.machines || {};
  const id = device?.id || "";
  const mid = machine !== null && machine !== undefined ? machine
    : (data?.links?.[id]?.machine || data?.devices?.[id]?.machine || "");
  const record = machines[mid] || machines[id];
  if (record && text(record.name)) return text(record.name);
  return device?.kind === "gpu-server" ? text(device.label) : "";
}

/**
 * Which ~/.ssh/config alias to open a connection with, and whether it is
 * known to exist.  Known: an alias the desktop app found in ~/.ssh/config
 * (`found`: [{name, alias, hostname?, user?, port?}], looked up for the
 * connection's own alias, ark-<name> and the host), or, in a browser, an
 * entry the person pasted or confirmed for the same address (`added`:
 * alias -> "user@host:port#mode").  A saved alias is checked the same way
 * (a stale ark-<hostname> alias once sent VS Code to a Host nobody had);
 * only an alias-only connection (no host to fall back to) is trusted as is.
 * Otherwise the managed alias to write (or null when the machine has no
 * name: open with user@host:port instead).
 * @returns {{alias: string|null, known: boolean}}
 */
export function resolveAlias({ name = "", conn = null, found = null, added = null } = {}) {
  const c = cleanConnection(conn);
  if (!c) return { alias: null, known: false };
  if (c.alias && !c.host) return { alias: c.alias, known: true };
  const managed = text(name) ? managedAlias(name) : null;
  const host = (c.host || "").toLowerCase();
  const key = `${c.user || ""}@${c.host}:${c.port || 22}`;
  const pasted = (a) => Boolean(a && added && typeof added === "object" && validAlias(a)
    && String(added[a] || "").split("#")[0] === key);
  if (Array.isArray(found)) {
    const inConfig = (a) => a && found.find((m) => m.alias === a && configMatchFits(m, c)
      && (!m.hostname || String(m.hostname).toLowerCase() === host));
    if (inConfig(c.alias)) return { alias: c.alias, known: true };
    if (inConfig(managed)) return { alias: managed, known: true };
    const other = found.find((m) => String(m.name || "").toLowerCase() === host && configMatchFits(m, c));
    if (other) return { alias: other.alias, known: true };
  } else {
    if (pasted(c.alias)) return { alias: c.alias, known: true };
    if (pasted(managed)) return { alias: managed, known: true };
  }
  return { alias: managed, known: false };
}

/**
 * Aliases made from a device's hostname or id (ark-<label>, ark-<host id>)
 * that older versions suggested and remembered in this browser: alias ->
 * the machine's managed alias to move the key file and login mode to ("" when
 * the device has no machine).  The machine's own alias is never stale.
 * @param {Array<object>} devices graph devices
 * @param {object} data GET /api/resources/connect
 */
export function staleAliases(devices, data = {}) {
  const out = new Map();
  for (const device of devices || []) {
    if (device?.kind !== "ark" && device?.kind !== "host") continue;
    const own = managedAlias(machineName(device, data)) || "";
    const id = String(device.id || "");
    const ids = [device.label, id.startsWith("host:") ? id.slice(5) : ""];
    for (const n of ids) {
      const a = text(n) ? managedAlias(n) : null;
      if (a && a !== own && !out.has(a)) out.set(a, own);
    }
  }
  return out;
}

/** ssh's default key file names: used without any ~/.ssh/config entry. */
const DEFAULT_KEYS = new Set(["id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk", "id_dsa"]);

/** Whether ssh tries this key file on its own (a default name in ~/.ssh). */
export function isDefaultKey(path) {
  const p = text(path).replace(/\\/g, "/");
  const parts = p.split("/");
  return parts.length >= 2 && parts[parts.length - 2] === ".ssh" && DEFAULT_KEYS.has(parts[parts.length - 1]);
}

/**
 * How to open a connection (VS Code, Cursor, terminal):
 *   "alias"   the alias is known to exist (resolveAlias known)
 *   "write"   desktop app: write Host ark-<machine> first (it asks), then the alias
 *   "confirm" browser: show the entry to add to ~/.ssh/config, open with the
 *             alias once the person says it is added
 *   "direct"  user@host:port: no machine alias, password login, or a key
 *             ssh uses by itself (a default key name)
 * A browser never opens the direct form for key login with another key:
 * ssh would not use that key for the bare address ("Permission denied
 * (publickey)", 2026-11-26).  An unknown login mode counts as key login.
 * @param {{resolved: {alias: string|null, known: boolean}, desktop?: boolean,
 *   auth?: string, keyFile?: string}} input
 */
export function openPlan({ resolved, desktop = false, auth = "", keyFile = "" } = {}) {
  if (resolved?.known && resolved.alias) return "alias";
  if (!resolved?.alias) return "direct";
  if (desktop) return "write";
  if (text(auth) === "password") return "direct";
  if (text(keyFile) && isDefaultKey(keyFile) && text(auth) !== "") return "direct";
  return "confirm";
}
