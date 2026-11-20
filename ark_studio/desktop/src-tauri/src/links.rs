//! Desktop links: one revocable device token per saved server.
//!
//! "Link this desktop" on a server's page (signed in there) makes a one-time
//! code (`POST /api/desktop/link`); the page hands the code to the app, which
//! claims it from Rust (`POST /api/mobile/pair/claim`) and keeps the device
//! token here. The token only lists and creates projects on that server
//! (`/api/desktop/projects`); the server's device list revokes it. No
//! passwords and no browser cookies are stored. Tokens never reach a page:
//! the app calls the server and hands the local ARK page the results.
//!
//! File: `<data>/private/server-links.json`, written atomically with mode
//! 0600 inside the 0700 private folder on Unix (on Windows the per-user data
//! folder's permissions apply).

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::paths;
use crate::settings::normalize_remote;

#[derive(Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Link {
    pub token: String,
    /// The server account the link acts as.
    pub user: String,
    /// The pairing id (the row in the server's device list).
    pub pairing_id: String,
    /// Unix seconds.
    pub linked_at: u64,
}

impl std::fmt::Debug for Link {
    // Never print the token.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Link")
            .field("user", &self.user)
            .field("pairing_id", &self.pairing_id)
            .field("linked_at", &self.linked_at)
            .finish_non_exhaustive()
    }
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Links {
    /// Normalized server address -> link.
    pub servers: BTreeMap<String, Link>,
}

pub fn links_file() -> PathBuf {
    paths::private_dir().join("server-links.json")
}

fn key(server: &str) -> Option<String> {
    normalize_remote(server).ok().map(|u| u.to_string())
}

impl Links {
    pub fn load() -> Links {
        Links::load_from(&links_file())
    }

    pub fn load_from(path: &Path) -> Links {
        std::fs::read(path).ok().and_then(|raw| serde_json::from_slice(&raw).ok()).unwrap_or_default()
    }

    pub fn save(&self) -> Result<(), String> {
        paths::ensure_private_dir(&paths::private_dir()).map_err(|e| e.to_string())?;
        self.save_to(&links_file())
    }

    /// Atomic write (temp file + rename), private on Unix.
    pub fn save_to(&self, path: &Path) -> Result<(), String> {
        let tmp = path.with_extension("json.tmp");
        let body = serde_json::to_vec_pretty(self).map_err(|e| e.to_string())?;
        {
            use std::io::Write;
            let mut opts = std::fs::OpenOptions::new();
            opts.create(true).write(true).truncate(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                opts.mode(0o600);
            }
            let mut f = opts.open(&tmp).map_err(|e| e.to_string())?;
            f.write_all(&body).map_err(|e| e.to_string())?;
            f.sync_all().map_err(|e| e.to_string())?;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600));
        }
        std::fs::rename(&tmp, path).map_err(|e| e.to_string())
    }

    pub fn get(&self, server: &str) -> Option<&Link> {
        self.servers.get(&key(server)?)
    }

    pub fn set(&mut self, server: &str, link: Link) -> Result<(), String> {
        let k = key(server).ok_or("Not a server address.")?;
        self.servers.insert(k, link);
        Ok(())
    }

    pub fn remove(&mut self, server: &str) -> Option<Link> {
        self.servers.remove(&key(server)?)
    }

    /// Drops the links of servers that are no longer saved.
    pub fn retain_saved(&mut self, history: &[String]) -> Vec<(String, Link)> {
        let saved: Vec<String> = history.iter().filter_map(|h| key(h)).collect();
        let gone: Vec<String> = self.servers.keys().filter(|k| !saved.contains(k)).cloned().collect();
        gone.into_iter().filter_map(|k| self.servers.remove(&k).map(|l| (k, l))).collect()
    }
}

/// The link from a `POST /api/mobile/pair/claim` reply for a desktop code.
pub fn parse_claim(reply: &Value, now: u64) -> Result<Link, String> {
    let token = reply.get("mobile_token").and_then(Value::as_str).unwrap_or_default().trim();
    if !token.starts_with("arkm_") || token.len() < 20 || token.len() > 200 || token.chars().any(char::is_whitespace) {
        return Err("The server did not return a device token.".into());
    }
    if reply.get("token_type").and_then(Value::as_str).is_some_and(|t| t != "X-ARK-Mobile-Token") {
        return Err("The server returned an unexpected token type.".into());
    }
    Ok(Link {
        token: token.to_string(),
        user: reply.get("user").and_then(Value::as_str).unwrap_or_default().to_string(),
        pairing_id: reply.get("pairing_id").and_then(Value::as_str).unwrap_or_default().to_string(),
        linked_at: now,
    })
}

