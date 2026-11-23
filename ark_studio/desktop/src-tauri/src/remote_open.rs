//! Open a resource location over SSH (2026-11-22): the Resources graph's
//! "Open SSH terminal" and "Open in VS Code / Cursor" (web/js/resource-open-model.js).
//!
//! * `open_ssh_terminal`: a new terminal window running
//!   `ssh -t [-p port] -- <dest> "cd -- '<path>' && exec $SHELL -l"`.
//!   The page sends the parts (alias or host, user, port, path), never a
//!   command: every part is checked here (`check_*`: no option-like values,
//!   no control characters, no shell syntax in names), the remote path is
//!   single-quoted for the remote shell, and only fixed programs are started
//!   with argument arrays (no local shell): Windows Terminal (`wt.exe`) when
//!   installed, else `ssh.exe` in a new console window; macOS Terminal via a
//!   one-shot `.command` file; on Linux the first terminal emulator found.
//! * `ssh_config_match`: which of the given names are Host aliases (or
//!   HostNames) of ~/.ssh/config. Only matches are returned, never the file.
//! * `open_editor_url`: hands a `vscode://` or `cursor://` URL (file or
//!   vscode-remote only) to the system.

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

pub const MAX_PATH_LEN: usize = 1024;
pub const MAX_URL_LEN: usize = 4096;
pub const MAX_NAMES: usize = 64;

#[derive(Clone, Debug, Default, Deserialize)]
pub struct SshTarget {
    #[serde(default)]
    pub alias: Option<String>,
    #[serde(default)]
    pub host: Option<String>,
    #[serde(default)]
    pub user: Option<String>,
    #[serde(default)]
    pub port: Option<u32>,
    /// The private key file on this computer (a path, never the key); used
    /// as `-i` when there is no alias (an alias brings its own IdentityFile).
    #[serde(default)]
    pub identity_file: Option<String>,
    /// "key" or "password" (ssh asks for it in the terminal; ARK never sees it).
    #[serde(default)]
    pub auth: Option<String>,
}

/// The auth mode: "password" turns the key off, anything else keeps it.
pub fn password_auth(auth: Option<&str>) -> bool {
    auth.map(str::trim) == Some("password")
}

/// A key file path for `ssh -i`: absolute (C:\..., /..., ~/...), no control
/// characters, never option-like.
pub fn check_identity(raw: &str) -> Result<String, String> {
    let p = raw.trim();
    if p.is_empty() || p.len() > MAX_PATH_LEN || p.chars().any(char::is_control) || p.starts_with('-') {
        return Err("Invalid key file path.".into());
    }
    let b = p.as_bytes();
    let drive = b.len() > 2 && b[0].is_ascii_alphabetic() && b[1] == b':' && (b[2] == b'\\' || b[2] == b'/');
    if !(p.starts_with('/') || p.starts_with("~/") || drive) || p.starts_with("//") || p.starts_with("\\\\") {
        return Err("The key file path must be absolute.".into());
    }
    Ok(p.to_string())
}

fn name_ok(s: &str, max: usize, extra: &[char]) -> bool {
    let mut chars = s.chars();
    match chars.next() {
        Some(c) if c.is_ascii_alphanumeric() || c == '_' => {}
        _ => return false,
    }
    s.len() <= max && chars.all(|c| c.is_ascii_alphanumeric() || "._-".contains(c) || extra.contains(&c))
}

/// A host name or an IPv4 / IPv6 address (never an ssh option).
pub fn check_host(s: &str) -> Result<(), String> {
    if name_ok(s, 253, &[':', '%']) { Ok(()) } else { Err("Invalid host name.".into()) }
}

pub fn check_user(s: &str) -> Result<(), String> {
    if name_ok(s, 64, &[]) { Ok(()) } else { Err("Invalid user name.".into()) }
}

/// An ~/.ssh/config Host alias (no patterns).
pub fn check_alias(s: &str) -> Result<(), String> {
    if name_ok(s, 100, &[]) { Ok(()) } else { Err("Invalid SSH alias.".into()) }
}

/// A POSIX path on the remote host: absolute or under ~, no control characters.
pub fn check_remote_path(s: &str) -> Result<(), String> {
    if s.is_empty() || s.len() > MAX_PATH_LEN || s.chars().any(char::is_control) {
        return Err("Invalid path.".into());
    }
    if !(s.starts_with('/') || s == "~" || s.starts_with("~/")) {
        return Err("The path must be absolute (/...) or start with ~/.".into());
    }
    Ok(())
}

/// `s` as one word for a POSIX shell.
pub fn sh_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// The command the remote shell runs: change to `path`, then a login shell.
/// None for the home folder (ssh starts a login shell there anyway).
pub fn remote_command(path: &str) -> Result<Option<String>, String> {
    check_remote_path(path)?;
    let trimmed = path.trim_end_matches('/');
    let target = if trimmed == "~" || path == "~/" {
        return Ok(None);
    } else if let Some(rest) = path.strip_prefix("~/") {
        format!("~/{}", sh_quote(rest))
    } else {
        sh_quote(path)
    };
    Ok(Some(format!("cd -- {target} && exec ${{SHELL:-/bin/sh}} -l")))
}

