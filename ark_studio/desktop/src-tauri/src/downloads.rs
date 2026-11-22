//! Downloads in the app's windows (`on_download` in lib.rs).
//!
//! Without a download handler WebView2 saved a page download silently to
//! the Downloads folder and showed nothing in the borderless app window:
//! "Download project ZIP", an attachment's Download or
//! a transcript's "Download JSONL" seemed to do nothing. The app now takes every download
//! the page starts (`<a download>`, Content-Disposition: attachment, blob:
//! URLs): the webview still fetches it with the page's own cookies, the file
//! goes to the Downloads folder under the name the server or page suggested
//! (`name (1).ext` when it exists), and shell.js shows the progress and the
//! result in the page ("Show in folder", or the failure).
//!
//! Pages name a download by id; only finished files can be revealed.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;

/// Longest file name kept (bytes of UTF-8, before the extension is re-added).
const MAX_NAME: usize = 160;

/// The file name for a download: the webview's suggestion (from
/// Content-Disposition or the `download` attribute), else the URL's last
/// path segment, else "download"; made safe for every desktop file system.
pub fn file_name(suggested: &Path, url: &str) -> String {
    // The last component on any platform (a Windows path read on Linux too).
    let whole = suggested.to_string_lossy();
    let from_suggestion = whole.rsplit(['/', '\\']).next().unwrap_or("").to_string();
    let raw = if from_suggestion.trim().is_empty() { url_name(url) } else { from_suggestion };
    let clean = sanitize(&raw);
    if clean.is_empty() {
        "download".into()
    } else {
        clean
    }
}

fn url_name(url: &str) -> String {
    let Ok(parsed) = url::Url::parse(url) else { return String::new() };
    if !matches!(parsed.scheme(), "http" | "https") {
        return String::new();
    }
    let last = parsed.path_segments().and_then(|mut s| s.next_back()).unwrap_or("");
    url::form_urlencoded::parse(format!("x={last}").as_bytes())
        .next()
        .map(|(_, v)| v.to_string())
        .unwrap_or_default()
}

fn sanitize(name: &str) -> String {
    let mapped: String = name
        .chars()
        .map(|c| if c.is_control() || matches!(c, '<' | '>' | ':' | '"' | '/' | '\\' | '|' | '?' | '*') { '_' } else { c })
        .collect();
    let trimmed = mapped.trim().trim_end_matches(['.', ' ']).trim_start_matches('.').to_string();
    let (stem, ext) = split_ext(&trimmed);
    let mut stem = stem.to_string();
    if stem.len() > MAX_NAME {
        let mut cut = MAX_NAME;
        while !stem.is_char_boundary(cut) {
            cut -= 1;
        }
        stem.truncate(cut);
    }
    // CON, NUL, COM1 ... cannot be file names on Windows (with any extension).
    let upper = stem.to_ascii_uppercase();
    let reserved = matches!(upper.as_str(), "CON" | "PRN" | "AUX" | "NUL")
        || (upper.len() == 4 && (upper.starts_with("COM") || upper.starts_with("LPT")) && upper.as_bytes()[3].is_ascii_digit());
    if reserved {
        stem.insert(0, '_');
    }
    if stem.is_empty() {
        return String::new();
    }
    format!("{stem}{ext}")
}

/// ("archive", ".zip"); ("archive.tar", ".gz"); ("README", "").
fn split_ext(name: &str) -> (&str, &str) {
    match name.rfind('.') {
        Some(i) if i > 0 && i < name.len() - 1 && name.len() - i <= 12 => name.split_at(i),
        _ => (name, ""),
    }
}

/// `dir/name`, or `dir/stem (n).ext` for the first `n` that does not exist.
pub fn unique_path(dir: &Path, name: &str, exists: impl Fn(&Path) -> bool) -> PathBuf {
    let first = dir.join(name);
    if !exists(&first) {
        return first;
    }
    let (stem, ext) = split_ext(name);
    for n in 1..10_000 {
        let p = dir.join(format!("{stem} ({n}){ext}"));
        if !exists(&p) {
            return p;
        }
    }
    dir.join(format!("{stem} ({}){ext}", std::process::id()))
}

/// Where a download goes: the Downloads folder, a free name there.
pub fn target(dir: &Path, suggested: &Path, url: &str, exists: impl Fn(&Path) -> bool) -> PathBuf {
    unique_path(dir, &file_name(suggested, url), exists)
}

/// Downloads of this run: the ones in flight (id, URL, destination, in
/// order) and the finished files a page may reveal, by id. Pages only ever
/// see ids, never choose a path.
#[derive(Default)]
pub struct Downloads {
    next: AtomicU64,
    active: Mutex<Vec<(u64, String, PathBuf)>>,
    done: Mutex<HashMap<u64, PathBuf>>,
}

impl Downloads {
    /// Records a download that starts now; returns its id.
    pub fn started(&self, url: &str, path: &Path) -> u64 {
        let id = self.next.fetch_add(1, Ordering::Relaxed) + 1;
        self.active.lock().unwrap().push((id, url.to_string(), path.to_path_buf()));
        id
    }

