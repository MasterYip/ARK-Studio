//! Where the desktop app keeps projects, keys, logs and settings.
//!
//! Two layouts:
//!
//! * **Next to the app** (Windows per-user installer and portable zip, when
//!   the app folder is writable): projects in `<app dir>\projects`, everything
//!   else in `<app dir>\data` (`private\`, `logs\`, `desktop.json`,
//!   `sidecar.json`). The uninstaller leaves both in place.
//! * **Per-user data dir** (MSI in Program Files, macOS .app, Linux .deb and
//!   AppImage, an app folder that is not writable, and every existing
//!   installation that already has data there):
//!
//!   | OS      | data dir                                    |
//!   |---------|---------------------------------------------|
//!   | Windows | `%LOCALAPPDATA%\ARK`                        |
//!   | macOS   | `~/Library/Application Support/ARK`         |
//!   | Linux   | `$XDG_DATA_HOME/ark` (`~/.local/share/ark`) |
//!
//!   with `projects/`, `private/`, `logs/`, `desktop.json`, `sidecar.json`.
//!
//! `ARK_DESKTOP_DATA_DIR` overrides both (tests): data there, projects in
//! `<dir>/projects`. The layout is decided once per process ([`layout`]);
//! existing data is never moved or silently abandoned.

use std::path::{Path, PathBuf};
use std::sync::OnceLock;

use serde::Serialize;

pub fn home() -> PathBuf {
    let var = if cfg!(windows) { "USERPROFILE" } else { "HOME" };
    std::env::var_os(var)
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

#[cfg(windows)]
fn platform_data_dir() -> PathBuf {
    std::env::var_os("LOCALAPPDATA")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| home().join("AppData").join("Local"))
        .join("ARK")
}

#[cfg(target_os = "macos")]
fn platform_data_dir() -> PathBuf {
    home().join("Library").join("Application Support").join("ARK")
}

#[cfg(all(unix, not(target_os = "macos")))]
fn platform_data_dir() -> PathBuf {
    std::env::var_os("XDG_DATA_HOME")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| home().join(".local").join("share"))
        .join("ark")
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Kind {
    /// Next to the app: `<app dir>\projects` and `<app dir>\data`.
    Install,
    /// The per-user data dir.
    User,
    /// `ARK_DESKTOP_DATA_DIR`.
    Override,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Layout {
    pub kind: Kind,
    /// private/, logs/, desktop.json, sidecar.json
    pub data: PathBuf,
    /// default project folder
    pub projects: PathBuf,
    /// Why the app folder is not used although it normally would be.
    pub note: Option<String>,
}

/// Facts the layout decision depends on (gathered by [`detect`]).
#[derive(Clone, Debug, Default)]
pub struct Inputs {
    pub override_dir: Option<PathBuf>,
    pub app_dir: Option<PathBuf>,
    pub user_dir: PathBuf,
    /// This kind of install keeps data next to the app (Windows, not under
    /// Program Files). False for a per-machine MSI, macOS and Linux packages.
    pub app_dir_eligible: bool,
    pub app_dir_has_data: bool,
    pub app_dir_writable: bool,
    pub user_dir_has_data: bool,
}

fn user_layout(i: &Inputs, note: Option<String>) -> Layout {
    Layout { kind: Kind::User, data: i.user_dir.clone(), projects: i.user_dir.join("projects"), note }
}

fn install_layout(app: &Path) -> Layout {
    Layout { kind: Kind::Install, data: app.join("data"), projects: app.join("projects"), note: None }
}

/// The layout rules (pure; see the module docs).
pub fn decide(i: &Inputs) -> Layout {
    if let Some(dir) = &i.override_dir {
        return Layout { kind: Kind::Override, data: dir.clone(), projects: dir.join("projects"), note: None };
    }
    let Some(app) = &i.app_dir else {
        return user_layout(i, None);
    };
    // Data already next to the app: keep using it.
    if i.app_dir_has_data && i.app_dir_writable {
        return install_layout(app);
    }
    // Existing users keep their per-user data.
    if i.user_dir_has_data {
        return user_layout(i, None);
    }
    if !i.app_dir_eligible {
        return user_layout(i, None);
    }
    if i.app_dir_writable {
        return install_layout(app);
    }
    user_layout(
        i,
        Some(format!(
            "The app folder {} is not writable, so ARK keeps its data in {}.",
            app.display(),
            i.user_dir.display()
        )),
    )
}

fn has_data(dir: &Path) -> bool {
    dir.join("desktop.json").is_file() || dir.join("projects").is_dir()
}

/// Program Files (per-machine MSI installs) never holds user data.
fn under_program_files(dir: &Path) -> bool {
    let d = dir.to_string_lossy().to_lowercase();
    ["ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"].iter().any(|v| {
        std::env::var(v).ok().filter(|p| !p.is_empty()).is_some_and(|p| d.starts_with(&p.to_lowercase()))
    })
}

/// Writable for real: create and remove a probe file.
pub fn writable(dir: &Path) -> bool {
    let probe = dir.join(format!(".ark-write-test-{}", std::process::id()));
    match std::fs::OpenOptions::new().write(true).create_new(true).open(&probe) {
        Ok(file) => {
            drop(file);
            let _ = std::fs::remove_file(&probe);
            true
        }
        Err(_) => false,
    }
}

pub fn detect() -> Inputs {
    let override_dir = std::env::var_os("ARK_DESKTOP_DATA_DIR").filter(|v| !v.is_empty()).map(PathBuf::from);
    let app_dir = std::env::current_exe().ok().and_then(|e| e.parent().map(Path::to_path_buf));
    let user_dir = platform_data_dir();
    let eligible = cfg!(windows) && app_dir.as_deref().is_some_and(|d| !under_program_files(d));
    Inputs {
        app_dir_has_data: app_dir.as_deref().is_some_and(|d| has_data(&d.join("data")) || d.join("projects").is_dir()),
        app_dir_writable: app_dir.as_deref().is_some_and(writable),
        user_dir_has_data: has_data(&user_dir),
        app_dir_eligible: eligible,
        override_dir,
        app_dir,
        user_dir,
    }
}

static LAYOUT: OnceLock<Layout> = OnceLock::new();

pub fn layout() -> &'static Layout {
    LAYOUT.get_or_init(|| decide(&detect()))
}

