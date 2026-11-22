//! Remembered connection choices (`desktop.json` in the data dir).
//! No passwords: sign-in to remote servers stays inside ARK.

use serde::{Deserialize, Serialize};

use crate::paths;

pub const DEFAULT_PORT: u16 = 18890;
const HISTORY_MAX: usize = 12;
/// The automatic update check runs at start and, while the app stays open,
/// again after this long (seconds).
pub const UPDATE_CHECK_INTERVAL: u64 = 12 * 60 * 60;

/// Version of the `desktop.json` layout. 2: no connection chooser; the app
/// always starts the local server and the remote servers are a saved list.
pub const SETTINGS_VERSION: u32 = 2;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Settings {
    pub settings_version: u32,
    /// Local project folder; empty means `paths::default_root()`.
    pub local_root: String,
    /// Preferred local port; 0 means `DEFAULT_PORT`.
    pub local_port: u16,
    /// The port the local server got the last time the preferred one was
    /// taken; tried next so the local page keeps its origin (and with it the
    /// page's browser storage: theme, language, layout). 0: none.
    pub fallback_port: u16,
    /// Saved remote servers (normalized URLs), most recently used first.
    pub remote_history: Vec<String>,
    /// Folders a remote ARK page may open on this computer without asking.
    pub local_open_roots: Vec<String>,
    /// Closing a window hides it to the tray instead of quitting (default).
    pub close_to_tray: bool,
    /// The user set `close_to_tray` in Settings. Files written before the
    /// default became "on" stored `false` without anyone choosing it, so an
    /// unchosen value is read as the default.
    pub close_to_tray_chosen: bool,
    /// Check for updates at start and every 12 hours while running (default on).
    pub auto_update_check: bool,
    /// When the last update check succeeded (Unix seconds; 0: never).
    pub last_update_check: u64,
    /// The app version that last ran: a different one means the app was
    /// updated ("ARK Studio was updated to ..." once). Empty: not recorded yet.
    pub last_seen_version: String,
    /// The newer version the system notification was shown for (once per version).
    pub notified_update: String,
    /// The newer version the person chose "Later" for in the app window's notice.
    pub dismissed_update: String,
    /// Not stored: there was no `desktop.json` (a fresh install).
    #[serde(skip)]
    pub fresh: bool,
    /// Version 1 (connection chooser): "local" / "remote" / "". Read for the
    /// migration only, never written.
    #[serde(skip_serializing)]
    pub mode: String,
    /// Version 1: the last remote address. Read for the migration only.
    #[serde(skip_serializing)]
    pub remote_url: String,
    /// Version 1: "open this choice automatically". Read for the migration only.
    #[serde(skip_serializing)]
    pub auto_connect: bool,
}

impl Default for Settings {
    fn default() -> Settings {
        Settings {
            // 0 when the field is missing from a file: a version 1 file.
            settings_version: 0,
            local_root: String::new(),
            local_port: 0,
            fallback_port: 0,
            remote_history: Vec::new(),
            local_open_roots: Vec::new(),
            close_to_tray: true,
            close_to_tray_chosen: false,
            auto_update_check: true,
            last_update_check: 0,
            last_seen_version: String::new(),
            notified_update: String::new(),
            dismissed_update: String::new(),
            fresh: false,
            mode: String::new(),
            remote_url: String::new(),
            auto_connect: false,
        }
    }
}

impl Settings {
    pub fn load() -> Settings {
        std::fs::read(paths::settings_file())
            .ok()
            .and_then(|raw| Settings::parse(&raw))
            .unwrap_or_else(|| {
                let mut s = Settings::default();
                s.migrate();
                s.fresh = true;
                s
            })
    }

    /// Parses `desktop.json`; an unchosen close-to-tray falls back to on,
    /// and an older file is migrated (`migrate`).
    pub fn parse(raw: &[u8]) -> Option<Settings> {
        let mut s: Settings = serde_json::from_slice(raw).ok()?;
        if !s.close_to_tray_chosen {
            s.close_to_tray = true;
        }
        s.migrate();
        Some(s)
    }

    /// Version 1 to 2: the chooser's last remote address joins the saved
    /// servers (first, as the most recent); "mode" and "open automatically"
    /// are dropped (the app always opens the local Home). Saved addresses
    /// are normalized and de-duplicated; unusable ones are dropped.
    pub fn migrate(&mut self) {
        if self.settings_version < 2 {
            let last = self.remote_url.trim().to_string();
            if !last.is_empty() {
                self.remote_history.retain(|u| u.trim() != last);
                self.remote_history.insert(0, last);
            }
        }
        let mut seen = Vec::new();
        for raw in std::mem::take(&mut self.remote_history) {
            if let Ok(u) = normalize_remote(&raw) {
                let u = u.to_string();
                if !seen.contains(&u) {
                    seen.push(u);
                }
            }
        }
        seen.truncate(HISTORY_MAX);
        self.remote_history = seen;
        self.mode.clear();
        self.remote_url.clear();
        self.auto_connect = false;
        self.settings_version = SETTINGS_VERSION;
    }