    /// True for the destination of a download still in flight (a second
    /// download of the same name must not pick it again).
    pub fn pending(&self, path: &Path) -> bool {
        self.active.lock().unwrap().iter().any(|(_, _, p)| p == path)
    }

    /// The oldest download of `url` still in flight, removed from the list,
    /// with its final path; a successful file becomes revealable.
    pub fn finished(&self, url: &str, path: Option<&Path>, success: bool) -> Option<(u64, PathBuf)> {
        let (id, recorded) = {
            let mut active = self.active.lock().unwrap();
            let i = active.iter().position(|(_, u, _)| u == url)?;
            let (id, _, p) = active.remove(i);
            (id, p)
        };
        let path = path.map(Path::to_path_buf).filter(|p| !p.as_os_str().is_empty()).unwrap_or(recorded);
        if success {
            self.done.lock().unwrap().insert(id, path.clone());
        }
        Some((id, path))
    }

    /// The file of finished download `id`, while it still exists.
    pub fn saved(&self, id: u64) -> Option<PathBuf> {
        self.done.lock().unwrap().get(&id).filter(|p| p.is_file()).cloned()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn suggested_names_win_and_are_cleaned() {
        let url = "http://127.0.0.1:18890/api/export?format=project&project=p1";
        assert_eq!(file_name(Path::new(r"C:\dl\Demo-2026-11-22.zip"), url), "Demo-2026-11-22.zip");
        assert_eq!(file_name(Path::new("/srv/dl/notes.md"), url), "notes.md");
        assert_eq!(file_name(Path::new("a:b*c?.zip"), url), "a_b_c_.zip");
        assert_eq!(file_name(Path::new("report. "), url), "report");
        assert_eq!(file_name(Path::new(".hidden"), url), "hidden");
        assert_eq!(file_name(Path::new("CON.txt"), url), "_CON.txt");
        assert_eq!(file_name(Path::new("com1"), url), "_com1");
        assert_eq!(file_name(Path::new("console.txt"), url), "console.txt");
    }

    #[test]
    fn without_a_suggestion_the_url_names_the_file() {
        assert_eq!(file_name(Path::new(""), "https://ark.example.com/files/Paper%20draft.pdf"), "Paper draft.pdf");
        assert_eq!(file_name(Path::new(""), "https://ark.example.com/"), "download");
        assert_eq!(file_name(Path::new(""), "blob:http://127.0.0.1:1/4f2c"), "download");
        assert_eq!(file_name(Path::new(""), "not a url"), "download");
    }

    #[test]
    fn long_names_keep_their_extension() {
        let long = format!("{}.zip", "项".repeat(100));
        let name = file_name(Path::new(&long), "");
        assert!(name.ends_with(".zip"));
        assert!(name.len() <= MAX_NAME + 4);
    }

    #[test]
    fn existing_files_get_a_number() {
        let dir = Path::new("/dl");
        let taken: HashSet<PathBuf> = [dir.join("a.zip"), dir.join("a (1).zip"), dir.join("README")].into_iter().collect();
        let exists = |p: &Path| taken.contains(p);
        assert_eq!(unique_path(dir, "b.zip", exists), dir.join("b.zip"));
        assert_eq!(unique_path(dir, "a.zip", exists), dir.join("a (2).zip"));
        assert_eq!(unique_path(dir, "README", exists), dir.join("README (1)"));
        assert_eq!(target(dir, Path::new(r"C:\x\a.zip"), "", exists), dir.join("a (2).zip"));
    }

    #[test]
    fn only_finished_downloads_are_revealable() {
        let d = Downloads::default();
        let tmp = std::env::temp_dir().join(format!("ark-dl-test-{}.zip", std::process::id()));
        std::fs::write(&tmp, b"x").unwrap();
        let a = d.started("u1", &tmp);
        let b = d.started("u2", Path::new("/dl/failed.zip"));
        assert_ne!(a, b);
        assert_eq!(d.saved(a), None, "in flight");
        assert!(d.pending(&tmp) && !d.pending(Path::new("/dl/other.zip")));
        assert_eq!(d.finished("u1", None, true), Some((a, tmp.clone())));
        assert_eq!(d.saved(a), Some(tmp.clone()));
        assert_eq!(d.finished("u2", None, false), Some((b, PathBuf::from("/dl/failed.zip"))));
        assert_eq!(d.saved(b), None);
        assert_eq!(d.finished("unknown", None, false), None);
        assert_eq!(d.saved(999), None);
        std::fs::remove_file(&tmp).unwrap();
        assert_eq!(d.saved(a), None, "moved or deleted since");
    }

    #[test]
    fn the_same_url_twice_finishes_in_order() {
        let d = Downloads::default();
        let a = d.started("u", Path::new("/dl/a.zip"));
        let b = d.started("u", Path::new("/dl/a (1).zip"));
        assert_eq!(d.finished("u", Some(Path::new("/dl/a.zip")), false).map(|x| x.0), Some(a));
        assert_eq!(d.finished("u", None, false), Some((b, PathBuf::from("/dl/a (1).zip"))));
    }
}
