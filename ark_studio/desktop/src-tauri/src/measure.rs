//! Folder sizes of this computer's resource locations (2026-11-20).
//!
//! A registered path (code repository, workspace, storage, database) lives on
//! one device, so only that device can measure it. ARK pages in the desktop
//! app ask for this computer's device id (`device_id`, the same id as
//! `ark-server agent host-id`: sha256("ark|" + OS machine id), 12 hex) and
//! for the sizes of its registered paths (`measure_paths`), then report them
//! to the server (setup action "location-stats").
//!
//! The walk is read-only and bounded like the server's (internal/platform/
//! dirsize): 20 s and two million entries per path (the result is then a
//! lower bound, `capped`), symbolic links and junctions are counted but never
//! followed, other file systems inside the tree are not entered (Unix),
//! network paths are not measured, unreadable folders are skipped and
//! counted. It runs on its own thread at background priority. Only sizes and
//! counts are returned, never names.

use serde::Serialize;
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

pub const MAX_DURATION: Duration = Duration::from_secs(20);
pub const MAX_ENTRIES: u64 = 2_000_000;
/// Most paths one call measures, and the time all of them may take.
pub const MAX_PATHS: usize = 50;
pub const TOTAL_BUDGET: Duration = Duration::from_secs(90);

#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Stat {
    pub path: String,
    pub kind: &'static str,
    pub bytes: u64,
    pub files: u64,
    pub git: bool,
    pub git_bytes: u64,
    pub git_files: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub capped: Option<&'static str>,
    pub skipped: u64,
    pub symlinks: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// The device id of an OS machine id (hostid.FromMachineID in the server).
pub fn device_id_from_machine_id(raw: &str) -> String {
    let sum = Sha256::digest(format!("ark|{}", raw.trim()).as_bytes());
    hex::encode(sum)[..12].to_string()
}

/// This computer's device id (None when the OS has no machine id).
pub fn device_id() -> Option<String> {
    machine_id().filter(|m| !m.trim().is_empty()).map(|m| device_id_from_machine_id(&m))
}

#[cfg(target_os = "linux")]
fn machine_id() -> Option<String> {
    ["/etc/machine-id", "/var/lib/dbus/machine-id"]
        .iter()
        .filter_map(|p| std::fs::read_to_string(p).ok())
        .map(|s| s.trim().to_string())
        .find(|s| !s.is_empty())
}

#[cfg(target_os = "macos")]
fn machine_id() -> Option<String> {
    let out = std::process::Command::new("ioreg").args(["-rd1", "-c", "IOPlatformExpertDevice"]).output().ok()?;
    let text = String::from_utf8_lossy(&out.stdout);
    let line = text.lines().find(|l| l.contains("\"IOPlatformUUID\""))?;
    // "IOPlatformUUID" = "XXXXXXXX-...."
    line.split('"').nth(3).map(str::to_string)
}

#[cfg(windows)]
fn machine_id() -> Option<String> {
    use windows_sys::Win32::System::Registry::{RegGetValueW, HKEY_LOCAL_MACHINE, RRF_RT_REG_SZ, RRF_SUBKEY_WOW6464KEY};
    let key: Vec<u16> = "SOFTWARE\\Microsoft\\Cryptography\0".encode_utf16().collect();
    let name: Vec<u16> = "MachineGuid\0".encode_utf16().collect();
    let mut buf = [0u16; 128];
    let mut size = (buf.len() * 2) as u32;
    // SAFETY: the key and value names are NUL-terminated; buf holds `size` bytes.
    let rc = unsafe {
        RegGetValueW(
            HKEY_LOCAL_MACHINE,
            key.as_ptr(),
            name.as_ptr(),
            RRF_RT_REG_SZ | RRF_SUBKEY_WOW6464KEY,
            std::ptr::null_mut(),
            buf.as_mut_ptr().cast(),
            &mut size,
        )
    };
    if rc != 0 {
        return None;
    }
    let n = (size as usize / 2).min(buf.len());
    Some(String::from_utf16_lossy(&buf[..n]).trim_end_matches('\0').trim().to_string())
}

#[cfg(not(any(target_os = "linux", target_os = "macos", windows)))]
fn machine_id() -> Option<String> {
    None
}

/// Background priority for the calling thread only (it is not reused).
fn lower_thread_priority() {
    #[cfg(windows)]
    {
        use windows_sys::Win32::System::Threading::{GetCurrentThread, SetThreadPriority, THREAD_MODE_BACKGROUND_BEGIN};
        // SAFETY: the pseudo handle of the calling thread.
        unsafe {
            SetThreadPriority(GetCurrentThread(), THREAD_MODE_BACKGROUND_BEGIN);
        }
    }
    #[cfg(target_os = "linux")]
    {
        const WHO_PROCESS: libc::c_long = 1;
        const CLASS_IDLE: libc::c_long = 3;
        // SAFETY: ioprio_set and setpriority with who = 0 change the calling thread only.
        unsafe {
            libc::syscall(libc::SYS_ioprio_set, WHO_PROCESS, 0 as libc::c_long, CLASS_IDLE << 13);
            libc::setpriority(libc::PRIO_PROCESS, 0, 19);
        }
    }
}

