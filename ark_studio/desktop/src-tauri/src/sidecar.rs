//! The bundled `ark-server` as a private loopback sidecar.
//!
//! * Bound to 127.0.0.1 on the preferred port, or on a free port when that
//!   one is taken (the UI says so).
//! * `ARK_DESKTOP_SECRET` (environment, never argv) turns on ark-server's
//!   desktop gate: only this app's window (cookie from a one-time HMAC-signed
//!   claim URL) and tokens the server issued itself are admitted, so other
//!   local users and processes cannot drive the owner-level local server.
//! * stdout/stderr go to `logs/ark-server.log` (rotated at 5 MB).
//! * Stop: close stdin (ark-server then shuts down gracefully), wait, then
//!   kill. On Windows the sidecar runs in a kill-on-close job object, so it
//!   (and the agent processes it started) also ends if the app crashes; on
//!   Unix the stdin EOF does the same.
//! * `sidecar.json` records the running sidecar; at start a leftover one
//!   (crash, interrupted upgrade) is stopped.

use std::fs::{File, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use hmac::{Hmac, Mac};
use sha2::Sha256;

use crate::paths;

const LOG_ROTATE_BYTES: u64 = 5 * 1024 * 1024;
const STOP_GRACE: Duration = Duration::from_secs(10);

pub struct Sidecar {
    child: Child,
    stdin: Option<ChildStdin>,
    pub port: u16,
    pub requested_port: u16,
    pub root: PathBuf,
    secret: String,
    stopped: bool,
    #[cfg(windows)]
    job: Option<win::Job>,
}

fn exe_name() -> &'static str {
    if cfg!(windows) {
        "ark-server.exe"
    } else {
        "ark-server"
    }
}

/// The sidecar binary: next to the app executable in every bundle (Tauri
/// externalBin), or `ARK_DESKTOP_SERVER` for development.
pub fn server_exe() -> Result<PathBuf, String> {
    if let Some(p) = std::env::var_os("ARK_DESKTOP_SERVER").filter(|v| !v.is_empty()) {
        return Ok(PathBuf::from(p));
    }
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let candidate = exe.parent().unwrap_or(Path::new(".")).join(exe_name());
    if candidate.is_file() {
        Ok(candidate)
    } else {
        Err(format!(
            "The bundled ark-server was not found at {}. Reinstall ARK Studio.",
            candidate.display()
        ))
    }
}

fn port_free(port: u16) -> bool {
    TcpListener::bind(("127.0.0.1", port)).is_ok()
}

/// The port to bind: the preferred one, else the one used the last time the
/// preferred port was taken (`fallback`), else none (a new free port). The
/// local page's origin includes the port, and the browser storage behind it
/// (theme, language, layout) is per origin: a new port on every start would
/// make those settings look lost after a restart or an update.
pub fn pick_port(preferred: u16, fallback: u16, free: impl Fn(u16) -> bool) -> Option<u16> {
    [preferred, fallback].into_iter().find(|&p| p != 0 && free(p))
}

fn any_free_port() -> Result<u16, String> {
    TcpListener::bind(("127.0.0.1", 0))
        .and_then(|l| l.local_addr())
        .map(|a| a.port())
        .map_err(|e| format!("No free loopback port: {e}"))
}

pub fn random_hex(bytes: usize) -> String {
    let mut buf = vec![0u8; bytes];
    getrandom::getrandom(&mut buf).expect("OS random source");
    hex::encode(buf)
}

fn open_log() -> Result<File, String> {
    let log = paths::server_log();
    paths::ensure_private_dir(&paths::logs_dir()).map_err(|e| e.to_string())?;
    if std::fs::metadata(&log).map(|m| m.len() > LOG_ROTATE_BYTES).unwrap_or(false) {
        let _ = std::fs::rename(&log, log.with_extension("log.1"));
    }
    let mut opts = OpenOptions::new();
    opts.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut file = opts.open(&log).map_err(|e| format!("Cannot open {}: {e}", log.display()))?;
    let stamp = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    let _ = writeln!(file, "\n--- ARK Studio desktop: starting ark-server (unix time {stamp}) ---");
    Ok(file)
}

/// The last `lines` lines of the server log, with the agent token hidden.
pub fn log_tail(lines: usize) -> String {
    let Ok(mut f) = File::open(paths::server_log()) else {
        return String::new();
    };
    let len = f.metadata().map(|m| m.len()).unwrap_or(0);
    let _ = f.seek(SeekFrom::Start(len.saturating_sub(16 * 1024)));
    let mut buf = Vec::new();
    let _ = f.read_to_end(&mut buf);
    let text = String::from_utf8_lossy(&buf);
    let all: Vec<String> = text
        .lines()
        .map(|l| {
            if l.trim_start().starts_with("Agent token:") {
                "Agent token: (hidden)".to_string()
            } else {
                l.to_string()
            }
        })
        .collect();
    all[all.len().saturating_sub(lines)..].join("\n")
}

