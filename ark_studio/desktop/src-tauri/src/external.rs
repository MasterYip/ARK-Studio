//! Links that leave the ARK window.
//!
//! target=_blank links, window.open() and (through shell.js) plain links to
//! other sites reach the main window's `on_new_window`; the URL goes to the
//! system: http(s) to the default browser, mailto: to the mail app. Every
//! other scheme (javascript:, data:, file:, custom app schemes) is dropped
//! here; local files open only through `open_local_path` (trusted folders,
//! or after asking).

use tauri::AppHandle;
use tauri_plugin_opener::OpenerExt;
use url::Url;

/// The URL to hand to the system, or None to drop the link.
pub fn system_target(url: &Url) -> Option<String> {
    match url.scheme() {
        "http" | "https" if url.host_str().is_some_and(|h| !h.is_empty()) => Some(url.to_string()),
        "mailto" if !url.path().trim().is_empty() => Some(url.to_string()),
        _ => None,
    }
}

/// Opens `target` (already checked by `system_target`) with the system.
/// Debug builds with `ARK_DESKTOP_OPEN_LOG` set append it to that file
/// instead (link tests run without launching a browser).
pub fn open(app: &AppHandle, target: String) {
    #[cfg(debug_assertions)]
    if let Some(log) = std::env::var_os("ARK_DESKTOP_OPEN_LOG").filter(|v| !v.is_empty()) {
        use std::io::Write;
        if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(log) {
            let _ = writeln!(f, "{target}");
        }
        return;
    }
    if let Err(e) = app.opener().open_url(target, None::<&str>) {
        eprintln!("ark-studio: open link: {e}");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn target(raw: &str) -> Option<String> {
        system_target(&Url::parse(raw).unwrap())
    }

    #[test]
    fn web_and_mail_links_go_to_the_system() {
        assert_eq!(target("https://github.com/MasterYip/ARK-Studio").as_deref(), Some("https://github.com/MasterYip/ARK-Studio"));
        assert_eq!(target("http://192.168.1.20:8891/#/home").as_deref(), Some("http://192.168.1.20:8891/#/home"));
        assert_eq!(target("HTTPS://Example.COM/a b").as_deref(), Some("https://example.com/a%20b"));
        assert_eq!(target("mailto:someone@example.com?subject=ARK").as_deref(), Some("mailto:someone@example.com?subject=ARK"));
    }

    #[test]
    fn other_schemes_are_dropped() {
        for raw in [
            "javascript:alert(1)",
            "data:text/html,<script>alert(1)</script>",
            "file:///C:/Windows/System32/calc.exe",
            "vscode://file/E:/x",
            "intent://scan/#Intent;scheme=zxing;end",
            "ms-settings:privacy",
            "about:blank",
            "blob:https://ark.example.com/1234",
            "ftp://example.com/file",
            "mailto:",
        ] {
            assert_eq!(target(raw), None, "{raw}");
        }
    }
}