/// Whether a path is on a network file system (not measured).
pub fn is_network_path(path: &Path) -> bool {
    let text = path.to_string_lossy();
    if text.starts_with("\\\\") || text.starts_with("//") {
        return true;
    }
    network_mount(path)
}

#[cfg(windows)]
fn network_mount(path: &Path) -> bool {
    use std::path::Component;
    use windows_sys::Win32::Storage::FileSystem::GetDriveTypeW;
    const DRIVE_REMOTE: u32 = 4;
    let Some(Component::Prefix(prefix)) = path.components().next() else {
        return false;
    };
    let root: Vec<u16> = format!("{}\\\0", prefix.as_os_str().to_string_lossy()).encode_utf16().collect();
    // SAFETY: root is a NUL-terminated drive root such as "C:\".
    unsafe { GetDriveTypeW(root.as_ptr()) == DRIVE_REMOTE }
}

#[cfg(target_os = "linux")]
fn network_mount(path: &Path) -> bool {
    const NETWORK: &[&str] = &[
        "nfs", "nfs4", "cifs", "smb3", "smbfs", "fuse.sshfs", "sshfs", "9p", "afs", "ceph", "glusterfs",
        "fuse.glusterfs", "lustre", "davfs", "fuse.davfs2", "fuse.rclone", "fuse.gcsfuse", "fuse.s3fs",
        "fuse.cephfs", "beegfs", "gpfs",
    ];
    let Ok(info) = std::fs::read_to_string("/proc/self/mountinfo") else {
        return false;
    };
    let path = path.to_string_lossy();
    let mut best = (0usize, String::new());
    for line in info.lines() {
        let Some((left, right)) = line.split_once(" - ") else { continue };
        let (Some(mp), Some(fs)) = (left.split_whitespace().nth(4), right.split_whitespace().next()) else {
            continue;
        };
        let mp = mp.replace("\\040", " ");
        let inside = mp == "/" || path == mp.as_str() || path.starts_with(&format!("{mp}/"));
        if inside && mp.len() >= best.0 {
            best = (mp.len(), fs.to_string());
        }
    }
    NETWORK.contains(&best.1.as_str())
}

#[cfg(not(any(windows, target_os = "linux")))]
fn network_mount(_: &Path) -> bool {
    false
}

#[cfg(unix)]
fn device_of(meta: &std::fs::Metadata) -> Option<u64> {
    use std::os::unix::fs::MetadataExt;
    Some(meta.dev())
}

#[cfg(not(unix))]
fn device_of(_: &std::fs::Metadata) -> Option<u64> {
    None
}

/// "~" and "~/x" are the user's home.
fn expand_home(raw: &str) -> PathBuf {
    let home = std::env::var_os("HOME").or_else(|| std::env::var_os("USERPROFILE"));
    match (raw, home) {
        ("~", Some(h)) => PathBuf::from(h),
        (r, Some(h)) if r.starts_with("~/") || r.starts_with("~\\") => PathBuf::from(h).join(&r[2..]),
        (r, _) => PathBuf::from(r),
    }
}

/// Measures one path within the caps.
pub fn measure(raw: &str, max_duration: Duration, max_entries: u64) -> Stat {
    let mut st = Stat { path: raw.to_string(), kind: "dir", ..Default::default() };
    let path = expand_home(raw.trim());
    if !path.is_absolute() {
        st.kind = "error";
        st.error = Some("not an absolute path".into());
        return st;
    }
    if is_network_path(&path) {
        st.kind = "network";
        return st;
    }
    // A link at the root itself is resolved once; links inside never are.
    let root = match std::fs::canonicalize(&path) {
        Ok(p) => p,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            st.kind = "missing";
            return st;
        }
        Err(e) => {
            st.kind = "error";
            st.error = Some(e.to_string());
            return st;
        }
    };
    let meta = match std::fs::symlink_metadata(&root) {
        Ok(m) => m,
        Err(e) => {
            st.kind = "error";
            st.error = Some(e.to_string());
            return st;
        }
    };
    if !meta.is_dir() {
        st.kind = "file";
        if meta.is_file() {
            st.bytes = meta.len();
            st.files = 1;
        }
        return st;
    }
    let root_dev = device_of(&meta);
    st.git = std::fs::symlink_metadata(root.join(".git")).map(|m| m.is_dir()).unwrap_or(false);
    let start = Instant::now();
    let mut entries: u64 = 0;
    let mut stack: Vec<(PathBuf, bool)> = vec![(root.clone(), false)];
    while let Some((dir, in_git)) = stack.pop() {
        let Ok(read) = std::fs::read_dir(&dir) else {
            st.skipped += 1;
            continue;
        };
        for entry in read {
            entries += 1;
            if entries > max_entries {
                st.capped = Some("entries");
                return st;
            }
            if entries % 256 == 0 && start.elapsed() >= max_duration {
                st.capped = Some("time");
                return st;
            }
            let Ok(entry) = entry else {
                st.skipped += 1;
                continue;
            };
            let Ok(ft) = entry.file_type() else {
                st.skipped += 1;
                continue;
            };
            let git = in_git || (dir == root && entry.file_name() == ".git");
            if ft.is_symlink() {
                st.symlinks += 1;
            } else if ft.is_dir() {
                if let (Some(rd), Ok(m)) = (root_dev, entry.metadata()) {
                    if device_of(&m) != Some(rd) {
                        continue; // another file system mounted inside
                    }
                }
                stack.push((entry.path(), git));
            } else if ft.is_file() {
                let Ok(m) = entry.metadata() else {
                    st.skipped += 1;
                    continue;
                };
                st.files += 1;
                st.bytes += m.len();
                if git {
                    st.git_files += 1;
                    st.git_bytes += m.len();
                }
            }
        }
    }
    st
}