/// A link code from a server page: `arkp_` and URL-safe characters only.
pub fn valid_code(code: &str) -> bool {
    let c = code.trim();
    c.starts_with("arkp_")
        && c.len() <= 200
        && c.len() > 10
        && c.chars().all(|ch| ch.is_ascii_alphanumeric() || ch == '_' || ch == '-')
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn link(token: &str) -> Link {
        Link { token: token.into(), user: "alice".into(), pairing_id: "pair_1".into(), linked_at: 7 }
    }

    #[test]
    fn links_are_keyed_by_normalized_server() {
        let mut l = Links::default();
        l.set("ark.example.com", link("arkm_aaaaaaaaaaaaaaaaaaaa")).unwrap();
        assert_eq!(l.get("https://ark.example.com/").unwrap().user, "alice");
        assert!(l.get("http://ark.example.com/").is_none());
        assert!(l.set("ftp://x", link("t")).is_err());
        assert!(l.remove("https://ark.example.com").is_some());
        assert!(l.servers.is_empty());
    }

    #[test]
    fn forgotten_servers_lose_their_links() {
        let mut l = Links::default();
        l.set("https://a.example/", link("arkm_aaaaaaaaaaaaaaaaaaaa")).unwrap();
        l.set("https://b.example/", link("arkm_bbbbbbbbbbbbbbbbbbbb")).unwrap();
        let gone = l.retain_saved(&["https://a.example/".to_string()]);
        assert_eq!(gone.len(), 1);
        assert_eq!(gone[0].0, "https://b.example/");
        assert!(l.get("https://a.example/").is_some() && l.get("https://b.example/").is_none());
    }

    #[test]
    fn round_trip_is_private_and_atomic() {
        let dir = std::env::temp_dir().join(format!("ark-links-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("server-links.json");
        let mut l = Links::default();
        l.set("https://a.example/", link("arkm_aaaaaaaaaaaaaaaaaaaa")).unwrap();
        l.save_to(&file).unwrap();
        assert!(!file.with_extension("json.tmp").exists());
        assert_eq!(Links::load_from(&file), l);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&file).unwrap().permissions().mode() & 0o777, 0o600);
        }
        // a damaged file reads as no links
        std::fs::write(&file, b"{not json").unwrap();
        assert_eq!(Links::load_from(&file), Links::default());
        assert_eq!(Links::load_from(&dir.join("missing.json")), Links::default());
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn claims_are_validated() {
        let ok = json!({"mobile_token": "arkm_0123456789abcdefghij", "token_type": "X-ARK-Mobile-Token",
                        "user": "alice", "pairing_id": "pair_9", "permission": "read_only"});
        let l = parse_claim(&ok, 42).unwrap();
        assert_eq!((l.user.as_str(), l.pairing_id.as_str(), l.linked_at), ("alice", "pair_9", 42));
        assert!(parse_claim(&json!({"mobile_token": "nope"}), 0).is_err());
        assert!(parse_claim(&json!({"ok": false}), 0).is_err());
        assert!(parse_claim(&json!({"mobile_token": "arkm_0123456789abcdefghij", "token_type": "Bearer"}), 0).is_err());
        assert!(parse_claim(&json!({"mobile_token": "arkm_0123456789 abcdefghij"}), 0).is_err());
    }

    #[test]
    fn debug_output_hides_the_token() {
        let text = format!("{:?}", link("arkm_secretsecretsecret"));
        assert!(!text.contains("secret") && text.contains("alice"));
    }

    #[test]
    fn link_codes_are_checked_before_use() {
        assert!(valid_code("arkp_AbC-123_xyzxyz"));
        assert!(!valid_code("arkm_AbC123xyzxyz"));
        assert!(!valid_code("arkp_a b"));
        assert!(!valid_code("arkp_\"x\"aaaaaaa"));
        assert!(!valid_code(""));
    }
}