/// The ssh destination: the alias, else [user@]host.
pub fn destination(t: &SshTarget) -> Result<String, String> {
    let alias = t.alias.as_deref().map(str::trim).filter(|s| !s.is_empty());
    let host = t.host.as_deref().map(str::trim).filter(|s| !s.is_empty());
    let user = t.user.as_deref().map(str::trim).filter(|s| !s.is_empty());
    if let Some(a) = alias {
        check_alias(a)?;
        return Ok(a.to_string());
    }
    let h = host.ok_or("No SSH host for this device.")?;
    check_host(h)?;
    match user {
        Some(u) => {
            check_user(u)?;
            Ok(format!("{u}@{h}"))
        }
        None => Ok(h.to_string()),
    }
}

/// ssh's arguments (after the program name).
pub fn ssh_args(t: &SshTarget, path: Option<&str>) -> Result<Vec<String>, String> {
    let dest = destination(t)?;
    let mut args = vec!["-t".to_string()];
    let has_alias = t.alias.as_deref().is_some_and(|a| !a.trim().is_empty());
    if let Some(p) = t.port {
        if !(1..=65535).contains(&p) {
            return Err("Invalid port.".into());
        }
        // An alias brings its own Port from ~/.ssh/config.
        if !has_alias && p != 22 {
            args.extend(["-p".to_string(), p.to_string()]);
        }
    }
    // Key login without an alias: this key only. Password login: nothing
    // (ssh prompts in the terminal).
    if let Some(k) = t.identity_file.as_deref().map(str::trim).filter(|k| !k.is_empty()) {
        if !has_alias && !password_auth(t.auth.as_deref()) {
            args.extend(["-i".to_string(), check_identity(k)?, "-o".to_string(), "IdentitiesOnly=yes".to_string()]);
        }
    }
    args.push("--".into());
    args.push(dest);
    if let Some(cmd) = path.map(str::trim).filter(|p| !p.is_empty()).map(remote_command).transpose()?.flatten() {
        args.push(cmd);
    }
    Ok(args)
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Os {
    Windows,
    Macos,
    Linux,
}

impl Os {
    pub fn current() -> Os {
        if cfg!(windows) {
            Os::Windows
        } else if cfg!(target_os = "macos") {
            Os::Macos
        } else {
            Os::Linux
        }
    }
}

/// What to start: a program with arguments, and for macOS the text of the
/// `.command` file (its path is the last argument).
#[derive(Clone, Debug, PartialEq)]
pub struct Launch {
    pub program: PathBuf,
    pub args: Vec<String>,
    /// Windows: start in a new console window.
    pub new_console: bool,
    pub script: Option<String>,
}

/// Linux terminal emulators, in order, with the flag before the command.
const LINUX_TERMINALS: &[(&str, &[&str])] = &[
    ("x-terminal-emulator", &["-e"]),
    ("gnome-terminal", &["--"]),
    ("konsole", &["-e"]),
    ("xfce4-terminal", &["-x"]),
    ("kitty", &[]),
    ("alacritty", &["-e"]),
    ("xterm", &["-e"]),
];

/// How to run `ssh <args>` in a new terminal window. `find` looks a program
/// up (PATH and the usual install folders); `script_path` is where macOS's
/// `.command` file goes.
pub fn terminal_launch(
    os: Os,
    args: &[String],
    find: &dyn Fn(&str) -> Option<PathBuf>,
    script_path: &Path,
) -> Result<Launch, String> {
    match os {
        Os::Windows => {
            let ssh = find("ssh.exe").ok_or("OpenSSH (ssh.exe) is not installed. Add it in Settings › Apps › Optional features.")?;
            let ssh_s = ssh.to_string_lossy().to_string();
            // Windows Terminal reads ";" as "next command" anywhere in its
            // command line: such arguments go to a plain console instead.
            if let Some(wt) = find("wt.exe").filter(|_| !args.iter().any(|a| a.contains(';')) && !ssh_s.contains(';')) {
                let mut wt_args = vec!["new-tab".to_string(), "--title".to_string(), "ARK SSH".to_string(), "--".to_string(), ssh_s];
                wt_args.extend(args.iter().cloned());
                return Ok(Launch { program: wt, args: wt_args, new_console: false, script: None });
            }
            Ok(Launch { program: ssh, args: args.to_vec(), new_console: true, script: None })
        }
        Os::Macos => {
            let mut words = vec![sh_quote("ssh")];
            words.extend(args.iter().map(|a| sh_quote(a)));
            let script = format!("#!/bin/sh\nrm -f -- \"$0\"\nexec {}\n", words.join(" "));
            Ok(Launch {
                program: PathBuf::from("/usr/bin/open"),
                args: vec!["-a".into(), "Terminal".into(), script_path.to_string_lossy().to_string()],
                new_console: false,
                script: Some(script),
            })
        }
        Os::Linux => {
            for (name, flag) in LINUX_TERMINALS {
                if let Some(program) = find(name) {
                    let mut a: Vec<String> = flag.iter().map(|s| s.to_string()).collect();
                    a.push("ssh".into());
                    a.extend(args.iter().cloned());
                    return Ok(Launch { program, args: a, new_console: false, script: None });
                }
            }
            Err("No terminal emulator found (x-terminal-emulator, gnome-terminal, konsole, xterm, ...).".into())
        }
    }
}

/// A program on PATH, or in the usual Windows folders for ssh.exe / wt.exe.
pub fn find_program(name: &str) -> Option<PathBuf> {
    let mut dirs: Vec<PathBuf> = std::env::var_os("PATH").map(|p| std::env::split_paths(&p).collect()).unwrap_or_default();
    if cfg!(windows) {
        if let Some(root) = std::env::var_os("SystemRoot") {
            dirs.push(Path::new(&root).join("System32").join("OpenSSH"));
        }
        if let Some(local) = std::env::var_os("LOCALAPPDATA") {
            dirs.push(Path::new(&local).join("Microsoft").join("WindowsApps"));
        }
    }
    dirs.into_iter().map(|d| d.join(name)).find(|p| p.is_file())
}

/// Starts `launch` (no shell; arguments as an array).
pub fn spawn(launch: &Launch, script_path: &Path) -> Result<(), String> {
    if let Some(text) = &launch.script {
        write_script(script_path, text).map_err(|e| format!("Could not prepare the terminal: {e}"))?;
    }
    let mut cmd = std::process::Command::new(&launch.program);
    cmd.args(&launch.args).stdin(std::process::Stdio::null());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NEW_CONSOLE: u32 = 0x0000_0010;
        if launch.new_console {
            cmd.creation_flags(CREATE_NEW_CONSOLE);
        }
    }
    cmd.spawn().map(|_| ()).map_err(|e| format!("Could not open a terminal: {e}"))
}