/// GET /api/health on the loopback port; true for an ARK answer.
pub fn health_ok(port: u16) -> bool {
    let addr = SocketAddr::from(([127, 0, 0, 1], port));
    let Ok(mut s) = TcpStream::connect_timeout(&addr, Duration::from_millis(500)) else {
        return false;
    };
    let _ = s.set_read_timeout(Some(Duration::from_secs(2)));
    let _ = s.set_write_timeout(Some(Duration::from_secs(2)));
    let req = format!("GET /api/health HTTP/1.0\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n");
    if s.write_all(req.as_bytes()).is_err() {
        return false;
    }
    let mut body = Vec::new();
    let _ = s.take(64 * 1024).read_to_end(&mut body);
    let text = String::from_utf8_lossy(&body);
    text.starts_with("HTTP/1.") && text.lines().next().is_some_and(|l| l.contains(" 200")) && text.contains("arkstudio")
}

/// Starts ark-server on `preferred`, else on `fallback` (see [`pick_port`]),
/// else on a free port. `proxy_env`: the proxy variables to set (`Some`) or
/// remove (`None`) for the server's outbound requests (proxy.rs).
pub fn start(root: &Path, preferred: u16, fallback: u16, proxy_env: &[(&str, Option<String>)]) -> Result<Sidecar, String> {
    let exe = server_exe()?;
    std::fs::create_dir_all(root).map_err(|e| format!("Cannot create the project folder {}: {e}", root.display()))?;
    paths::ensure_private_dir(&paths::private_dir()).map_err(|e| e.to_string())?;
    let port = match pick_port(preferred, fallback, port_free) {
        Some(p) => p,
        None => any_free_port()?,
    };
    let secret = random_hex(32);
    let log = open_log()?;
    let log2 = log.try_clone().map_err(|e| e.to_string())?;

    let mut cmd = Command::new(&exe);
    cmd.arg("serve")
        .arg("--root")
        .arg(root)
        .arg("--host")
        .arg("127.0.0.1")
        .arg("--port")
        .arg(port.to_string())
        .env("ARK_DESKTOP_SECRET", &secret)
        .env_remove("ARK_PUBLIC")
        .stdin(Stdio::piped())
        .stdout(Stdio::from(log))
        .stderr(Stdio::from(log2))
        .current_dir(root);
    if std::env::var_os("ARK_PRIVATE_DIR").is_none() {
        cmd.env("ARK_PRIVATE_DIR", paths::private_dir());
    }
    // The server keeps the organization database, agent sessions and project
    // snapshots next to the project folder by default. For the default folder
    // next to the app that would be the app folder itself: keep them in data\.
    let layout = paths::layout();
    if layout.kind == paths::Kind::Install && root == layout.projects.as_path() {
        if std::env::var_os("ARK_ORG_DB").is_none() {
            cmd.env("ARK_ORG_DB", layout.data.join("org.sqlite3"));
        }
        if std::env::var_os("ARK_BLACKBOX_DIR").is_none() {
            cmd.env("ARK_BLACKBOX_DIR", layout.data.join("blackbox"));
        }
        cmd.arg("--sessions-root").arg(layout.data.join("agent-sessions"));
    }
    for (key, value) in proxy_env {
        match value {
            Some(v) => cmd.env(key, v),
            None => cmd.env_remove(key),
        };
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NO_WINDOW);
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }
    let mut child = cmd.spawn().map_err(|e| format!("Cannot start {}: {e}", exe.display()))?;
    let stdin = child.stdin.take();
    #[cfg(windows)]
    let job = win::Job::assign(&child);
    let record = serde_json::json!({ "pid": child.id(), "exe": exe, "port": port });
    let _ = std::fs::write(paths::sidecar_file(), record.to_string());
    Ok(Sidecar {
        child,
        stdin,
        port,
        requested_port: preferred,
        root: root.to_path_buf(),
        secret,
        stopped: false,
        #[cfg(windows)]
        job,
    })
}

impl Sidecar {
    pub fn origin(&self) -> String {
        format!("http://127.0.0.1:{}", self.port)
    }

    pub fn alive(&mut self) -> bool {
        !self.stopped && matches!(self.child.try_wait(), Ok(None))
    }

