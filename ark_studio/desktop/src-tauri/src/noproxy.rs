//! The app's own local server never goes through a proxy.
//!
//! With a proxy on (Clash and similar tools set `http_proxy` / `all_proxy`,
//! or the desktop's proxy settings), WebKitGTK sent the main window's
//! requests to its own sidecar on 127.0.0.1 through the proxy, and the server
//! refused them as non-local (the phone pairing page or "This ARK server
//! belongs to the ARK Studio desktop app", 2026-11-27). GLib's proxy
//! resolver (libproxy), reqwest and curl honour `no_proxy`, so the loopback
//! names are added to it before any window or the sidecar starts. Remote
//! servers keep using the proxy.

/// Loopback names that must bypass any proxy.
pub const LOOPBACK: &[&str] = &["localhost", "127.0.0.1", "::1"];

/// `existing` (a `no_proxy` value) with the loopback names added; None when
/// it already lists all of them or bypasses everything (`*`).
pub fn merged(existing: Option<&str>) -> Option<String> {
    let current = existing.unwrap_or("").trim();
    let items: Vec<&str> = current.split(',').map(str::trim).filter(|s| !s.is_empty()).collect();
    if items.iter().any(|s| *s == "*") {
        return None;
    }
    let missing: Vec<&str> = LOOPBACK
        .iter()
        .copied()
        .filter(|name| !items.iter().any(|s| s.eq_ignore_ascii_case(name) || (*name == "::1" && *s == "[::1]")))
        .collect();
    if missing.is_empty() {
        return None;
    }
    Some(items.into_iter().chain(missing).collect::<Vec<_>>().join(","))
}

/// Adds the loopback names to `no_proxy` and `NO_PROXY` of this process
/// (inherited by the WebView's network process and the sidecar). Call first
/// thing in `run()`, before any thread or window exists.
pub fn apply() {
    for key in ["no_proxy", "NO_PROXY"] {
        let current = std::env::var(key).ok();
        if let Some(value) = merged(current.as_deref()) {
            std::env::set_var(key, value);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn adds_loopback_names_to_an_empty_or_partial_list() {
        assert_eq!(merged(None).as_deref(), Some("localhost,127.0.0.1,::1"));
        assert_eq!(merged(Some("")).as_deref(), Some("localhost,127.0.0.1,::1"));
        // Keeps what the user set (proxy tools write their own lists).
        assert_eq!(
            merged(Some(" .corp.example.com, LOCALHOST ")).as_deref(),
            Some(".corp.example.com,LOCALHOST,127.0.0.1,::1")
        );
        assert_eq!(merged(Some("192.168.0.0/16,[::1]")).as_deref(), Some("192.168.0.0/16,[::1],localhost,127.0.0.1"));
    }

    #[test]
    fn leaves_complete_or_bypass_all_lists_alone() {
        assert_eq!(merged(Some("localhost,127.0.0.1,::1")), None);
        assert_eq!(merged(Some("127.0.0.1, localhost, ::1, example.com")), None);
        assert_eq!(merged(Some("*")), None);
    }

    #[test]
    fn apply_sets_both_spellings() {
        // Remote servers are not in the list: they keep the proxy.
        apply();
        for key in ["no_proxy", "NO_PROXY"] {
            let v = std::env::var(key).unwrap();
            for name in LOOPBACK {
                assert!(v.split(',').any(|s| s.trim().eq_ignore_ascii_case(name)), "{key}={v}");
            }
            assert!(!v.contains("example.org"), "{key}={v}");
        }
        assert_eq!(merged(std::env::var("no_proxy").ok().as_deref()), None, "idempotent");
    }
}