/// Measures every path (at most MAX_PATHS, within TOTAL_BUDGET) on a fresh
/// background-priority thread; paths left when the budget runs out are not
/// returned.
pub fn measure_all(paths: Vec<String>) -> Vec<Stat> {
    std::thread::spawn(move || {
        lower_thread_priority();
        let start = Instant::now();
        let mut out = Vec::new();
        for p in paths.into_iter().take(MAX_PATHS) {
            let left = TOTAL_BUDGET.saturating_sub(start.elapsed());
            if left.is_zero() {
                break;
            }
            out.push(measure(&p, MAX_DURATION.min(left), MAX_ENTRIES));
        }
        out
    })
    .join()
    .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn tree(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("ark-measure-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(dir.join("src")).unwrap();
        fs::create_dir_all(dir.join(".git/objects")).unwrap();
        fs::write(dir.join("a.txt"), [0u8; 10]).unwrap();
        fs::write(dir.join("src/b.rs"), [0u8; 20]).unwrap();
        fs::write(dir.join(".git/objects/p"), [0u8; 100]).unwrap();
        fs::write(dir.join(".git/HEAD"), [0u8; 5]).unwrap();
        dir
    }

    #[test]
    fn device_id_matches_server() {
        // hostid.FromMachineID("0123456789abcdef0123456789abcdef\n") in the Go server.
        assert_eq!(device_id_from_machine_id("0123456789abcdef0123456789abcdef\n"), "14df97e70ef7");
    }

    #[test]
    fn measures_tree_and_git_share() {
        let dir = tree("git");
        let st = measure(dir.to_str().unwrap(), MAX_DURATION, MAX_ENTRIES);
        assert_eq!((st.kind, st.bytes, st.files), ("dir", 135, 4));
        assert!(st.git);
        assert_eq!((st.git_bytes, st.git_files), (105, 2));
        assert_eq!(st.capped, None);
        let file = measure(dir.join("a.txt").to_str().unwrap(), MAX_DURATION, MAX_ENTRIES);
        assert_eq!((file.kind, file.bytes, file.files), ("file", 10, 1));
        assert_eq!(measure(dir.join("nope").to_str().unwrap(), MAX_DURATION, MAX_ENTRIES).kind, "missing");
        assert_eq!(measure("relative/path", MAX_DURATION, MAX_ENTRIES).kind, "error");
        let capped = measure(dir.to_str().unwrap(), MAX_DURATION, 2);
        assert_eq!(capped.capped, Some("entries"));
        assert!(capped.files <= 2);
        let all = measure_all(vec![dir.to_string_lossy().into_owned(); 3]);
        assert_eq!(all.len(), 3);
        fs::remove_dir_all(&dir).unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn never_follows_links() {
        let dir = tree("links");
        let outside = std::env::temp_dir().join(format!("ark-measure-outside-{}", std::process::id()));
        fs::create_dir_all(&outside).unwrap();
        fs::write(outside.join("big"), [0u8; 1000]).unwrap();
        std::os::unix::fs::symlink(&outside, dir.join("link")).unwrap();
        std::os::unix::fs::symlink(outside.join("big"), dir.join("filelink")).unwrap();
        let st = measure(dir.to_str().unwrap(), MAX_DURATION, MAX_ENTRIES);
        assert_eq!((st.bytes, st.symlinks), (135, 2));
        fs::remove_dir_all(&dir).unwrap();
        fs::remove_dir_all(&outside).unwrap();
    }

    #[test]
    fn unc_paths_are_network() {
        assert!(is_network_path(Path::new("\\\\server\\share\\x")));
        assert!(is_network_path(Path::new("//server/share/x")));
    }
}