    /// Waits until /api/health answers, the process exits, or `timeout`.
    pub fn wait_ready(&mut self, timeout: Duration) -> Result<(), String> {
        let deadline = Instant::now() + timeout;
        loop {
            if let Ok(Some(status)) = self.child.try_wait() {
                self.stopped = true;
                return Err(format!("ark-server stopped during startup ({status}).\n\n{}", log_tail(30)));
            }
            if health_ok(self.port) {
                return Ok(());
            }
            if Instant::now() > deadline {
                self.stop();
                return Err(format!(
                    "ark-server did not answer within {} s.\n\n{}",
                    timeout.as_secs(),
                    log_tail(30)
                ));
            }
            std::thread::sleep(Duration::from_millis(150));
        }
    }

    /// A one-time URL that signs this browser in to the sidecar and then
    /// opens `next` (a path on the server).
    pub fn claim_url(&self, next: &str) -> String {
        let ts = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
        let nonce = random_hex(12);
        let payload = format!("{ts}.{nonce}");
        let mut mac = Hmac::<Sha256>::new_from_slice(self.secret.as_bytes()).expect("hmac key");
        mac.update(format!("claim:{payload}").as_bytes());
        let sig = hex::encode(mac.finalize().into_bytes());
        let next = if next.starts_with('/') && !next.starts_with("//") { next } else { "/" };
        let next: String = url::form_urlencoded::byte_serialize(next.as_bytes()).collect();
        format!("{}/__ark_desktop/claim?t={payload}.{sig}&next={next}", self.origin())
    }