fn write_script(path: &Path, text: &str) -> std::io::Result<()> {
    use std::io::Write;
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o700);
    }
    options.open(path)?.write_all(text.as_bytes())
}

/// A fresh path for macOS's `.command` file.
pub fn script_path() -> PathBuf {
    let mut bytes = [0u8; 8];
    let _ = getrandom::getrandom(&mut bytes);
    std::env::temp_dir().join(format!("ark-ssh-{}.command", hex::encode(bytes)))
}

/// A `vscode://` / `cursor://` URL that opens a folder (file or Remote-SSH).
pub fn check_editor_url(raw: &str) -> Result<String, String> {
    if raw.is_empty() || raw.len() > MAX_URL_LEN || raw.chars().any(char::is_control) {
        return Err("Invalid editor link.".into());
    }
    let url = url::Url::parse(raw).map_err(|_| "Invalid editor link.".to_string())?;
    if !matches!(url.scheme(), "vscode" | "cursor") {
        return Err("Only VS Code and Cursor links are opened.".into());
    }
    match url.host_str() {
        Some("file") if url.path().len() > 1 => {}
        Some("vscode-remote") if url.path().starts_with("/ssh-remote+") => {}
        _ => return Err("Only folder links are opened.".into()),
    }
    if url.query().is_some() || url.fragment().is_some() {
        return Err("Only folder links are opened.".into());
    }
    Ok(url.to_string())
}

#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct ConfigHost {
    pub alias: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub hostname: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub port: Option<u16>,
}

/// Host blocks of an ssh config (aliases without patterns; Include and
/// Match are not followed). Only Host, HostName, User and Port are read.
pub fn parse_ssh_config(text: &str) -> Vec<ConfigHost> {
    let mut out: Vec<ConfigHost> = Vec::new();
    let mut current: Vec<usize> = Vec::new();
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let (key, value) = match line.find(|c: char| c.is_whitespace() || c == '=') {
            Some(i) => (&line[..i], line[i..].trim_start_matches(|c: char| c.is_whitespace() || c == '=').trim()),
            None => (line, ""),
        };
        let value = value.trim_matches('"');
        match key.to_ascii_lowercase().as_str() {
            "host" => {
                current.clear();
                for alias in value.split_whitespace() {
                    if check_alias(alias).is_ok() && !out.iter().any(|h| h.alias == alias) {
                        current.push(out.len());
                        out.push(ConfigHost { alias: alias.to_string(), ..Default::default() });
                    }
                }
            }
            "match" => current.clear(),
            "hostname" if check_host(value).is_ok() => {
                for &i in &current {
                    out[i].hostname.get_or_insert_with(|| value.to_string());
                }
            }
            "user" if check_user(value).is_ok() => {
                for &i in &current {
                    out[i].user.get_or_insert_with(|| value.to_string());
                }
            }
            "port" => {
                if let Ok(p) = value.parse::<u16>() {
                    for &i in &current {
                        out[i].port.get_or_insert(p);
                    }
                }
            }
            _ => {}
        }
    }
    out
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct ConfigMatch {
    pub name: String,
    #[serde(flatten)]
    pub host: ConfigHost,
}

/// For each name: the Host whose alias is that name, else whose HostName
/// is (case-insensitive). Names without a match are left out.
pub fn match_config(hosts: &[ConfigHost], names: &[String]) -> Vec<ConfigMatch> {
    names
        .iter()
        .take(MAX_NAMES)
        .filter(|n| !n.is_empty() && n.len() <= 253)
        .filter_map(|name| {
            let by_alias = hosts.iter().find(|h| h.alias.eq_ignore_ascii_case(name));
            let found = by_alias.or_else(|| hosts.iter().find(|h| h.hostname.as_deref().is_some_and(|x| x.eq_ignore_ascii_case(name))));
            found.map(|h| ConfigMatch { name: name.clone(), host: h.clone() })
        })
        .collect()
}