    pub fn save(&self) -> Result<(), String> {
        let file = paths::settings_file();
        paths::ensure_private_dir(&paths::data_dir()).map_err(|e| e.to_string())?;
        let tmp = file.with_extension("json.tmp");
        let body = serde_json::to_vec_pretty(self).map_err(|e| e.to_string())?;
        std::fs::write(&tmp, body).map_err(|e| e.to_string())?;
        std::fs::rename(&tmp, &file).map_err(|e| e.to_string())
    }

    pub fn root(&self) -> std::path::PathBuf {
        if self.local_root.trim().is_empty() {
            paths::default_root()
        } else {
            std::path::PathBuf::from(self.local_root.trim())
        }
    }

    pub fn port(&self) -> u16 {
        if self.local_port == 0 {
            DEFAULT_PORT
        } else {
            self.local_port
        }
    }

    /// The automatic update check is on and the last one is a day old (or
    /// lies in the future: the clock was changed).
    pub fn update_check_due(&self, now: u64) -> bool {
        self.auto_update_check
            && (now < self.last_update_check || now - self.last_update_check >= UPDATE_CHECK_INTERVAL)
    }

    /// Records the running version. Returns the previous version when the
    /// app was updated since it last ran ("" when an older app, which did
    /// not record it, ran before); None on a fresh install or the same version.
    pub fn note_version(&mut self, current: &str) -> Option<String> {
        let previous = std::mem::replace(&mut self.last_seen_version, current.to_string());
        if previous == current || (previous.is_empty() && self.fresh) {
            return None;
        }
        Some(previous)
    }

    /// Whether to show the system notification for `version` (once per version).
    pub fn take_notification(&mut self, version: &str) -> bool {
        if version.is_empty() || self.notified_update == version {
            return false;
        }
        self.notified_update = version.to_string();
        true
    }

    /// Saves (or moves to the front) a normalized server address.
    pub fn remember_remote(&mut self, url: &str) {
        self.remote_history.retain(|u| u != url);
        self.remote_history.insert(0, url.to_string());
        self.remote_history.truncate(HISTORY_MAX);
    }
}

