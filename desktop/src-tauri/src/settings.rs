//! Remembered connection choices (`desktop.json` in the data dir).
//! No passwords: sign-in to remote servers stays inside ARK.

use serde::{Deserialize, Serialize};

use crate::paths;

pub const DEFAULT_PORT: u16 = 18890;
const HISTORY_MAX: usize = 12;

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Settings {
    /// "local", "remote" or "" (never connected).
    pub mode: String,
    /// Local project folder; empty means `paths::default_root()`.
    pub local_root: String,
    /// Preferred local port; 0 means `DEFAULT_PORT`.
    pub local_port: u16,
    pub remote_url: String,
    pub remote_history: Vec<String>,
    /// Connect to the last choice at start instead of showing the chooser.
    pub auto_connect: bool,
    /// Folders a remote ARK page may open on this computer without asking.
    pub local_open_roots: Vec<String>,
    /// Closing a window hides it to the tray instead of quitting.
    pub close_to_tray: bool,
}

impl Settings {
    pub fn load() -> Settings {
        std::fs::read(paths::settings_file())
            .ok()
            .and_then(|raw| serde_json::from_slice(&raw).ok())
            .unwrap_or_default()
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

    pub fn remember_remote(&mut self, url: &str) {
        self.remote_url = url.to_string();
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
    fn normalizes_remote_addresses() {
        assert_eq!(normalize_remote("ark.example.com").unwrap().as_str(), "https://ark.example.com/");
        assert_eq!(normalize_remote(" http://127.0.0.1:8890 ").unwrap().as_str(), "http://127.0.0.1:8890/");
        assert_eq!(normalize_remote("https://h/ark#x").unwrap().as_str(), "https://h/ark/");
        assert!(normalize_remote("file:///etc/passwd").is_err());
        assert!(normalize_remote("javascript:alert(1)").is_err());
        assert!(normalize_remote("").is_err());
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
}