/// ~/.ssh/config of this user (empty when absent).
pub fn read_user_config() -> String {
    let home = std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" });
    home.map(|h| Path::new(&h).join(".ssh").join("config"))
        .and_then(|p| std::fs::read(p).ok())
        .filter(|b| b.len() <= 1 << 20)
        .map(|b| String::from_utf8_lossy(&b).to_string())
        .unwrap_or_default()
}

// ------------------------------------------------- managed ~/.ssh/config hosts

/// VS Code Remote-SSH only uses a key, a port and a user that ~/.ssh/config
/// gives a Host alias. `ssh_config_ensure` keeps one entry per server
/// (`Host ark-<name>`) inside a marked block at the end of the file; text
/// outside the block is never changed. web/js/resource-open-model.js writes
/// the same block for the browser's copy-and-paste snippet.
pub const MANAGED_BEGIN: &str = "# >>> ARK Studio managed hosts (edits inside are replaced) >>>";
pub const MANAGED_END: &str = "# <<< ARK Studio managed hosts <<<";
pub const MANAGED_PREFIX: &str = "ark-";

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq)]
pub struct ManagedHost {
    pub alias: String,
    pub host: String,
    #[serde(default)]
    pub user: Option<String>,
    #[serde(default)]
    pub port: Option<u32>,
    /// The private key file on this computer (a path, never the key).
    #[serde(default)]
    pub identity_file: Option<String>,
    /// "key" (IdentityFile + IdentitiesOnly) or "password" (neither: ssh and
    /// VS Code show their own password prompt; ARK never handles it).
    #[serde(default)]
    pub auth: Option<String>,
}

/// A key file path for IdentityFile: absolute (or ~/...), no control
/// characters or double quotes. Backslashes become "/" (OpenSSH reads "\"
/// as an escape; Windows' ssh.exe takes "/" fine).
pub fn identity_for_config(raw: &str) -> Result<String, String> {
    let p = raw.trim();
    if p.is_empty() || p.len() > MAX_PATH_LEN || p.chars().any(|c| c.is_control() || c == '"') {
        return Err("Invalid key file path.".into());
    }
    let p = p.replace('\\', "/");
    let drive = p.len() > 2 && p.as_bytes()[0].is_ascii_alphabetic() && &p[1..3] == ":/";
    if !(p.starts_with('/') || p.starts_with("~/") || drive) || p.starts_with("//") {
        return Err("The key file path must be absolute.".into());
    }
    Ok(if p.chars().any(char::is_whitespace) { format!("\"{p}\"") } else { p })
}

/// The checked entry's lines (no trailing newline).
pub fn managed_entry(h: &ManagedHost) -> Result<String, String> {
    check_alias(&h.alias)?;
    if !h.alias.starts_with(MANAGED_PREFIX) {
        return Err("ARK only manages ark-... aliases.".into());
    }
    check_host(h.host.trim())?;
    let mut lines = vec![format!("Host {}", h.alias), format!("  HostName {}", h.host.trim())];
    if let Some(p) = h.port {
        if !(1..=65535).contains(&p) {
            return Err("Invalid port.".into());
        }
        lines.push(format!("  Port {p}"));
    }
    if let Some(u) = h.user.as_deref().map(str::trim).filter(|u| !u.is_empty()) {
        check_user(u)?;
        lines.push(format!("  User {u}"));
    }
    let key = h.identity_file.as_deref().map(str::trim).filter(|k| !k.is_empty());
    if let Some(k) = key.filter(|_| !password_auth(h.auth.as_deref())) {
        lines.push(format!("  IdentityFile {}", identity_for_config(k)?));
        lines.push("  IdentitiesOnly yes".into());
    }
    Ok(lines.join("\n"))
}

/// The managed block's entries: (alias, entry text).
fn managed_entries(block: &str) -> Vec<(String, String)> {
    let mut out: Vec<(String, String)> = Vec::new();
    for line in block.lines() {
        let t = line.trim_end();
        if let Some(alias) = t.strip_prefix("Host ") {
            out.push((alias.trim().to_string(), t.to_string()));
        } else if let Some(last) = out.last_mut() {
            if !t.trim().is_empty() {
                last.1.push('\n');
                last.1.push_str(t);
            }
        }
    }
    out
}

/// `text` with `h` added to (or replaced in) the managed block. None when
/// the file already has exactly that entry.
pub fn upsert_managed(text: &str, h: &ManagedHost) -> Result<Option<String>, String> {
    let entry = managed_entry(h)?;
    let crlf = text.contains("\r\n");
    let norm = text.replace("\r\n", "\n");
    let (before, block, after) = match (norm.find(MANAGED_BEGIN), norm.find(MANAGED_END)) {
        (Some(b), Some(e)) if b < e => {
            let rest = &norm[e + MANAGED_END.len()..];
            (norm[..b].to_string(), norm[b + MANAGED_BEGIN.len()..e].to_string(), rest.strip_prefix('\n').unwrap_or(rest).to_string())
        }
        (None, None) => (norm.clone(), String::new(), String::new()),
        _ => return Err("The ARK Studio block in ~/.ssh/config is damaged; fix or remove its marker lines.".into()),
    };
    let mut entries = managed_entries(&block);
    match entries.iter_mut().find(|(a, _)| a.eq_ignore_ascii_case(&h.alias)) {
        Some(existing) if existing.1 == entry => return Ok(None),
        Some(existing) => existing.1 = entry,
        None => entries.push((h.alias.clone(), entry)),
    }
    let mut out = before;
    if !out.is_empty() && !out.ends_with('\n') {
        out.push('\n');
    }
    if !out.is_empty() && !out.ends_with("\n\n") {
        out.push('\n');
    }
    out.push_str(MANAGED_BEGIN);
    out.push('\n');
    for (_, e) in &entries {
        out.push_str(e);
        out.push('\n');
    }
    out.push_str(MANAGED_END);
    out.push('\n');
    out.push_str(&after);
    Ok(Some(if crlf { out.replace('\n', "\r\n") } else { out }))
}