/// Normalizes a typed server address: adds https:// when no scheme is
/// given, accepts only http(s), drops fragments and trailing slashes.
pub fn normalize_remote(raw: &str) -> Result<url::Url, String> {
    let text = raw.trim();
    if text.is_empty() {
        return Err("Enter the address of an ARK server.".into());
    }
    let with_scheme = if text.contains("://") {
        text.to_string()
    } else {
        format!("https://{text}")
    };
    let mut url = url::Url::parse(&with_scheme).map_err(|e| format!("Not a valid address: {e}"))?;
    if url.scheme() != "http" && url.scheme() != "https" {
        return Err("Only http:// and https:// addresses are supported.".into());
    }
    if url.host_str().unwrap_or("").is_empty() {
        return Err("The address has no host name.".into());
    }
    url.set_fragment(None);
    if !url.path().ends_with('/') {
        let path = format!("{}/", url.path());
        url.set_path(&path);
    }
    Ok(url)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fallback_port_round_trips_and_defaults_to_none() {
        assert_eq!(Settings::default().fallback_port, 0);
        assert_eq!(Settings::parse(br#"{"settingsVersion":2}"#).unwrap().fallback_port, 0);
        let s = Settings::parse(br#"{"settingsVersion":2,"fallbackPort":51384}"#).unwrap();
        assert_eq!(s.fallback_port, 51384);
        assert!(String::from_utf8(serde_json::to_vec(&s).unwrap()).unwrap().contains("\"fallbackPort\":51384"));
    }

    #[test]
    fn normalizes_remote_addresses() {
        assert_eq!(normalize_remote("ark.example.com").unwrap().as_str(), "https://ark.example.com/");
        assert_eq!(normalize_remote(" http://127.0.0.1:8890 ").unwrap().as_str(), "http://127.0.0.1:8890/");
        assert_eq!(normalize_remote("https://h/ark#x").unwrap().as_str(), "https://h/ark/");
        assert!(normalize_remote("file:///etc/passwd").is_err());
        assert!(normalize_remote("javascript:alert(1)").is_err());
        assert!(normalize_remote("").is_err());
    }

    #[test]
    fn close_to_tray_defaults_on_unless_chosen() {
        assert!(Settings::default().close_to_tray);
        // written by an older version: false was the default, not a choice
        assert!(Settings::parse(br#"{"mode":"local","closeToTray":false}"#).unwrap().close_to_tray);
        assert!(Settings::parse(br#"{}"#).unwrap().close_to_tray);
        // an explicit choice in the chooser is kept
        assert!(!Settings::parse(br#"{"closeToTray":false,"closeToTrayChosen":true}"#).unwrap().close_to_tray);
        assert!(Settings::parse(br#"{"closeToTray":true,"closeToTrayChosen":true}"#).unwrap().close_to_tray);
    }

    #[test]
    fn history_is_most_recent_first_and_bounded() {
        let mut s = Settings::default();
        for i in 0..20 {
            s.remember_remote(&format!("https://h{i}/"));
        }
        s.remember_remote("https://h5/");
        assert_eq!(s.remote_history[0], "https://h5/");
        assert_eq!(s.remote_history.len(), HISTORY_MAX);
        assert_eq!(s.remote_history.iter().filter(|u| *u == "https://h5/").count(), 1);
    }

    #[test]
    fn version_1_files_migrate_to_the_saved_server_list() {
        let raw = br#"{"mode":"remote","remoteUrl":"https://b.example/","remoteHistory":["https://a.example/","https://b.example/"],
            "autoConnect":true,"localRoot":"D:/ark","localPort":18999,"localOpenRoots":["D:/data"],"closeToTray":false,"closeToTrayChosen":true}"#;
        let s = Settings::parse(raw).unwrap();
        assert_eq!(s.settings_version, SETTINGS_VERSION);
        assert_eq!(s.remote_history, vec!["https://b.example/", "https://a.example/"]);
        assert_eq!(s.local_root, "D:/ark");
        assert_eq!(s.local_port, 18999);
        assert_eq!(s.local_open_roots, vec!["D:/data"]);
        assert!(!s.close_to_tray);
        assert!(!s.auto_connect && s.mode.is_empty() && s.remote_url.is_empty());
        // the dropped chooser fields are not written back
        let out = serde_json::to_string(&s).unwrap();
        assert!(!out.contains("autoConnect") && !out.contains("\"mode\"") && !out.contains("remoteUrl"));
        assert!(out.contains("\"settingsVersion\":2"));
    }

    #[test]
    fn migration_normalizes_and_dedupes_saved_servers() {
        let raw = br#"{"remoteUrl":"ark.example.com","remoteHistory":["https://ark.example.com/","javascript:x","http://h:1/a"]}"#;
        let s = Settings::parse(raw).unwrap();
        assert_eq!(s.remote_history, vec!["https://ark.example.com/", "http://h:1/a/"]);
        // a version 2 file keeps its order and ignores a stray remoteUrl
        let s = Settings::parse(br#"{"settingsVersion":2,"remoteUrl":"https://x/","remoteHistory":["https://y/"]}"#).unwrap();
        assert_eq!(s.remote_history, vec!["https://y/"]);
        // a fresh install has nothing to migrate
        assert!(Settings::parse(b"{}").unwrap().remote_history.is_empty());
    }

    #[test]
    fn an_updated_app_is_noticed_once() {
        // fresh install: nothing to announce, the version is recorded
        let mut s = Settings { fresh: true, ..Settings::default() };
        assert_eq!(s.note_version("2.1.2"), None);
        assert_eq!(s.last_seen_version, "2.1.2");
        assert_eq!(s.note_version("2.1.2"), None);
        // updated: the previous version, once
        assert_eq!(s.note_version("2.2.0").as_deref(), Some("2.1.2"));
        assert_eq!(s.note_version("2.2.0"), None);
        // a file written by an older app (no lastSeenVersion): updated from an unknown version
        let mut old = Settings::parse(br#"{"settingsVersion":2,"lastUpdateCheck":5}"#).unwrap();
        assert!(!old.fresh);
        assert_eq!(old.note_version("2.1.2").as_deref(), Some(""));
        let out = serde_json::to_string(&old).unwrap();
        assert!(out.contains("\"lastSeenVersion\":\"2.1.2\"") && !out.contains("fresh"));
    }

    #[test]
    fn the_update_notification_shows_once_per_version() {
        let mut s = Settings::default();
        assert!(s.take_notification("2.1.2"));
        assert!(!s.take_notification("2.1.2"));
        assert!(s.take_notification("2.2.0"));
        assert!(!s.take_notification(""));
        let s = Settings::parse(br#"{"notifiedUpdate":"2.2.0","dismissedUpdate":"2.2.0"}"#).unwrap();
        assert_eq!((s.notified_update.as_str(), s.dismissed_update.as_str()), ("2.2.0", "2.2.0"));
    }

    #[test]
    fn automatic_update_check_is_on_by_default_every_12_hours() {
        let day = UPDATE_CHECK_INTERVAL;
        let s = Settings::default();
        assert!(s.auto_update_check);
        // never checked: due on the first start
        assert!(s.update_check_due(20 * day));
        // older files have no such fields: on, never checked
        let s = Settings::parse(br#"{"settingsVersion":2}"#).unwrap();
        assert!(s.auto_update_check && s.last_update_check == 0);
        let s = Settings { last_update_check: 10 * day, ..Settings::default() };
        assert!(!s.update_check_due(10 * day + day - 1));
        assert!(s.update_check_due(11 * day));
        // a clock set back does not stop the checks for good
        assert!(s.update_check_due(day));
        // switched off: never
        let off = Settings::parse(br#"{"autoUpdateCheck":false,"lastUpdateCheck":5}"#).unwrap();
        assert!(!off.auto_update_check && off.last_update_check == 5);
        assert!(!off.update_check_due(100 * day));
        // written back under the same names
        let out = serde_json::to_string(&off).unwrap();
        assert!(out.contains("\"autoUpdateCheck\":false") && out.contains("\"lastUpdateCheck\":5"));
    }
}
