//! The saved remote servers as the app shows them (Settings, tray, the
//! project switcher of the local ARK page), and where a page belongs.
//!
//! A server is its normalized address (`settings::normalize_remote`): an
//! http(s) URL whose path ends in `/` (a server may live under a prefix such
//! as `https://host/ark/`).

use serde::Serialize;
use url::Url;

use crate::settings::normalize_remote;

#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ServerEntry {
    /// Normalized address, the key of the server everywhere.
    pub url: String,
    /// scheme://host[:port]
    pub origin: String,
    /// Short name for lists and menus: host[:port][/prefix].
    pub label: String,
}

/// One entry for a saved address; None for an unusable one.
pub fn entry(raw: &str) -> Option<ServerEntry> {
    let url = normalize_remote(raw).ok()?;
    let origin = url.origin().ascii_serialization();
    let mut label = url.host_str().unwrap_or_default().to_string();
    if let Some(port) = url.port() {
        label = format!("{label}:{port}");
    }
    let path = url.path().trim_end_matches('/');
    if !path.is_empty() {
        label.push_str(path);
    }
    Some(ServerEntry { url: url.to_string(), origin, label })
}

/// The saved servers, in the saved order (most recent first).
pub fn list(history: &[String]) -> Vec<ServerEntry> {
    history.iter().filter_map(|u| entry(u)).collect()
}

/// The saved server `raw` names, if it is one (compared normalized).
pub fn find_saved(history: &[String], raw: &str) -> Option<ServerEntry> {
    let wanted = entry(raw)?;
    list(history).into_iter().find(|e| e.url == wanted.url)
}

/// The saved server a page belongs to: same origin and the page path under
/// the server's path (the longest such path wins).
pub fn saved_for_page(history: &[String], page: &Url) -> Option<ServerEntry> {
    let origin = page.origin().ascii_serialization();
    let path = page.path();
    list(history)
        .into_iter()
        .filter(|e| {
            e.origin == origin && {
                let base = Url::parse(&e.url).map(|u| u.path().to_string()).unwrap_or_default();
                path.starts_with(&base) || format!("{path}/") == base
            }
        })
        .max_by_key(|e| e.url.len())
}

/// What to open on a server.
#[derive(Clone, Debug, PartialEq)]
pub enum Target {
    Home,
    /// A project, by id (the server's web app opens it: `?ark_open_project=`).
    Project(String),
    /// The server's new-project dialog (`?ark_new_project=1`).
    NewProject,
    /// The server's "Link this desktop" prompt (`?ark_link_desktop=1`).
    LinkDesktop,
}

/// The URL that opens `target` on the server at `server` (normalized).
pub fn target_url(server: &str, target: &Target) -> Result<Url, String> {
    let mut url = normalize_remote(server)?;
    match target {
        Target::Home => {}
        Target::Project(id) => {
            let id = id.trim();
            if id.is_empty() || id.len() > 200 {
                return Err("Not a project id.".into());
            }
            url.query_pairs_mut().append_pair("ark_open_project", id);
        }
        Target::NewProject => {
            url.query_pairs_mut().append_pair("ark_new_project", "1");
        }
        Target::LinkDesktop => {
            url.query_pairs_mut().append_pair("ark_link_desktop", "1");
        }
    }
    Ok(url)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn hist(items: &[&str]) -> Vec<String> {
        items.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn entries_have_short_labels_and_origins() {
        let e = entry("https://ark.example.com").unwrap();
        assert_eq!(e.url, "https://ark.example.com/");
        assert_eq!(e.origin, "https://ark.example.com");
        assert_eq!(e.label, "ark.example.com");
        let e = entry("http://10.0.0.5:8890/team/").unwrap();
        assert_eq!(e.label, "10.0.0.5:8890/team");
        assert_eq!(e.origin, "http://10.0.0.5:8890");
        assert!(entry("ftp://x").is_none());
        assert_eq!(list(&hist(&["a.example", "", "b.example"])).len(), 2);
    }

    #[test]
    fn saved_server_lookup_is_normalized() {
        let h = hist(&["https://a.example/", "http://b.example:81/ark/"]);
        assert_eq!(find_saved(&h, "a.example").unwrap().url, "https://a.example/");
        assert_eq!(find_saved(&h, "http://b.example:81/ark").unwrap().url, "http://b.example:81/ark/");
        assert!(find_saved(&h, "https://c.example/").is_none());
        assert!(find_saved(&h, "http://a.example/").is_none(), "scheme matters");
    }

    #[test]
    fn pages_map_to_their_server() {
        let h = hist(&["https://h.example/", "https://h.example/team/", "http://127.0.0.1:9000/"]);
        let page = |s: &str| Url::parse(s).unwrap();
        assert_eq!(saved_for_page(&h, &page("https://h.example/team/?x=1")).unwrap().url, "https://h.example/team/");
        assert_eq!(saved_for_page(&h, &page("https://h.example/team")).unwrap().url, "https://h.example/team/");
        assert_eq!(saved_for_page(&h, &page("https://h.example/login")).unwrap().url, "https://h.example/");
        assert!(saved_for_page(&h, &page("https://evil.example/")).is_none());
        assert!(saved_for_page(&h, &page("http://127.0.0.1:9001/")).is_none(), "port matters");
        assert!(saved_for_page(&h, &page("https://h.example.evil.example/")).is_none());
    }

    #[test]
    fn targets_open_a_project_or_the_new_project_dialog() {
        assert_eq!(target_url("h.example", &Target::Home).unwrap().as_str(), "https://h.example/");
        assert_eq!(
            target_url("https://h.example/ark", &Target::Project("prj_1 &x".into())).unwrap().as_str(),
            "https://h.example/ark/?ark_open_project=prj_1+%26x"
        );
        assert_eq!(target_url("h.example", &Target::NewProject).unwrap().as_str(), "https://h.example/?ark_new_project=1");
        assert!(target_url("h.example", &Target::Project("  ".into())).is_err());
        assert_eq!(target_url("h.example", &Target::LinkDesktop).unwrap().as_str(), "https://h.example/?ark_link_desktop=1");
    }
}