pub fn data_dir() -> PathBuf {
    layout().data.clone()
}

pub fn default_root() -> PathBuf {
    layout().projects.clone()
}

pub fn private_dir() -> PathBuf {
    data_dir().join("private")
}

pub fn logs_dir() -> PathBuf {
    data_dir().join("logs")
}

pub fn server_log() -> PathBuf {
    logs_dir().join("ark-server.log")
}

pub fn settings_file() -> PathBuf {
    data_dir().join("desktop.json")
}

pub fn sidecar_file() -> PathBuf {
    data_dir().join("sidecar.json")
}

/// Creates `dir` (and parents); on Unix the leaf is made private (0700).
pub fn ensure_private_dir(dir: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}

/// `shutil.which` for a bare program name.
pub fn find_in_path(name: &str) -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    let exts: Vec<String> = if cfg!(windows) {
        std::env::var("PATHEXT")
            .unwrap_or_else(|_| ".EXE;.CMD;.BAT;.COM".into())
            .split(';')
            .filter(|e| !e.is_empty())
            .map(|e| e.to_ascii_lowercase())
            .collect()
    } else {
        vec![String::new()]
    };
    for dir in std::env::split_paths(&path) {
        for ext in &exts {
            let candidate = dir.join(format!("{name}{ext}"));
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn inputs() -> Inputs {
        Inputs {
            app_dir: Some(PathBuf::from("C:/Users/u/AppData/Local/ARK Studio")),
            user_dir: PathBuf::from("C:/Users/u/AppData/Local/ARK"),
            app_dir_eligible: true,
            app_dir_writable: true,
            ..Default::default()
        }
    }

    #[test]
    fn new_windows_install_keeps_data_next_to_the_app() {
        let l = decide(&inputs());
        assert_eq!(l.kind, Kind::Install);
        assert_eq!(l.projects, PathBuf::from("C:/Users/u/AppData/Local/ARK Studio/projects"));
        assert_eq!(l.data, PathBuf::from("C:/Users/u/AppData/Local/ARK Studio/data"));
        assert!(l.note.is_none());
    }

    #[test]
    fn existing_per_user_data_wins_over_the_new_default() {
        let l = decide(&Inputs { user_dir_has_data: true, ..inputs() });
        assert_eq!(l.kind, Kind::User);
        assert_eq!(l.projects, PathBuf::from("C:/Users/u/AppData/Local/ARK/projects"));
        assert!(l.note.is_none());
    }

    #[test]
    fn data_already_next_to_the_app_stays_there() {
        let l = decide(&Inputs { app_dir_has_data: true, user_dir_has_data: true, ..inputs() });
        assert_eq!(l.kind, Kind::Install);
    }

    #[test]
    fn unwritable_app_folder_falls_back_and_says_so() {
        let l = decide(&Inputs { app_dir_writable: false, ..inputs() });
        assert_eq!(l.kind, Kind::User);
        assert!(l.note.as_deref().unwrap().contains("not writable"));
        let l = decide(&Inputs { app_dir_writable: false, app_dir_has_data: true, ..inputs() });
        assert_eq!(l.kind, Kind::User);
    }

    #[test]
    fn read_only_packages_use_the_user_dir_without_a_warning() {
        // MSI under Program Files, macOS .app, Linux .deb / AppImage
        let l = decide(&Inputs { app_dir_eligible: false, app_dir_writable: false, ..inputs() });
        assert_eq!((l.kind, l.note.is_none()), (Kind::User, true));
        // even when Program Files happens to be writable (elevated run)
        let l = decide(&Inputs { app_dir_eligible: false, ..inputs() });
        assert_eq!(l.kind, Kind::User);
    }

    #[test]
    fn override_wins() {
        let l = decide(&Inputs { override_dir: Some(PathBuf::from("/tmp/x")), app_dir_has_data: true, ..inputs() });
        assert_eq!((l.kind, l.projects), (Kind::Override, PathBuf::from("/tmp/x/projects")));
    }

    #[test]
    fn writable_probe_leaves_nothing_behind() {
        let dir = std::env::temp_dir().join(format!("ark-paths-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        assert!(writable(&dir));
        assert_eq!(std::fs::read_dir(&dir).unwrap().count(), 0);
        assert!(!writable(&dir.join("missing")));
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
