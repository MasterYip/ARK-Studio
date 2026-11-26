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
//!
//! Since 2.1.7 pages save through `save_download` instead (web/js/
//! download-link.js): the app requests the file itself with the page's
//! cookies, asks where to save it (the system "Save as" dialog) and streams
//! it there through a `.part` file, reporting progress; a failed response
//! comes back as the server's error text. The pieces below that do not need
//! Tauri ([`fetch_to`], [`PartFile`], [`disposition_name`], [`error_text`])
//! are tested here and in a standalone crate.

use std::collections::{HashMap, HashSet};
use std::fs::File;
use std::io::Write;
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
    cancelled: Mutex<HashSet<String>>,
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

    /// A file a page saved through the dialog: revealable by the returned id.
    pub fn record_saved(&self, path: &Path) -> u64 {
        let id = self.next.fetch_add(1, Ordering::Relaxed) + 1;
        self.done.lock().unwrap().insert(id, path.to_path_buf());
        id
    }

    /// A page asked to stop the download with this ticket.
    pub fn cancel(&self, ticket: &str) {
        self.cancelled.lock().unwrap().insert(ticket.to_string());
    }

    /// True once for a cancelled ticket.
    pub fn take_cancelled(&self, ticket: &str) -> bool {
        self.cancelled.lock().unwrap().remove(ticket)
    }
}

// ------------------------------------------------------------ save dialog

/// File name from a Content-Disposition header (`filename*=UTF-8''...` wins).
pub fn disposition_name(value: &str) -> Option<String> {
    let mut plain = None;
    for part in value.split(';').map(str::trim) {
        let Some((key, val)) = part.split_once('=') else { continue };
        let key = key.trim().to_ascii_lowercase();
        let val = val.trim();
        if key == "filename*" {
            let encoded = val.splitn(3, '\'').nth(2).unwrap_or(val);
            let decoded = url::form_urlencoded::parse(format!("x={}", encoded.replace('+', "%2B")).as_bytes())
                .next()
                .map(|(_, v)| v.to_string())
                .unwrap_or_default();
            if !decoded.trim().is_empty() {
                return Some(decoded);
            }
        } else if key == "filename" {
            let v = val.trim_matches('"').to_string();
            if !v.trim().is_empty() {
                plain = Some(v);
            }
        }
    }
    plain
}

/// What to tell the user about a failed response: the server's JSON
/// `error` / `detail` / `message`, else a short first line, else the status.
pub fn error_text(status: u16, body: &[u8]) -> String {
    let text = String::from_utf8_lossy(body);
    if let Ok(v) = serde_json::from_str::<serde_json::Value>(&text) {
        let pick = |v: &serde_json::Value| v.as_str().map(str::to_string);
        let msg = v
            .get("error")
            .and_then(|e| pick(e).or_else(|| e.get("message").and_then(pick)))
            .or_else(|| v.get("detail").and_then(pick))
            .or_else(|| v.get("message").and_then(pick));
        if let Some(m) = msg.filter(|m| !m.trim().is_empty()) {
            return m;
        }
    }
    let line = text.trim().lines().next().unwrap_or("").trim();
    if !line.is_empty() && line.len() < 200 && !line.starts_with('<') {
        return line.to_string();
    }
    format!("HTTP {status}")
}

/// Same scheme, host and port: a page may only save files of its own server.
pub fn same_origin(page: &url::Url, target: &url::Url) -> bool {
    matches!(target.scheme(), "http" | "https") && page.origin() == target.origin()
}

/// A download being written: `name.ext.part` next to the destination,
/// renamed over it when complete and removed when dropped unfinished.
pub struct PartFile {
    part: PathBuf,
    dest: PathBuf,
    file: Option<File>,
}

impl PartFile {
    pub fn create(dest: &Path) -> std::io::Result<Self> {
        let mut name = dest.file_name().map(|n| n.to_os_string()).unwrap_or_else(|| "download".into());
        name.push(".part");
        let part = dest.with_file_name(name);
        let file = File::create(&part)?;
        Ok(Self { part, dest: dest.to_path_buf(), file: Some(file) })
    }

    pub fn write(&mut self, bytes: &[u8]) -> std::io::Result<()> {
        self.file.as_mut().expect("open part file").write_all(bytes)
    }

    /// Flushes and moves the file to its destination (replacing a file the
    /// user chose to overwrite in the dialog).
    pub fn finish(mut self) -> std::io::Result<PathBuf> {
        let file = self.file.take().expect("open part file");
        let synced = file.sync_all();
        drop(file);
        if let Err(e) = synced.and_then(|_| std::fs::rename(&self.part, &self.dest)) {
            let _ = std::fs::remove_file(&self.part);
            return Err(e);
        }
        Ok(self.dest.clone())
    }
}