    /// Graceful stop (stdin EOF), then kill after the grace period.
    pub fn stop(&mut self) {
        if self.stopped {
            return;
        }
        self.stopped = true;
        drop(self.stdin.take());
        let deadline = Instant::now() + STOP_GRACE;
        while Instant::now() < deadline {
            if !matches!(self.child.try_wait(), Ok(None)) {
                break;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        if matches!(self.child.try_wait(), Ok(None)) {
            #[cfg(windows)]
            if let Some(job) = &self.job {
                job.terminate();
            }
            #[cfg(unix)]
            unsafe {
                libc::kill(-(self.child.id() as i32), libc::SIGKILL);
            }
            let _ = self.child.kill();
            let _ = self.child.wait();
        }
        let _ = std::fs::remove_file(paths::sidecar_file());
    }
}

impl Drop for Sidecar {
    fn drop(&mut self) {
        self.stop();
    }
}

/// Stops a sidecar left over from a previous run (crash, upgrade), but only
/// if the recorded PID still runs the recorded ark-server executable.
pub fn kill_orphan() {
    let file = paths::sidecar_file();
    let Some(record) = std::fs::read(&file).ok().and_then(|b| serde_json::from_slice::<serde_json::Value>(&b).ok())
    else {
        return;
    };
    let _ = std::fs::remove_file(&file);
    let pid = record.get("pid").and_then(|v| v.as_u64()).unwrap_or(0) as u32;
    let exe = record.get("exe").and_then(|v| v.as_str()).unwrap_or("").to_string();
    if pid == 0 || exe.is_empty() {
        return;
    }
    kill_if_matches(pid, &exe);
}

#[cfg(windows)]
fn kill_if_matches(pid: u32, exe: &str) {
    win::kill_if_matches(pid, exe);
}

/// Stops all ark-server processes whose executable is in this app's folder.
pub fn kill_in_app_dir() {
    let Ok(exe) = std::env::current_exe() else { return };
    let Some(dir) = exe.parent() else { return };
    let target = dir.join(exe_name()).to_string_lossy().to_string();
    #[cfg(windows)]
    for pid in win::pids_named(exe_name()) {
        win::kill_if_matches(pid, &target);
    }
    #[cfg(target_os = "linux")]
    if let Ok(entries) = std::fs::read_dir("/proc") {
        for e in entries.flatten() {
            if let Ok(pid) = e.file_name().to_string_lossy().parse::<u32>() {
                kill_if_matches(pid, &target);
            }
        }
    }
}

#[cfg(unix)]
fn kill_if_matches(pid: u32, exe: &str) {
    if unsafe { libc::kill(pid as i32, 0) } != 0 {
        return;
    }
    let running = running_exe(pid);
    let want = Path::new(exe).file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
    if running.as_deref().is_some_and(|r| r == exe || r.ends_with(&format!("/{want}")) || r == want) {
        unsafe {
            libc::kill(-(pid as i32), libc::SIGTERM);
        }
        std::thread::sleep(Duration::from_secs(1));
        unsafe {
            if libc::kill(pid as i32, 0) == 0 {
                libc::kill(-(pid as i32), libc::SIGKILL);
            }
        }
    }
}

#[cfg(target_os = "linux")]
fn running_exe(pid: u32) -> Option<String> {
    std::fs::read_link(format!("/proc/{pid}/exe")).ok().map(|p| p.to_string_lossy().to_string())
}

#[cfg(all(unix, not(target_os = "linux")))]
fn running_exe(pid: u32) -> Option<String> {
    let out = Command::new("ps").args(["-p", &pid.to_string(), "-o", "comm="]).output().ok()?;
    let text = String::from_utf8_lossy(&out.stdout).trim().to_string();
    (!text.is_empty()).then_some(text)
}

#[cfg(windows)]
mod win {
    use std::os::windows::io::AsRawHandle;
    use std::process::Child;

    use windows_sys::Win32::Foundation::{CloseHandle, HANDLE};
    use windows_sys::Win32::System::JobObjects::{
        AssignProcessToJobObject, CreateJobObjectW, JobObjectExtendedLimitInformation, SetInformationJobObject,
        TerminateJobObject, JOBOBJECT_EXTENDED_LIMIT_INFORMATION, JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
    };
    use windows_sys::Win32::System::Threading::{
        OpenProcess, QueryFullProcessImageNameW, TerminateProcess, PROCESS_QUERY_LIMITED_INFORMATION,
        PROCESS_TERMINATE,
    };

    pub struct Job(HANDLE);
    unsafe impl Send for Job {}

    impl Job {
        pub fn assign(child: &Child) -> Option<Job> {
            unsafe {
                let job = CreateJobObjectW(std::ptr::null(), std::ptr::null());
                if job.is_null() {
                    return None;
                }
                let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
                info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
                let ok = SetInformationJobObject(
                    job,
                    JobObjectExtendedLimitInformation,
                    &info as *const _ as *const core::ffi::c_void,
                    std::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
                );
                if ok == 0 || AssignProcessToJobObject(job, child.as_raw_handle() as HANDLE) == 0 {
                    CloseHandle(job);
                    return None;
                }
                Some(Job(job))
            }
        }

        pub fn terminate(&self) {
            unsafe {
                TerminateJobObject(self.0, 1);
            }
        }
    }

    impl Drop for Job {
        fn drop(&mut self) {
            unsafe {
                CloseHandle(self.0);
            }
        }
    }

    /// PIDs of processes with this image name (case-insensitive).
    pub fn pids_named(name: &str) -> Vec<u32> {
        use windows_sys::Win32::Foundation::INVALID_HANDLE_VALUE;
        use windows_sys::Win32::System::Diagnostics::ToolHelp::{
            CreateToolhelp32Snapshot, Process32FirstW, Process32NextW, PROCESSENTRY32W, TH32CS_SNAPPROCESS,
        };
        let mut out = Vec::new();
        unsafe {
            let snap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
            if snap == INVALID_HANDLE_VALUE {
                return out;
            }
            let mut entry: PROCESSENTRY32W = std::mem::zeroed();
            entry.dwSize = std::mem::size_of::<PROCESSENTRY32W>() as u32;
            let mut ok = Process32FirstW(snap, &mut entry) != 0;
            while ok {
                let len = entry.szExeFile.iter().position(|c| *c == 0).unwrap_or(entry.szExeFile.len());
                if String::from_utf16_lossy(&entry.szExeFile[..len]).eq_ignore_ascii_case(name) {
                    out.push(entry.th32ProcessID);
                }
                ok = Process32NextW(snap, &mut entry) != 0;
            }
            CloseHandle(snap);
        }
        out
    }

    pub fn kill_if_matches(pid: u32, exe: &str) {
        unsafe {
            let h = OpenProcess(PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION, 0, pid);
            if h.is_null() {
                return;
            }
            let mut buf = [0u16; 1024];
            let mut len = buf.len() as u32;
            if QueryFullProcessImageNameW(h, 0, buf.as_mut_ptr(), &mut len) != 0 {
                let running = String::from_utf16_lossy(&buf[..len as usize]);
                if running.eq_ignore_ascii_case(exe) {
                    TerminateProcess(h, 1);
                }
            }
            CloseHandle(h);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::pick_port;

    #[test]
    fn port_choice_keeps_the_origin_stable() {
        let all_free = |_| true;
        assert_eq!(pick_port(18890, 51384, all_free), Some(18890));
        // the preferred port is taken: the one used last time, not a new one
        assert_eq!(pick_port(18890, 51384, |p| p != 18890), Some(51384));
        // both taken (or no earlier fallback): the caller picks a free port
        assert_eq!(pick_port(18890, 51384, |p| p != 18890 && p != 51384), None);
        assert_eq!(pick_port(18890, 0, |p| p != 18890), None);
        assert_eq!(pick_port(0, 0, all_free), None);
    }
}