/// ~/.ssh of this user.
pub fn user_ssh_dir() -> Option<PathBuf> {
    std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" }).map(|h| Path::new(&h).join(".ssh"))
}

/// Writes `text` to `config`: a one-time copy of the old file goes to
/// config.ark-backup, the new text is written next to it and renamed.
pub fn write_config(config: &Path, text: &str) -> Result<(), String> {
    let dir = config.parent().ok_or("No ~/.ssh folder.")?;
    std::fs::create_dir_all(dir).map_err(|e| format!("Could not create {}: {e}", dir.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700));
    }
    let backup = dir.join("config.ark-backup");
    if config.is_file() && !backup.exists() {
        std::fs::copy(config, &backup).map_err(|e| format!("Could not back up ~/.ssh/config: {e}"))?;
    }
    let tmp = dir.join("config.ark-tmp");
    std::fs::write(&tmp, text.as_bytes()).map_err(|e| format!("Could not write ~/.ssh/config: {e}"))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600));
    }
    std::fs::rename(&tmp, config).map_err(|e| {
        let _ = std::fs::remove_file(&tmp);
        format!("Could not write ~/.ssh/config: {e}")
    })
}

/// Private key files in `dir` (paths only; the files are not returned):
/// not *.pub / config / known_hosts, starting with a PEM / OpenSSH header.
pub fn key_candidates(dir: &Path) -> Vec<String> {
    let Ok(read) = std::fs::read_dir(dir) else { return Vec::new() };
    let mut out: Vec<String> = read
        .filter_map(Result::ok)
        .map(|e| e.path())
        .filter(|p| p.is_file())
        .filter(|p| {
            let name = p.file_name().map(|n| n.to_string_lossy().to_ascii_lowercase()).unwrap_or_default();
            !(name.ends_with(".pub") || name.starts_with("config") || name.starts_with("known_hosts") || name.starts_with("authorized_keys"))
        })
        .filter(|p| {
            use std::io::Read;
            let mut head = [0u8; 64];
            let n = std::fs::File::open(p).and_then(|mut f| f.read(&mut head)).unwrap_or(0);
            let head = String::from_utf8_lossy(&head[..n]);
            head.starts_with("-----BEGIN") && head.contains("PRIVATE KEY")
        })
        .map(|p| p.to_string_lossy().to_string())
        .take(32)
        .collect();
    out.sort();
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn target(alias: &str, host: &str, user: &str, port: Option<u32>) -> SshTarget {
        let s = |v: &str| if v.is_empty() { None } else { Some(v.to_string()) };
        SshTarget { alias: s(alias), host: s(host), user: s(user), port, ..Default::default() }
    }

    fn v(items: &[&str]) -> Vec<String> {
        items.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn ssh_arguments() {
        let t = target("", "203.0.113.7", "user", Some(2222));
        assert_eq!(
            ssh_args(&t, Some("/data/repo")).unwrap(),
            v(&["-t", "-p", "2222", "--", "user@203.0.113.7", "cd -- '/data/repo' && exec ${SHELL:-/bin/sh} -l"])
        );
        // Port 22 and an alias's port are left to ssh / ~/.ssh/config.
        assert_eq!(ssh_args(&target("", "box", "", Some(22)), None).unwrap(), v(&["-t", "--", "box"]));
        assert_eq!(ssh_args(&target("gpu-a", "10.0.0.5", "ana", Some(2222)), Some("~")).unwrap(), v(&["-t", "--", "gpu-a"]));
        assert_eq!(
            ssh_args(&target("gpu-a", "", "", None), Some("~/work/my repo")).unwrap(),
            v(&["-t", "--", "gpu-a", "cd -- ~/'work/my repo' && exec ${SHELL:-/bin/sh} -l"])
        );
    }

    #[test]
    fn paths_are_quoted_for_the_remote_shell() {
        let cmd = |p: &str| remote_command(p).unwrap().unwrap();
        assert_eq!(cmd("/it's/$(rm -rf ~)/`id`"), r"cd -- '/it'\''s/$(rm -rf ~)/`id`' && exec ${SHELL:-/bin/sh} -l");
        assert_eq!(cmd("/a;b && c|d"), "cd -- '/a;b && c|d' && exec ${SHELL:-/bin/sh} -l");
        assert_eq!(remote_command("~/").unwrap(), None);
        for bad in ["relative/path", "C:\\work", "-rf", "/a\nrm -rf ~", "/a\rb", "/a\u{0}b", "/a\u{7f}", "~user/x", ""] {
            assert!(remote_command(bad).is_err(), "{bad:?}");
        }
        assert!(remote_command(&format!("/{}", "a".repeat(MAX_PATH_LEN))).is_err());
    }

    #[test]
    fn options_and_shell_syntax_are_refused() {
        for t in [
            target("", "-oProxyCommand=calc", "", None),
            target("", "host", "-oProxyCommand=calc", None),
            target("-F/tmp/x", "", "", None),
            target("", "a b", "", None),
            target("", "h;calc", "", None),
            target("", "h&calc", "", None),
            target("", "h|x", "", None),
            target("", "h\nx", "", None),
            target("", "h", "u@v", None),
            target("", "h", "$(id)", None),
            target("a*", "", "", None),
            target("", "", "user", None),
            target("", "h", "", Some(0)),
            target("", "h", "", Some(70000)),
        ] {
            assert!(ssh_args(&t, None).is_err(), "{t:?}");
        }
        assert!(check_host("fe80::1%eth0").is_ok());
        assert!(check_host("gpu-01.lab.example.com").is_ok());
    }

    fn finder(present: &'static [&'static str]) -> impl Fn(&str) -> Option<PathBuf> {
        move |name: &str| present.iter().find(|p| p.ends_with(name)).map(PathBuf::from)
    }

    #[test]
    fn terminal_per_os() {
        let args = ssh_args(&target("", "box", "me", Some(2222)), Some("/srv/x y")).unwrap();
        let script = Path::new("/tmp/ark-ssh-1.command");
        // Windows Terminal when installed.
        let wt = terminal_launch(Os::Windows, &args, &finder(&["C:/OpenSSH/ssh.exe", "C:/Apps/wt.exe"]), script).unwrap();
        assert_eq!(wt.program, PathBuf::from("C:/Apps/wt.exe"));
        assert_eq!(&wt.args[..5], &v(&["new-tab", "--title", "ARK SSH", "--", "C:/OpenSSH/ssh.exe"])[..]);
        assert_eq!(&wt.args[5..], &args[..]);
        assert!(!wt.new_console);
        // Else ssh.exe in its own console; also when an argument has ";".
        let plain = terminal_launch(Os::Windows, &args, &finder(&["C:/OpenSSH/ssh.exe"]), script).unwrap();
        assert_eq!((plain.program.clone(), plain.args.clone(), plain.new_console), (PathBuf::from("C:/OpenSSH/ssh.exe"), args.clone(), true));
        let semi = ssh_args(&target("", "box", "", None), Some("/a;b")).unwrap();
        let l = terminal_launch(Os::Windows, &semi, &finder(&["C:/OpenSSH/ssh.exe", "C:/Apps/wt.exe"]), script).unwrap();
        assert!(l.new_console && l.program.ends_with("ssh.exe"));
        assert!(terminal_launch(Os::Windows, &args, &finder(&[]), script).is_err());
        // macOS: a .command file that removes itself, every word quoted.
        let mac = terminal_launch(Os::Macos, &args, &finder(&[]), script).unwrap();
        assert_eq!(mac.program, PathBuf::from("/usr/bin/open"));
        assert_eq!(mac.args, v(&["-a", "Terminal", "/tmp/ark-ssh-1.command"]));
        assert_eq!(
            mac.script.unwrap(),
            "#!/bin/sh\nrm -f -- \"$0\"\nexec 'ssh' '-t' '-p' '2222' '--' 'me@box' 'cd -- '\\''/srv/x y'\\'' && exec ${SHELL:-/bin/sh} -l'\n"
        );
        // Linux: the first terminal found, with its own "run this" flag.
        let gnome = terminal_launch(Os::Linux, &args, &finder(&["/usr/bin/gnome-terminal", "/usr/bin/xterm"]), script).unwrap();
        assert_eq!(gnome.program, PathBuf::from("/usr/bin/gnome-terminal"));
        assert_eq!(&gnome.args[..2], &v(&["--", "ssh"])[..]);
        assert_eq!(&gnome.args[2..], &args[..]);
        let xterm = terminal_launch(Os::Linux, &args, &finder(&["/usr/bin/xterm"]), script).unwrap();
        assert_eq!(&xterm.args[..2], &v(&["-e", "ssh"])[..]);
        assert!(terminal_launch(Os::Linux, &args, &finder(&[]), script).is_err());
    }

    #[test]
    fn editor_links() {
        for ok in [
            "vscode://file/E:/repo",
            "vscode://file/home/me/x%20y",
            "cursor://file/Users/me/x",
            "vscode://vscode-remote/ssh-remote+gpu-a/data/repo",
            "vscode://vscode-remote/ssh-remote+7b22686f73744e616d65223a2268227d/srv",
        ] {
            assert!(check_editor_url(ok).is_ok(), "{ok}");
        }
        for bad in [
            "https://example.com",
            "file:///C:/Windows/System32/calc.exe",
            "vscode://example.com/run?x=1",
            "vscode://file/",
            "vscode://vscode-remote/wsl+Ubuntu/x",
            "vscode://file/x?windowId=1",
            "vscode://file/x#y",
            "vscode://file/a\nb",
            "javascript:alert(1)",
            "",
        ] {
            assert!(check_editor_url(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn ssh_config() {
        let text = "# mine\nHost gpu-a gpu-a-alias\n  HostName 10.0.0.5\n  User ana\n  Port 2222\n  IdentityFile ~/.ssh/id_a\n\
                    Host *.lab !x\n  User nobody\nHost=box\n  hostname=\"box.example.com\"\nMatch host foo\n  User skip\nHost bad;name ok\n  HostName -oX\n";
        let hosts = parse_ssh_config(text);
        assert_eq!(
            hosts,
            vec![
                ConfigHost { alias: "gpu-a".into(), hostname: Some("10.0.0.5".into()), user: Some("ana".into()), port: Some(2222) },
                ConfigHost { alias: "gpu-a-alias".into(), hostname: Some("10.0.0.5".into()), user: Some("ana".into()), port: Some(2222) },
                ConfigHost { alias: "box".into(), hostname: Some("box.example.com".into()), ..Default::default() },
                ConfigHost { alias: "ok".into(), ..Default::default() },
            ]
        );
        let m = match_config(&hosts, &v(&["GPU-A", "box.example.com", "unknown", ""]));
        assert_eq!(m.iter().map(|x| (x.name.as_str(), x.host.alias.as_str())).collect::<Vec<_>>(), vec![("GPU-A", "gpu-a"), ("box.example.com", "box")]);
        let json = serde_json::to_value(&m[0]).unwrap();
        assert_eq!(json, serde_json::json!({"name": "GPU-A", "alias": "gpu-a", "hostname": "10.0.0.5", "user": "ana", "port": 2222}));
    }

    fn managed(alias: &str, key: Option<&str>) -> ManagedHost {
        ManagedHost {
            alias: alias.into(),
            host: "203.0.113.7".into(),
            user: Some("user".into()),
            port: Some(2222),
            identity_file: key.map(String::from),
            auth: None,
        }
    }

    #[test]
    fn managed_block_is_added_once_and_kept_apart() {
        let mine = "Host gpu-a\n  HostName 10.0.0.5\n";
        let k = managed("ark-lab-1", Some(r"C:\Users\me\.ssh\id_rsa_lab_1"));
        let once = upsert_managed(mine, &k).unwrap().unwrap();
        assert_eq!(
            once,
            format!(
                "Host gpu-a\n  HostName 10.0.0.5\n\n{MANAGED_BEGIN}\nHost ark-lab-1\n  HostName 203.0.113.7\n  Port 2222\n  User user\n  \
                 IdentityFile C:/Users/me/.ssh/id_rsa_lab_1\n  IdentitiesOnly yes\n{MANAGED_END}\n"
            )
        );
        // Same entry again: nothing to write.
        assert_eq!(upsert_managed(&once, &k).unwrap(), None);
        // A second host joins the block; a changed one is replaced in place.
        let two = upsert_managed(&once, &managed("ark-b", None)).unwrap().unwrap();
        assert_eq!(two.matches(MANAGED_BEGIN).count(), 1);
        assert!(two.contains(&format!("Host ark-b\n  HostName 203.0.113.7\n  Port 2222\n  User user\n{MANAGED_END}")));
        let moved = upsert_managed(&two, &managed("ark-lab-1", Some("/home/me/.ssh/k2"))).unwrap().unwrap();
        assert!(moved.contains("IdentityFile /home/me/.ssh/k2") && !moved.contains("id_rsa_lab_1"));
        assert!(moved.find("Host ark-lab-1").unwrap() < moved.find("Host ark-b").unwrap());
        assert!(moved.starts_with(mine));
        assert_eq!(upsert_managed(&moved, &managed("ark-b", None)).unwrap(), None);
        // Text after the block stays; CRLF files stay CRLF; empty files work.
        let after = format!("{once}Host z\n  User z\n");
        let updated = upsert_managed(&after, &managed("ark-b", None)).unwrap().unwrap();
        assert!(updated.ends_with(&format!("{MANAGED_END}\nHost z\n  User z\n")));
        let crlf = upsert_managed("Host a\r\n  User a\r\n", &k).unwrap().unwrap();
        assert!(crlf.contains("\r\n") && !crlf.replace("\r\n", "").contains('\n'));
        assert_eq!(upsert_managed(&crlf, &k).unwrap(), None);
        assert!(upsert_managed("", &k).unwrap().unwrap().starts_with(MANAGED_BEGIN));
        // A half-removed block is not guessed at.
        assert!(upsert_managed(&format!("{MANAGED_BEGIN}\nHost ark-x\n"), &k).is_err());
    }

    #[test]
    fn managed_entries_refuse_bad_input() {
        let ok = managed("ark-a", Some("~/.ssh/my key"));
        assert!(managed_entry(&ok).unwrap().contains("IdentityFile \"~/.ssh/my key\""));
        for bad in [
            ManagedHost { alias: "gpu-a".into(), ..managed("x", None) },
            ManagedHost { alias: "ark-a b".into(), ..managed("x", None) },
            ManagedHost { alias: "ark-a\nHost *".into(), ..managed("x", None) },
            ManagedHost { host: "h\n  ProxyCommand calc".into(), ..managed("ark-a", None) },
            ManagedHost { host: "a b".into(), ..managed("ark-a", None) },
            ManagedHost { host: "\"h\"".into(), ..managed("ark-a", None) },
            ManagedHost { host: "-oProxyCommand=calc".into(), ..managed("ark-a", None) },
            ManagedHost { user: Some("u\"x".into()), ..managed("ark-a", None) },
            ManagedHost { user: Some("u x".into()), ..managed("ark-a", None) },
            ManagedHost { port: Some(0), ..managed("ark-a", None) },
            managed("ark-a", Some("relative/key")),
            managed("ark-a", Some("C:/k\"ey")),
            managed("ark-a", Some("/k\n  ProxyCommand calc")),
            managed("ark-a", Some("//server/share/key")),
        ] {
            assert!(managed_entry(&bad).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn key_files_are_found_by_header() {
        let dir = std::env::temp_dir().join(format!("ark-keys-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("id_rsa_lab_1"), concat!("-----BEGIN OPENSSH ", "PRIVATE KEY-----\nxx\n")).unwrap();
        std::fs::write(dir.join("id_rsa_lab_1.pub"), "ssh-rsa AAAA").unwrap();
        std::fs::write(dir.join("config"), "Host x\n").unwrap();
        std::fs::write(dir.join("notes.txt"), "hello").unwrap();
        let found = key_candidates(&dir);
        let _ = std::fs::remove_dir_all(&dir);
        assert_eq!(found.len(), 1);
        assert!(found[0].ends_with("id_rsa_lab_1"));
    }

    #[test]
    fn config_written_with_backup() {
        let dir = std::env::temp_dir().join(format!("ark-sshcfg-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let config = dir.join("config");
        std::fs::write(&config, "Host mine\n").unwrap();
        let text = upsert_managed("Host mine\n", &managed("ark-a", None)).unwrap().unwrap();
        write_config(&config, &text).unwrap();
        write_config(&config, &text).unwrap();
        let backup = std::fs::read_to_string(dir.join("config.ark-backup")).unwrap();
        let now = std::fs::read_to_string(&config).unwrap();
        let tmp_left = dir.join("config.ark-tmp").exists();
        let _ = std::fs::remove_dir_all(&dir);
        assert_eq!(backup, "Host mine\n");
        assert_eq!(now, text);
        assert!(!tmp_left);
    }

    #[test]
    fn key_and_password_entries() {
        let key = r"C:\Users\me\.ssh\id_rsa_lab_1";
        // Key login: IdentityFile + IdentitiesOnly (no password prompt).
        let with_key = managed_entry(&ManagedHost { auth: Some("key".into()), ..managed("ark-a", Some(key)) }).unwrap();
        assert!(with_key.ends_with("\n  IdentityFile C:/Users/me/.ssh/id_rsa_lab_1\n  IdentitiesOnly yes"));
        // Password login: neither line, even when a key path is remembered;
        // ssh / VS Code show their own prompt.
        let pwd = managed_entry(&ManagedHost { auth: Some("password".into()), ..managed("ark-a", Some(key)) }).unwrap();
        assert_eq!(pwd, "Host ark-a\n  HostName 203.0.113.7\n  Port 2222\n  User user");
        assert_eq!(managed_entry(&managed("ark-a", None)).unwrap(), pwd);
        // Switching key -> password replaces the entry in place.
        let text = upsert_managed("", &ManagedHost { auth: Some("key".into()), ..managed("ark-a", Some(key)) }).unwrap().unwrap();
        let switched = upsert_managed(&text, &ManagedHost { auth: Some("password".into()), ..managed("ark-a", Some(key)) }).unwrap().unwrap();
        assert!(!switched.contains("IdentityFile") && !switched.contains("IdentitiesOnly"));
        assert_eq!(switched.matches("Host ark-a").count(), 1);
    }

    #[test]
    fn terminal_with_key_or_password() {
        let key = Some(r"C:\Users\me\.ssh\id_rsa_lab_1".to_string());
        let t = SshTarget { identity_file: key.clone(), ..target("", "203.0.113.7", "user", Some(2222)) };
        assert_eq!(
            ssh_args(&t, None).unwrap(),
            v(&["-t", "-p", "2222", "-i", r"C:\Users\me\.ssh\id_rsa_lab_1", "-o", "IdentitiesOnly=yes", "--", "user@203.0.113.7"])
        );
        // Password: no -i; ssh prompts in the terminal.
        let p = SshTarget { auth: Some("password".into()), ..t.clone() };
        assert_eq!(ssh_args(&p, None).unwrap(), v(&["-t", "-p", "2222", "--", "user@203.0.113.7"]));
        // An alias carries its own key.
        let a = SshTarget { identity_file: key, ..target("ark-lab-1", "203.0.113.7", "user", Some(2222)) };
        assert_eq!(ssh_args(&a, None).unwrap(), v(&["-t", "--", "ark-lab-1"]));
        for bad in ["-oProxyCommand=calc", "relative/key", "/k\nx", "//srv/share/k", r"\srv\share\k"] {
            let b = SshTarget { identity_file: Some(bad.into()), ..target("", "h", "", None) };
            assert!(ssh_args(&b, None).is_err(), "{bad:?}");
        }
        assert!(check_identity("~/.ssh/my key").is_ok());
    }
}