impl Drop for PartFile {
    fn drop(&mut self) {
        if let Some(file) = self.file.take() {
            drop(file);
            let _ = std::fs::remove_file(&self.part);
        }
    }
}

/// Why a save stopped.
#[derive(Debug, PartialEq, Eq)]
pub enum SaveError {
    /// The page cancelled it.
    Cancelled,
    /// The server, the network or the disk failed (text for the user).
    Failed(String),
}

/// Streams a successful response to `dest` (through a `.part` file).
/// `progress(received, total)` runs after each chunk; false stops the save.
pub async fn fetch_to(
    mut response: reqwest::Response,
    dest: &Path,
    mut progress: impl FnMut(u64, Option<u64>) -> bool,
) -> Result<u64, SaveError> {
    let total = response.content_length();
    let disk = |e: std::io::Error| SaveError::Failed(format!("{}: {e}", dest.display()));
    let mut part = PartFile::create(dest).map_err(disk)?;
    let mut received = 0u64;
    while let Some(chunk) = response.chunk().await.map_err(|e| SaveError::Failed(net_error(&e)))? {
        part.write(&chunk).map_err(disk)?;
        received += chunk.len() as u64;
        if !progress(received, total) {
            return Err(SaveError::Cancelled);
        }
    }
    if let Some(t) = total.filter(|t| received < *t) {
        return Err(SaveError::Failed(format!("the connection closed after {received} of {t} bytes")));
    }
    part.finish().map_err(disk)?;
    Ok(received)
}

/// A failed response's error text (from the first 64 KiB of its body).
pub async fn response_error(mut response: reqwest::Response) -> String {
    let status = response.status().as_u16();
    let mut body = Vec::new();
    while body.len() < 64 * 1024 {
        match response.chunk().await {
            Ok(Some(c)) => body.extend_from_slice(&c),
            _ => break,
        }
    }
    error_text(status, &body)
}

/// A network error with its causes (reqwest's top line alone is vague).
pub fn net_error(e: &reqwest::Error) -> String {
    let mut out = e.to_string();
    let mut cur = std::error::Error::source(e);
    while let Some(c) = cur {
        out.push_str(": ");
        out.push_str(&c.to_string());
        cur = c.source();
    }
    out
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

    #[test]
    fn disposition_names() {
        let ark = r#"attachment; filename="ARK_Develop.ark.zip"; filename*=UTF-8''ARK_Develop.ark.zip"#;
        assert_eq!(disposition_name(ark).as_deref(), Some("ARK_Develop.ark.zip"));
        assert_eq!(disposition_name("attachment; filename*=UTF-8''%E9%A1%B9%E7%9B%AE.zip").as_deref(), Some("项目.zip"));
        assert_eq!(disposition_name("attachment; filename*=UTF-8''a+b.zip").as_deref(), Some("a+b.zip"));
        assert_eq!(disposition_name(r#"attachment; filename="notes.md""#).as_deref(), Some("notes.md"));
        assert_eq!(disposition_name("attachment"), None);
    }

    #[test]
    fn error_texts() {
        assert_eq!(
            error_text(404, br#"{"error":"Project not found or not accessible for this account."}"#),
            "Project not found or not accessible for this account."
        );
        assert_eq!(error_text(400, br#"{"error":{"message":"bad"}}"#), "bad");
        assert_eq!(error_text(401, br#"{"detail":"Sign in first"}"#), "Sign in first");
        assert_eq!(error_text(502, b"<html><body>Bad gateway</body></html>"), "HTTP 502");
        assert_eq!(error_text(500, b"disk full\nmore"), "disk full");
        assert_eq!(error_text(503, b""), "HTTP 503");
    }

    #[test]
    fn only_the_page_origin() {
        let page = url::Url::parse("https://ark.example.com:8443/#/settings").unwrap();
        let ok = url::Url::parse("https://ark.example.com:8443/api/export?format=project").unwrap();
        let port = url::Url::parse("https://ark.example.com/api/export").unwrap();
        let other = url::Url::parse("https://evil.example.com:8443/api/export").unwrap();
        let file = url::Url::parse("file:///C:/x").unwrap();
        assert!(same_origin(&page, &ok));
        assert!(!same_origin(&page, &port) && !same_origin(&page, &other) && !same_origin(&page, &file));
    }

    #[test]
    fn part_files_move_or_vanish() {
        let dir = std::env::temp_dir().join(format!("ark-part-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let dest = dir.join("a.zip");
        std::fs::write(&dest, b"old").unwrap();
        let mut p = PartFile::create(&dest).unwrap();
        p.write(b"new data").unwrap();
        assert!(dir.join("a.zip.part").is_file());
        assert_eq!(p.finish().unwrap(), dest);
        assert_eq!(std::fs::read(&dest).unwrap(), b"new data", "replaces the chosen file");
        assert!(!dir.join("a.zip.part").exists());
        {
            let mut p = PartFile::create(&dir.join("b.zip")).unwrap();
            p.write(b"half").unwrap();
        }
        assert!(!dir.join("b.zip.part").exists() && !dir.join("b.zip").exists(), "unfinished: nothing left");
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn saved_files_and_cancel_tickets() {
        let d = Downloads::default();
        let tmp = std::env::temp_dir().join(format!("ark-saved-{}.zip", std::process::id()));
        std::fs::write(&tmp, b"x").unwrap();
        let id = d.record_saved(&tmp);
        assert_eq!(d.saved(id), Some(tmp.clone()));
        assert!(!d.take_cancelled("t1"));
        d.cancel("t1");
        assert!(d.take_cancelled("t1") && !d.take_cancelled("t1"));
        std::fs::remove_file(&tmp).unwrap();
    }

    // fetch_to against a local HTTP server: a full body, a cut connection,
    // a cancel, and an error body.
    fn serve(responses: Vec<Vec<u8>>) -> String {
        use std::io::{Read, Write as _};
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        std::thread::spawn(move || {
            for resp in responses {
                let (mut s, _) = listener.accept().unwrap();
                let mut buf = [0u8; 4096];
                let _ = s.read(&mut buf);
                let _ = s.write_all(&resp);
            }
        });
        format!("http://{addr}/api/export?format=project")
    }

    fn http_response(status: &str, headers: &str, body: &[u8]) -> Vec<u8> {
        let mut out = format!("HTTP/1.1 {status}\r\n{headers}Connection: close\r\n\r\n").into_bytes();
        out.extend_from_slice(body);
        out
    }

    #[test]
    fn fetch_to_streams_and_reports() {
        let rt = tokio::runtime::Builder::new_current_thread().enable_all().build().unwrap();
        let body: Vec<u8> = (0..300_000u32).map(|i| (i % 251) as u8).collect();
        let cd = "Content-Disposition: attachment; filename=\"Demo.ark.zip\"; filename*=UTF-8''Demo.ark.zip\r\n";
        let ok = http_response("200 OK", &format!("Content-Length: {}\r\n{cd}", body.len()), &body);
        let cut = http_response("200 OK", "Content-Length: 1000\r\n", &[7u8; 10]);
        let err = http_response("404 Not Found", "Content-Type: application/json\r\n",
            br#"{"error":"Project not found or not accessible for this account."}"#);
        let url = serve(vec![ok.clone(), cut, ok, err]);
        let dir = std::env::temp_dir().join(format!("ark-fetch-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        // The app installs this at startup (lib.rs run()); reqwest needs one.
        let _ = rustls::crypto::ring::default_provider().install_default();
        let client = reqwest::Client::builder().no_proxy().build().unwrap();
        rt.block_on(async {
            // 1. the whole file, with progress up to the total
            let resp = client.get(&url).send().await.unwrap();
            assert!(resp.status().is_success());
            let name = resp.headers().get("content-disposition").and_then(|v| v.to_str().ok()).and_then(disposition_name);
            assert_eq!(name.as_deref(), Some("Demo.ark.zip"));
            let dest = dir.join("Demo.ark.zip");
            let mut seen = Vec::new();
            let n = fetch_to(resp, &dest, |r, t| { seen.push((r, t)); true }).await.unwrap();
            assert_eq!(n, body.len() as u64);
            assert_eq!(std::fs::read(&dest).unwrap(), body);
            assert_eq!(seen.last(), Some(&(body.len() as u64, Some(body.len() as u64))));
            assert!(!dir.join("Demo.ark.zip.part").exists());

            // 2. the connection closes early: an error, no file
            let resp = client.get(&url).send().await.unwrap();
            let dest = dir.join("cut.zip");
            match fetch_to(resp, &dest, |_, _| true).await {
                Err(SaveError::Failed(_)) => {}
                other => panic!("cut connection: {other:?}"),
            }
            assert!(!dest.exists() && !dir.join("cut.zip.part").exists());

            // 3. cancelled after the first chunk: no file
            let resp = client.get(&url).send().await.unwrap();
            let dest = dir.join("cancel.zip");
            assert_eq!(fetch_to(resp, &dest, |_, _| false).await, Err(SaveError::Cancelled));
            assert!(!dest.exists() && !dir.join("cancel.zip.part").exists());

            // 4. an error response: the server's message
            let resp = client.get(&url).send().await.unwrap();
            assert_eq!(resp.status().as_u16(), 404);
            assert_eq!(response_error(resp).await, "Project not found or not accessible for this account.");
        });
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
