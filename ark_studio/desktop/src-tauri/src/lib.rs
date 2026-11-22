//! ARK Studio desktop shell (Tauri 2).
//!
//! The app always runs the bundled `ark-server` as a private loopback
//! sidecar (see `sidecar.rs`) and opens its Home: one default page, no
//! connection chooser. Saved remote ARK servers are one click away in the
//! local page's project switcher, in the tray and in Settings.
//!
//! Two borderless windows:
//!
//! * "main": always a server's own URL (or a bundled status page):
//!   - the local ARK, through its one-time claim URL;
//!   - a saved remote server, opened directly (sign-in happens on the
//!     server's own page; the app stores no passwords).
//! * "settings": the bundled Settings page (`ui/index.html`): local project
//!   folder and port, "Connect to a server…" with the saved servers, trusted
//!   folders, close-to-tray, data folder, server log and About (version, updates).
//!
//! What a page may call (Tauri capabilities; checked again in the commands):
//!
//! * bundled pages (Settings, status page): everything they need;
//! * the local ARK page: the window controls, the local-open bridge and the
//!   desktop hub (saved servers, their status, opening one, Settings);
//! * remote ARK pages: the window controls, the local-open bridge, "back to
//!   this computer" and their own desktop context only. They never see the
//!   saved servers or anything else about the local side. A remote page may
//!   also hand the app a one-time "Link this desktop" code for its own
//!   server (link_server); see links.rs.
//!
//! Desktop links (links.rs): with a saved server's per-device token the app
//! lists and creates projects there itself (from Rust, never from a page) and
//! hands the local ARK page the results.
//!
//! Window chrome: on Windows and Linux both windows are undecorated; ARK draws
//! the window controls (src/shell.js) and the page marks its drag regions.
//! On macOS the windows keep the native traffic lights over the page
//! (Overlay title bar, hidden title).
//!
//! Closing a window hides it to the tray (default), or quits when that
//! setting is off. Quitting stops the sidecar.
//!
//! Creating a window (`main_window`, `settings_window`) must not happen on
//! the main thread inside a synchronous command or a menu/tray handler: on
//! Windows, `WebviewWindowBuilder::build` deadlocks WebView2 there and the
//! app stops responding. Commands that may create a window are `async` (and
//! build through `spawn_blocking`); tray, menu and single-instance handlers
//! hand that work to another thread (`off_main`).
//!
//! Updates (Settings > About, and a banner on the local Home): the Tauri
//! updater checks `latest.json` of the latest GitHub release, then of the
//! fixed `updater` release (signed with the project's updater key), at most
//! once a day at start, and on demand. Installer builds update
//! themselves; the Windows portable build links to the release instead.

mod changelog;
mod downloads;
mod external;
mod links;
mod measure;
mod paths;
mod probe;
mod remote_open;
mod servers;
mod settings;
mod sidecar;
mod tray_popup;

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, OnceLock};
use std::time::Duration;

use serde_json::{json, Value};
use tauri::ipc::CapabilityBuilder;
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::webview::{DownloadEvent, NewWindowResponse};
use tauri::{AppHandle, Manager, RunEvent, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind, MessageDialogResult};
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_opener::OpenerExt;

use links::Links;
use servers::Target;
use settings::Settings;
use sidecar::Sidecar;

const MAIN: &str = "main";
const SETTINGS: &str = "settings";
const TRAY: &str = "ark";
const READY_TIMEOUT: Duration = Duration::from_secs(45);
/// Set at build time by CI when the updater signing key exists; overrides
/// `plugins.updater.pubkey` in tauri.conf.json.
const UPDATER_PUBKEY: Option<&str> = option_env!("ARK_UPDATER_PUBKEY");
/// The updater public key in use (see `pick_pubkey`); set once in `run`.
static UPDATER_KEY: OnceLock<Option<String>> = OnceLock::new();
/// GitHub releases of ARK Studio (About: release notes, portable downloads).
const RELEASES_URL: &str = "https://github.com/MasterYip/ARK-Studio/releases";
/// Window permissions every page of the app may use (controls, dragging).
const WINDOW_PERMISSIONS: &[&str] = &[
    "core:window:allow-minimize",
    "core:window:allow-toggle-maximize",
    "core:window:allow-internal-toggle-maximize",
    "core:window:allow-is-maximized",
    "core:window:allow-start-dragging",
    "allow-close-window",
];
/// Commands every ARK server page may use, local or remote: the local-open
/// bridge, "back to this computer", the page's own desktop context and the
/// image copy bridge.
const SERVER_PERMISSIONS: &[&str] = &[
    "allow-open-local-path",
    "allow-desktop-context",
    "allow-go-local-home",
    "allow-link-server",
    "allow-copy-image-png",
    "allow-device-id",
    "allow-measure-paths",
    // Resources: open a location over SSH or in VS Code / Cursor (remote_open.rs).
    "allow-open-ssh-terminal",
    "allow-ssh-config-match",
    "allow-open-editor-url",
    // "Show in folder" on a finished download (shell.js, downloads.rs).
    "allow-show-download",
    // "ARK Studio 2.x.y is available" in the app window (shell.js) and the
    // About dot: read the update state, "Later" for a version, and open the
    // app's own settings at a section (Settings > App on any ARK page).
    "allow-update-state",
    "allow-dismiss-update",
    "allow-open-app-settings",
];
/// The desktop hub: only the local ARK page (and the bundled pages).
const LOCAL_PERMISSIONS: &[&str] = &[
    "allow-desktop-servers",
    "allow-server-status",
    "allow-open-server",
    "allow-open-settings",
    "allow-server-projects",
    "allow-create-server-project",
    "allow-unlink-server",
    // Settings > About and App on the local page: check, install, open the
    // release page (portable builds), turn the automatic check on or off and
    // close the "updated to" notice.
    "allow-check-update",
    "allow-open-releases",
    "allow-set-update-auto-check",
    "allow-dismiss-update-notice",
    // Settings > Project storage location: the native folder picker.
    "allow-choose-project-folder",
];

#[derive(Clone, Debug, Default, PartialEq)]
enum Current {
    #[default]
    None,
    Starting,
    Local,
    Remote(String),
    Offline,
}

#[derive(Default)]
struct AppState {
    sidecar: Mutex<Option<Sidecar>>,
    current: Mutex<Current>,
    /// Origins with the server capability (local and remote ARK pages).
    granted: Mutex<HashSet<String>>,
    /// Local sidecar origins that also have the hub capability.
    granted_local: Mutex<HashSet<String>>,
    settings: Mutex<Settings>,
    /// Desktop links (per-server device tokens).
    links: Mutex<Links>,
    tray_items: Mutex<Option<TrayItems>>,
    quitting: AtomicBool,
    /// The newer version the last update check found.
    update: Mutex<Option<String>>,
    /// "ARK Studio was updated to ...": {version, previous, changes, url}
    /// after the first start of a new version, until the person closes it.
    notice: Mutex<Option<Value>>,
    /// Page downloads of this run (downloads.rs).
    downloads: downloads::Downloads,
}

struct TrayItems {
    start: MenuItem<tauri::Wry>,
    stop: MenuItem<tauri::Wry>,
}

/// The updater key: the build-time variable first, then tauri.conf.json.
fn pick_pubkey(env: Option<&str>, config: Option<&str>) -> Option<String> {
    env.into_iter().chain(config).map(str::trim).find(|k| !k.is_empty()).map(String::from)
}

fn updater_key() -> Option<&'static str> {
    UPDATER_KEY.get().and_then(|k| k.as_deref())
}

fn updater_enabled() -> bool {
    updater_key().is_some()
}

/// How this copy was installed. The bundler marks the binary it packs into
/// each installer (NSIS, MSI, .deb, AppImage; macOS is always an .app); the
/// Windows portable zip (release.yml copies the unmarked
/// `target/<triple>/release/ark-studio.exe`) and plain builds carry no mark.
fn install_kind() -> &'static str {
    use tauri::utils::config::BundleType;
    match tauri::utils::platform::bundle_type() {
        Some(BundleType::Nsis) => "nsis",
        Some(BundleType::Msi) => "msi",
        Some(BundleType::Deb) => "deb",
        Some(BundleType::Rpm) => "rpm",
        Some(BundleType::AppImage) => "appimage",
        Some(BundleType::App | BundleType::Dmg) => "app",
        None => "portable",
    }
}

/// The updater can replace this copy (an installer build, not portable).
fn self_install(kind: &str) -> bool {
    kind != "portable"
}

/// The release page of `version`, or the latest release.
fn release_page(version: Option<&str>) -> String {
    let version = version.map(|v| v.trim().trim_start_matches('v')).filter(|v| {
        !v.is_empty() && v.len() <= 40 && v.chars().all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | '+'))
    });
    match version {
        Some(v) => format!("{RELEASES_URL}/tag/v{v}"),
        None => format!("{RELEASES_URL}/latest"),
    }
}

/// Runs `f` on a new thread. Tray, menu and single-instance callbacks run on
/// the main thread, where creating a window deadlocks WebView2 on Windows;
/// from another thread the event loop creates it.
fn off_main(app: &AppHandle, f: impl FnOnce(&AppHandle) + Send + 'static) {
    let handle = app.clone();
    std::thread::spawn(move || f(&handle));
}

fn platform() -> &'static str {
    if cfg!(windows) {
        "windows"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else {
        "linux"
    }
}

fn state(app: &AppHandle) -> tauri::State<'_, AppState> {
    app.state::<AppState>()
}

fn user_agent(app: &AppHandle) -> String {
    format!("ARK-Studio-Desktop/{}", app.package_info().version)
}

/// A client for one call to a server. Built per call so that it follows the
/// current system proxy settings (proxy tools switch them while the app runs).
fn http(app: &AppHandle) -> reqwest::Client {
    reqwest::Client::builder()
        .user_agent(user_agent(app))
        .connect_timeout(Duration::from_secs(12))
        .timeout(Duration::from_secs(20))
        .build()
        .unwrap_or_default()
}

/// Origin of the bundled pages (`app.windows[].useHttpsScheme` is false).
fn app_origin() -> Url {
    let base = if cfg!(windows) { "http://tauri.localhost/" } else { "tauri://localhost/" };
    Url::parse(base).expect("static url")
}

/// A page of the bundled UI (`index.html`, `offline.html?...`).
fn app_page(page: &str) -> Url {
    app_origin().join(page).expect("bundled page url")
}

fn is_bundled(url: &Url) -> bool {
    match url.scheme() {
        "tauri" => url.host_str() == Some("localhost"),
        "http" | "https" => url.host_str() == Some("tauri.localhost"),
        _ => false,
    }
}

fn focus(w: &WebviewWindow) {
    let _ = w.unminimize();
    let _ = w.show();
    let _ = w.set_focus();
}

fn settings_snapshot(app: &AppHandle) -> Settings {
    state(app).settings.lock().unwrap().clone()
}

/// The saved servers with their link state (never the token).
fn servers_json(app: &AppHandle, history: &[String]) -> Vec<Value> {
    let links = state(app).links.lock().unwrap().clone();
    servers::list(history)
        .into_iter()
        .map(|e| {
            let link = links.get(&e.url);
            json!({
                "url": e.url,
                "origin": e.origin,
                "label": e.label,
                "linked": link.is_some(),
                "user": link.map(|l| l.user.clone()),
            })
        })
        .collect()
}

fn sidecar_origin(app: &AppHandle) -> Option<String> {
    state(app).sidecar.lock().unwrap().as_mut().and_then(|s| s.alive().then(|| s.origin()))
}

// ---------------------------------------------------------------- callers

/// Who called a command, from the calling page's URL (never from arguments).
#[derive(Debug)]
enum Caller {
    /// The bundled Settings / status pages.
    Bundled,
    /// The local ARK page (the running sidecar's origin).
    Local,
    /// A remote ARK page (an origin the app opened).
    Remote(Url),
    Other,
}

fn caller(app: &AppHandle, webview: &tauri::Webview) -> Caller {
    let Ok(url) = webview.url() else { return Caller::Other };
    if is_bundled(&url) {
        return Caller::Bundled;
    }
    let origin = url.origin().ascii_serialization();
    if sidecar_origin(app).is_some_and(|o| o == origin) {
        return Caller::Local;
    }
    if (url.scheme() == "http" || url.scheme() == "https") && state(app).granted.lock().unwrap().contains(&origin) {
        return Caller::Remote(url);
    }
    Caller::Other
}

/// The desktop hub is for the local ARK page and the bundled pages only.
fn require_hub(app: &AppHandle, webview: &tauri::Webview) -> Result<(), String> {
    match caller(app, webview) {
        Caller::Bundled | Caller::Local => Ok(()),
        _ => Err("Only the local ARK page of ARK Studio may use this.".into()),
    }
}

// ---------------------------------------------------------------- windows

/// Chrome configuration handed to src/shell.js for one window.
fn shell_script(app: &AppHandle, label: &str) -> String {
    let (chrome, controls, inset): (&str, Vec<&str>, u32) = if cfg!(target_os = "macos") {
        ("overlay", vec![], 80)
    } else if label == SETTINGS {
        ("custom", vec!["minimize", "close"], 0)
    } else {
        ("custom", vec!["minimize", "maximize", "close"], 0)
    };
    let cfg = json!({
        "version": app.package_info().version.to_string(),
        "platform": platform(),
        "chrome": chrome,
        "label": label,
        "controls": controls,
        "insetLeft": inset,
    });
    include_str!("shell.js").replace("__ARK_SHELL_CONFIG__", &cfg.to_string())
}

fn base_builder<'a>(app: &'a AppHandle, label: &'a str, url: WebviewUrl) -> WebviewWindowBuilder<'a, tauri::Wry, AppHandle> {
    let handle = app.clone();
    let builder = WebviewWindowBuilder::new(app, label, url)
        .title("ARK Studio")
        .initialization_script(shell_script(app, label))
        // Page downloads go to the Downloads folder and are shown by shell.js
        // (downloads.rs); without this the click had no visible result.
        .on_download(move |webview, event| on_download(&handle, &webview, event))
        // Tauri's native file-drop handler swallows HTML5 drag and drop on
        // Windows (workbench views, Mind/graph drags, file drops on the page
        // all went dead); the app reads no native drops, so turn it off.
        .disable_drag_drop_handler()
        .visible(false);
    #[cfg(target_os = "macos")]
    let builder = builder
        .decorations(true)
        .title_bar_style(tauri::TitleBarStyle::Overlay)
        .hidden_title(true);
    #[cfg(not(target_os = "macos"))]
    let builder = builder.decorations(false);
    builder
}

fn settings_window(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    if let Some(w) = app.get_webview_window(SETTINGS) {
        return Ok(w);
    }
    // Created blank and then navigated (show_settings): the shell script is
    // registered before the first real page loads.
    let builder = base_builder(app, SETTINGS, WebviewUrl::External("about:blank".parse().expect("url")))
        .resizable(true)
        .maximizable(false)
        .center();
    // Windows/Linux: a transparent window around an ARK card with its own
    // rounded corners and soft 30 px shadow in a 28 px margin.
    #[cfg(not(target_os = "macos"))]
    let builder = builder.inner_size(640.0, 800.0).min_inner_size(520.0, 480.0).transparent(true).shadow(false);
    #[cfg(target_os = "macos")]
    let builder = builder.inner_size(600.0, 780.0).min_inner_size(480.0, 440.0);
    builder.build()
}

fn main_window(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    if let Some(w) = app.get_webview_window(MAIN) {
        return Ok(w);
    }
    let handle = app.clone();
    let builder = base_builder(app, MAIN, WebviewUrl::External("about:blank".parse().expect("url")))
        .inner_size(1360.0, 880.0)
        .min_inner_size(900.0, 600.0)
        .center()
        .on_new_window(move |url, _features| {
            // Links that ask for a new window (target=_blank, window.open,
            // and plain links to other sites via shell.js) open in the system
            // browser; the local server needs a fresh claim there.
            let target = {
                let st = state(&handle);
                let guard = st.sidecar.lock().unwrap();
                match guard.as_ref() {
                    Some(s) if url.origin().ascii_serialization() == s.origin() => {
                        let mut path = url.path().to_string();
                        if let Some(q) = url.query() {
                            path = format!("{path}?{q}");
                        }
                        Url::parse(&s.claim_url(&path)).ok()
                    }
                    _ => Some(url.clone()),
                }
            };
            if let Some(t) = target.as_ref().and_then(external::system_target) {
                external::open(&handle, t);
            }
            NewWindowResponse::Deny
        });
    // Windows: DWM shadow (and rounded corners on Windows 11); native edge
    // resizing and Aero Snap stay available on the undecorated window.
    #[cfg(windows)]
    let builder = builder.shadow(true);
    let w = builder.build()?;
    #[cfg(windows)]
    hide_frame_border(&w);
    Ok(w)
}

// ---------------------------------------------------------------- downloads

/// The Downloads folder (`ARK_DESKTOP_DOWNLOADS` overrides it for tests).
fn download_dir(app: &AppHandle) -> PathBuf {
    if let Some(dir) = std::env::var_os("ARK_DESKTOP_DOWNLOADS").filter(|v| !v.is_empty()) {
        return PathBuf::from(dir);
    }
    app.path()
        .download_dir()
        .or_else(|_| app.path().home_dir().map(|h| h.join("Downloads")))
        .unwrap_or_else(|_| std::env::temp_dir())
}

/// A download a page started (`<a download>`, an attachment response, a
/// blob: URL): saved under a free name in the Downloads folder, with every
/// step reported to the page (shell.js `__ARK_DOWNLOAD_EVENT__`).
fn on_download(app: &AppHandle, webview: &tauri::Webview, event: DownloadEvent<'_>) -> bool {
    let list = &state(app).downloads;
    match event {
        DownloadEvent::Requested { url, destination } => {
            let dir = download_dir(app);
            if let Err(e) = std::fs::create_dir_all(&dir) {
                eprintln!("ark-studio: downloads folder {}: {e}", dir.display());
            }
            let path = downloads::target(&dir, destination, url.as_str(), |p| p.exists() || list.pending(p));
            *destination = path.clone();
            let id = list.started(url.as_str(), &path);
            tell_download(webview, id, "started", &path);
            true
        }
        DownloadEvent::Finished { url, path, success } => {
            if let Some((id, path)) = list.finished(url.as_str(), path.as_deref(), success) {
                tell_download(webview, id, if success { "done" } else { "failed" }, &path);
            }
            true
        }
        _ => true,
    }
}

fn tell_download(webview: &tauri::Webview, id: u64, step: &str, path: &Path) {
    let name = path.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
    let ev = json!({ "id": id.to_string(), "state": step, "name": name });
    let _ = webview.eval(format!("window.__ARK_DOWNLOAD_EVENT__&&window.__ARK_DOWNLOAD_EVENT__({ev})"));
}

/// "Show in folder" on a finished download (by its id; pages never name a path).
#[tauri::command]
fn show_download(app: AppHandle, webview: tauri::Webview, id: String) -> Result<(), String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    let id: u64 = id.parse().map_err(|_| "Unknown download.".to_string())?;
    let path = state(&app).downloads.saved(id).ok_or_else(|| "The downloaded file is no longer there.".to_string())?;
    app.opener().reveal_item_in_dir(path).map_err(|e| e.to_string())
}

/// Windows 11 draws a 1 px frame border around shadowed undecorated windows;
/// drop it and keep the shadow and rounded corners. (Windows 10 has no such
/// attribute: there the border only shows when the user enabled "Show accent
/// color on title bars and window borders".)
#[cfg(windows)]
fn hide_frame_border(w: &WebviewWindow) {
    use windows_sys::Win32::Graphics::Dwm::{DwmSetWindowAttribute, DWMWA_BORDER_COLOR};
    const DWMWA_COLOR_NONE: u32 = 0xFFFF_FFFE;
    if let Ok(hwnd) = w.hwnd() {
        let color = DWMWA_COLOR_NONE;
        unsafe {
            DwmSetWindowAttribute(
                hwnd.0 as _,
                DWMWA_BORDER_COLOR as u32,
                &color as *const u32 as *const core::ffi::c_void,
                std::mem::size_of::<u32>() as u32,
            );
        }
    }
}

/// Shows `url` in the main window and hides Settings.
fn open_main(app: &AppHandle, url: Url) {
    match main_window(app) {
        Ok(w) => {
            let _ = w.navigate(url);
            focus(&w);
        }
        Err(e) => eprintln!("ark-studio: main window: {e}"),
    }
    if let Some(c) = app.get_webview_window(SETTINGS) {
        let _ = c.hide();
    }
}

/// Opens Settings (`view`: "connect" focuses "Connect to a server…",
/// "about" the version and updates). May create the window: never call it
/// on the main thread from a command or a menu/tray handler (`off_main`).
fn show_settings(app: &AppHandle, notice: Option<&str>, view: Option<&str>) {
    let mut q = url::form_urlencoded::Serializer::new(String::new());
    if let Some(n) = notice {
        q.append_pair("notice", n);
    }
    if let Some(v) = view.filter(|v| matches!(*v, "connect" | "local" | "servers" | "about" | "window" | "data")) {
        q.append_pair("view", v);
    }
    let q = q.finish();
    let page = if q.is_empty() { "index.html".to_string() } else { format!("index.html?{q}") };
    match settings_window(app) {
        Ok(w) => {
            let _ = w.navigate(app_page(&page));
            focus(&w);
        }
        Err(e) => eprintln!("ark-studio: settings window: {e}"),
    }
}

/// Brings the app forward: the main window, or the local Home when nothing
/// is shown yet.
fn show_app(app: &AppHandle) {
    let current = state(app).current.lock().unwrap().clone();
    match (current, app.get_webview_window(MAIN)) {
        (Current::None, _) | (_, None) => go_local(app),
        (_, Some(w)) => focus(&w),
    }
}

/// A bundled status page in the main window: `kind` is "starting", "local"
/// (the local server failed or stopped) or "remote" (a server did not answer).
fn show_status(app: &AppHandle, kind: &str, target: &str, message: &str) {
    let q: String = url::form_urlencoded::Serializer::new(String::new())
        .append_pair("kind", kind)
        .append_pair("url", target)
        .append_pair("message", message)
        .finish();
    *state(app).current.lock().unwrap() = if kind == "starting" { Current::Starting } else { Current::Offline };
    open_main(app, app_page(&format!("offline.html?{q}")));
}

/// Lets pages of `origin` use the window controls, the local-open bridge
/// and "back to this computer"; the local sidecar origin also gets the hub.
fn grant_server_origin(app: &AppHandle, origin: &str, local: bool) {
    let st = state(app);
    let mut granted = st.granted.lock().unwrap();
    let mut granted_local = st.granted_local.lock().unwrap();
    if granted.contains(origin) && (!local || granted_local.contains(origin)) {
        return;
    }
    let id = format!("ark-server-{}", granted.len() + granted_local.len() + 1);
    let mut cap = CapabilityBuilder::new(id).remote(format!("{origin}/*")).window(MAIN);
    for p in WINDOW_PERMISSIONS.iter().chain(SERVER_PERMISSIONS) {
        cap = cap.permission(*p);
    }
    if local {
        for p in LOCAL_PERMISSIONS {
            cap = cap.permission(*p);
        }
    }
    match app.add_capability(cap) {
        Ok(()) => {
            granted.insert(origin.to_string());
            if local {
                granted_local.insert(origin.to_string());
            }
        }
        Err(e) => eprintln!("ark-studio: capability for {origin}: {e}"),
    }
}

fn refresh_tray(app: &AppHandle) {
    let running = state(app).sidecar.lock().unwrap().as_mut().is_some_and(|s| s.alive());
    if let Some(items) = state(app).tray_items.lock().unwrap().as_ref() {
        let _ = items.start.set_enabled(!running);
        let _ = items.stop.set_enabled(running);
    }
}

fn stop_sidecar(app: &AppHandle) {
    let taken = state(app).sidecar.lock().unwrap().take();
    if let Some(mut s) = taken {
        s.stop();
    }
    refresh_tray(app);
}

/// Starts (or reuses) the local server for `root` and opens its Home.
fn start_local(app: &AppHandle, root: PathBuf, port: u16) -> Result<Value, String> {
    {
        let st = state(app);
        let mut guard = st.sidecar.lock().unwrap();
        let reuse = guard.as_mut().is_some_and(|s| s.alive() && s.root == root && s.requested_port == port);
        if !reuse {
            if let Some(mut old) = guard.take() {
                old.stop();
            }
            let fallback = st.settings.lock().unwrap().fallback_port;
            let mut fresh = sidecar::start(&root, port, fallback)?;
            fresh.wait_ready(READY_TIMEOUT)?;
            // Remember a fallback port for the next start (see sidecar::pick_port).
            if fresh.port != port && fresh.port != fallback {
                let mut s = st.settings.lock().unwrap();
                s.fallback_port = fresh.port;
                if let Err(e) = s.save() {
                    eprintln!("ark-studio: settings: {e}");
                }
            }
            *guard = Some(fresh);
        }
    }
    let (origin, claim, actual, requested) = {
        let st = state(app);
        let guard = st.sidecar.lock().unwrap();
        let s = guard.as_ref().ok_or("ark-server is not running")?;
        (s.origin(), s.claim_url("/"), s.port, s.requested_port)
    };
    grant_server_origin(app, &origin, true);
    *state(app).current.lock().unwrap() = Current::Local;
    refresh_tray(app);
    open_main(app, Url::parse(&claim).map_err(|e| e.to_string())?);
    Ok(json!({ "port": actual, "requestedPort": requested, "fallback": requested != 0 && actual != requested }))
}

/// The local Home: at once when the server runs, else after starting it
/// (with the "Starting ARK…" page meanwhile).
fn go_local(app: &AppHandle) {
    go_local_at(app, "/");
}

/// The local ARK at `next` (a path of the local server, e.g. its Settings).
fn go_local_at(app: &AppHandle, next: &str) {
    let claim = state(app).sidecar.lock().unwrap().as_mut().and_then(|s| s.alive().then(|| s.claim_url(next)));
    if let Some(claim) = claim.and_then(|c| Url::parse(&c).ok()) {
        *state(app).current.lock().unwrap() = Current::Local;
        open_main(app, claim);
        return;
    }
    show_status(app, "starting", "", "");
    let handle = app.clone();
    std::thread::spawn(move || {
        let s = settings_snapshot(&handle);
        if let Err(e) = start_local(&handle, s.root(), s.port()) {
            show_status(&handle, "local", "", &e);
        }
    });
}

fn open_remote(app: &AppHandle, url: &Url) {
    let origin = url.origin().ascii_serialization();
    grant_server_origin(app, &origin, false);
    *state(app).current.lock().unwrap() = Current::Remote(url.to_string());
    open_main(app, url.clone());
}

/// GET <server>/api/health: reachable (any HTTP answer) and ARK (2xx); when
/// unreachable, the reason (see `probe::check`).
async fn probe(app: &AppHandle, server: &str) -> Value {
    let Ok(base) = settings::normalize_remote(server) else {
        return json!({ "url": server, "reachable": false, "ark": false, "reason": "invalid", "error": "invalid address" });
    };
    probe::check(&base, &user_agent(app)).await
}

fn reachable(check: &Value) -> bool {
    check["reachable"].as_bool().unwrap_or(false)
}

/// Saves `url` as the most recent server (and rebuilds the tray menu).
fn remember_server(app: &AppHandle, url: &str) -> Result<(), String> {
    {
        let st = state(app);
        let mut s = st.settings.lock().unwrap();
        s.remember_remote(url);
        s.save()?;
    }
    refresh_tray_menu(app);
    Ok(())
}

/// Opens `target` on a saved server; unless `force`, only when it answers.
async fn open_saved(app: &AppHandle, raw: &str, target: Target, force: bool) -> Result<Value, String> {
    let history = settings_snapshot(app).remote_history;
    let entry = servers::find_saved(&history, raw).ok_or("This server is not in the saved list.")?;
    if !force {
        let check = probe(app, &entry.url).await;
        if !reachable(&check) {
            let mut v = probe::unreachable(&check, &entry.url);
            v["label"] = json!(entry.label);
            return Ok(v);
        }
    }
    let url = servers::target_url(&entry.url, &target)?;
    remember_server(app, &entry.url)?;
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || open_remote(&handle, &url)).await.map_err(|e| e.to_string())?;
    Ok(json!({ "opened": true, "url": entry.url }))
}

fn git_found() -> bool {
    paths::find_in_path("git").is_some()
}

fn quit(app: &AppHandle) {
    state(app).quitting.store(true, Ordering::SeqCst);
    stop_sidecar(app);
    app.exit(0);
}

/// Close button / Alt+F4 policy. Settings just goes away; the main window
/// hides to the tray while "Keep running in the tray" is on (the default),
/// else ARK Studio quits.
fn close_requested(app: &AppHandle, label: &str) {
    if label == SETTINGS {
        if let Some(c) = app.get_webview_window(SETTINGS) {
            let _ = c.hide();
        }
        let main_visible = app.get_webview_window(MAIN).is_some_and(|w| w.is_visible().unwrap_or(false));
        if main_visible || state(app).settings.lock().unwrap().close_to_tray {
            return;
        }
    } else if state(app).settings.lock().unwrap().close_to_tray {
        if let Some(w) = app.get_webview_window(label) {
            let _ = w.hide();
        }
        return;
    }
    let handle = app.clone();
    std::thread::spawn(move || quit(&handle));
}

// ---------------------------------------------------------------- commands
// Bundled pages (Settings, status page, tray window): capabilities/bundled.json.

/// The tray window's card is `width` x `height` CSS pixels: place and show it.
#[tauri::command]
fn tray_menu_ready(app: AppHandle, webview: tauri::Webview, width: f64, height: f64) -> Result<(), String> {
    if webview.label() != tray_popup::LABEL {
        return Err("Only the tray window may use this.".into());
    }
    tray_popup::ready(&app, width, height)
}

/// An item of the tray window (the native menu's ids); "dismiss" only hides it.
#[tauri::command]
fn tray_menu_action(app: AppHandle, webview: tauri::Webview, id: String) -> Result<(), String> {
    if webview.label() != tray_popup::LABEL {
        return Err("Only the tray window may use this.".into());
    }
    tray_popup::hide(&app);
    if id != "dismiss" {
        on_tray_menu(&app, &id);
    }
    Ok(())
}

#[tauri::command]
fn get_state(app: AppHandle) -> Value {
    let st = state(&app);
    let s = st.settings.lock().unwrap().clone();
    let local = st
        .sidecar
        .lock()
        .unwrap()
        .as_mut()
        .and_then(|s| s.alive().then(|| json!({ "port": s.port, "root": s.root, "origin": s.origin() })));
    json!({
        "version": app.package_info().version.to_string(),
        "platform": platform(),
        "settings": s,
        "servers": servers_json(&app, &s.remote_history),
        "defaultRoot": paths::default_root(),
        "defaultPort": settings::DEFAULT_PORT,
        "dataDir": paths::data_dir(),
        "layout": paths::layout(),
        "logFile": paths::server_log(),
        "gitFound": git_found(),
        "serverFound": sidecar::server_exe().is_ok(),
        "local": local,
        "update": update_info(&app),
    })
}

/// About: what the updater can do here and what it last found.
fn update_info(app: &AppHandle) -> Value {
    let kind = install_kind();
    let s = settings_snapshot(app);
    json!({
        "current": app.package_info().version.to_string(),
        "enabled": updater_enabled(),
        "install": kind,
        "selfInstall": self_install(kind),
        "available": state(app).update.lock().unwrap().clone(),
        "autoCheck": s.auto_update_check,
        "lastCheck": s.last_update_check,
        "dismissed": s.dismissed_update,
        "notice": state(app).notice.lock().unwrap().clone(),
    })
}

#[tauri::command]
async fn pick_folder(app: AppHandle, current: Option<String>) -> Option<String> {
    let mut dialog = app.dialog().file().set_title("Choose a folder");
    if let Some(c) = current.filter(|c| Path::new(c).is_dir()) {
        dialog = dialog.set_directory(c);
    }
    dialog.blocking_pick_folder().and_then(|p| p.into_path().ok()).map(|p| p.to_string_lossy().to_string())
}

/// Where the folder picker opens: `current` or its nearest existing parent,
/// else `fallback` when it exists.
fn picker_start(current: Option<&str>, fallback: &Path) -> Option<PathBuf> {
    let mut dir = current.map(str::trim).filter(|c| !c.is_empty()).map(PathBuf::from);
    while let Some(d) = dir {
        if d.is_absolute() && d.is_dir() {
            return Some(d);
        }
        dir = d.parent().map(Path::to_path_buf);
    }
    fallback.is_dir().then(|| fallback.to_path_buf())
}

/// Settings > Project storage location (local ARK page): the native folder
/// picker. The page checks and saves the folder through its own server,
/// which applies it at once (no restart).
#[tauri::command]
async fn choose_project_folder(app: AppHandle, webview: tauri::Webview, current: Option<String>) -> Result<Option<String>, String> {
    require_hub(&app, &webview)?;
    let mut dialog = app.dialog().file().set_title(if system_zh() { "选择项目存储位置" } else { "Choose the project storage location" });
    if let Some(start) = picker_start(current.as_deref(), &paths::default_root()) {
        dialog = dialog.set_directory(start);
    }
    Ok(dialog.blocking_pick_folder().and_then(|p| p.into_path().ok()).map(|p| p.to_string_lossy().to_string()))
}

/// Saves the local project folder and port, (re)starts the local server
/// when they changed and opens the local Home.
#[tauri::command]
async fn connect_local(app: AppHandle, root: String, port: u16) -> Result<Value, String> {
    let root_path = if root.trim().is_empty() { paths::default_root() } else { PathBuf::from(root.trim()) };
    if !root_path.is_absolute() {
        return Err("Choose an absolute folder path.".into());
    }
    {
        let st = state(&app);
        let mut s = st.settings.lock().unwrap();
        s.local_root = if root_path == paths::default_root() { String::new() } else { root_path.to_string_lossy().into() };
        s.local_port = port;
        s.save()?;
    }
    let port = if port == 0 { settings::DEFAULT_PORT } else { port };
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || start_local(&handle, root_path, port))
        .await
        .map_err(|e| e.to_string())?
}

/// "Connect to a server…": saves the address and opens it. Unless `force`,
/// an address that does not answer is reported instead of opened.
#[tauri::command]
async fn connect_remote(app: AppHandle, url: String, force: bool) -> Result<Value, String> {
    let url = settings::normalize_remote(&url)?;
    if !force {
        let check = probe(&app, url.as_str()).await;
        if !reachable(&check) {
            return Ok(probe::unreachable(&check, url.as_str()));
        }
    }
    remember_server(&app, url.as_str())?;
    let handle = app.clone();
    let target = url.clone();
    tauri::async_runtime::spawn_blocking(move || open_remote(&handle, &target)).await.map_err(|e| e.to_string())?;
    Ok(json!({ "opened": true, "url": url.as_str() }))
}

#[tauri::command]
fn save_settings(app: AppHandle, patch: Value) -> Result<Value, String> {
    let st = state(&app);
    let mut s = st.settings.lock().unwrap();
    if let Some(v) = patch.get("closeToTray").and_then(Value::as_bool) {
        s.close_to_tray = v;
        s.close_to_tray_chosen = true;
    }
    if let Some(list) = patch.get("localOpenRoots").and_then(Value::as_array) {
        s.local_open_roots = list
            .iter()
            .filter_map(Value::as_str)
            .map(str::trim)
            .filter(|p| Path::new(p).is_absolute())
            .map(String::from)
            .collect();
    }
    if let Some(v) = patch.get("autoUpdateCheck").and_then(Value::as_bool) {
        s.auto_update_check = v;
    }
    s.save()?;
    Ok(serde_json::to_value(&*s).unwrap_or(Value::Null))
}

#[tauri::command]
async fn forget_remote(app: AppHandle, url: String) -> Result<(), String> {
    let key = settings::normalize_remote(&url).map(|u| u.to_string()).unwrap_or(url);
    {
        let st = state(&app);
        let mut s = st.settings.lock().unwrap();
        s.remote_history.retain(|u| *u != key);
        s.save()?;
    }
    refresh_tray_menu(&app);
    // A forgotten server keeps no link: end its device session there too.
    unlink(&app, &key).await;
    Ok(())
}

#[tauri::command]
fn open_data_dir(app: AppHandle) -> Result<(), String> {
    let dir = paths::data_dir();
    paths::ensure_private_dir(&dir).map_err(|e| e.to_string())?;
    app.opener().open_path(dir.to_string_lossy(), None::<&str>).map_err(|e| e.to_string())
}

#[tauri::command]
fn open_log(app: AppHandle) -> Result<(), String> {
    let log = paths::server_log();
    if !log.exists() {
        return Err("No server log yet.".into());
    }
    app.opener().reveal_item_in_dir(log).map_err(|e| e.to_string())
}

#[tauri::command]
async fn stop_local(app: AppHandle) {
    let handle = app.clone();
    let _ = tauri::async_runtime::spawn_blocking(move || stop_sidecar(&handle)).await;
}

/// The window close button. Synchronous on purpose (it only hides windows
/// or quits on another thread; it never creates one).
#[tauri::command]
fn close_window(app: AppHandle, window: tauri::Window) {
    close_requested(&app, window.label());
}

/// Asks the release endpoint for a newer version and remembers the answer
/// (About, the tray). A successful check counts as the day's check.
async fn fetch_update(app: &AppHandle) -> Result<Option<tauri_plugin_updater::Update>, String> {
    use tauri_plugin_updater::UpdaterExt;
    let update = app.updater().map_err(|e| e.to_string())?.check().await.map_err(|e| e.to_string())?;
    {
        let st = state(app);
        let mut s = st.settings.lock().unwrap();
        s.last_update_check = now_secs();
        if let Err(e) = s.save() {
            eprintln!("ark-studio: settings: {e}");
        }
    }
    let found = update.as_ref().map(|u| u.version.clone());
    let changed = {
        let st = state(app);
        let mut current = st.update.lock().unwrap();
        let changed = *current != found;
        *current = found.clone();
        changed
    };
    if changed {
        refresh_tray_menu(app);
        update_tray_tooltip(app);
        if let Some(v) = found.as_deref() {
            announce_update(app, v);
        }
    }
    Ok(update)
}

/// A system notification (best effort: the system may have them turned off).
fn notify(app: &AppHandle, title: &str, body: &str) {
    if let Err(e) = app.notification().builder().title(title).body(body).show() {
        eprintln!("ark-studio: notification: {e}");
    }
}

/// "ARK Studio 2.x.y is available": once per version.
fn announce_update(app: &AppHandle, version: &str) {
    let first = {
        let st = state(app);
        let mut s = st.settings.lock().unwrap();
        let first = s.take_notification(version);
        if first {
            if let Err(e) = s.save() {
                eprintln!("ark-studio: settings: {e}");
            }
        }
        first
    };
    if first {
        notify(app, &update_text("available_title", version), update_text("available_body", version).as_str());
    }
}

/// The tray icon's tooltip names a newer version.
fn update_tray_tooltip(app: &AppHandle) {
    let Some(tray) = app.tray_by_id(TRAY) else { return };
    let tip = match state(app).update.lock().unwrap().as_deref() {
        Some(v) => format!("ARK Studio · {}", update_text("available_title", v)),
        None => "ARK Studio".to_string(),
    };
    let _ = tray.set_tooltip(Some(tip));
}

/// Update notices in the system language (worded like ui/i18n.js).
fn update_text(key: &str, version: &str) -> String {
    let zh = system_zh();
    match key {
        "available_title" => if zh { format!("ARK Studio {version} 可用") } else { format!("ARK Studio {version} is available") },
        "available_body" => if zh {
            "打开 ARK Studio，点「更新」即可安装；托盘菜单里也有。".to_string()
        } else {
            "Open ARK Studio and choose Update to install it; it is also in the tray menu.".to_string()
        },
        "updated_title" => if zh { format!("ARK Studio 已更新到 {version}") } else { format!("ARK Studio was updated to {version}") },
        "updated_body" => if zh { "首页列出了这次更新的主要内容。".to_string() } else { "The Home page lists what changed.".to_string() },
        _ => String::new(),
    }
}

/// First start of a new version: remember it, keep the "updated to" notice
/// (key changes from the bundled CHANGELOG.md) and tell the system once.
fn note_updated(app: &AppHandle) {
    let current = app.package_info().version.to_string();
    let previous = {
        let st = state(app);
        let mut s = st.settings.lock().unwrap();
        let before = s.last_seen_version.clone();
        let previous = s.note_version(&current);
        if s.last_seen_version != before {
            if let Err(e) = s.save() {
                eprintln!("ark-studio: settings: {e}");
            }
        }
        previous
    };
    let Some(previous) = previous else { return };
    *state(app).notice.lock().unwrap() = Some(json!({
        "version": current,
        "previous": previous,
        "changes": changelog::notes(changelog::CHANGELOG, &current, 6),
        "url": release_page(Some(&current)),
    }));
    notify(app, &update_text("updated_title", &current), &update_text("updated_body", &current));
}

/// The update state for the local Home's banner (no network: what the last
/// check found). Same shape as `update_info`.
#[tauri::command]
fn update_state(app: AppHandle) -> Value {
    update_info(&app)
}

/// About: "Check for updates" (`install` false) and "Update and restart"
/// (`install` true: download, stop the local server, install, restart).
/// Returns the same shape as `update_info`.
#[tauri::command]
async fn check_update(app: AppHandle, install: bool) -> Result<Value, String> {
    if !updater_enabled() {
        return Ok(update_info(&app));
    }
    if install && !self_install(install_kind()) {
        return Err("This copy of ARK Studio cannot install updates itself. Download the new version instead.".into());
    }
    let update = fetch_update(&app).await?;
    if let (true, Some(update)) = (install, update) {
        let bytes = update.download(|_, _| {}, || {}).await.map_err(|e| e.to_string())?;
        // The installer replaces ark-server too: stop it first.
        state(&app).quitting.store(true, Ordering::SeqCst);
        let handle = app.clone();
        let _ = tauri::async_runtime::spawn_blocking(move || stop_sidecar(&handle)).await;
        if let Err(e) = update.install(bytes) {
            // Nothing was replaced: bring the local server back.
            state(&app).quitting.store(false, Ordering::SeqCst);
            if *state(&app).current.lock().unwrap() == Current::Local {
                off_main(&app, go_local);
            }
            return Err(e.to_string());
        }
        app.restart();
    }
    Ok(update_info(&app))
}

/// Settings > About on the local ARK page: "Check for updates when ARK
/// Studio starts". Returns the same shape as `update_info`.
#[tauri::command]
fn set_update_auto_check(app: AppHandle, webview: tauri::Webview, enabled: bool) -> Result<Value, String> {
    require_hub(&app, &webview)?;
    {
        let st = state(&app);
        let mut s = st.settings.lock().unwrap();
        s.auto_update_check = enabled;
        s.save()?;
    }
    Ok(update_info(&app))
}

/// About: a release page in the browser (`version`: that release, else the
/// latest; the portable build's "Download").
#[tauri::command]
fn open_releases(app: AppHandle, version: Option<String>) -> Result<(), String> {
    app.opener().open_url(release_page(version.as_deref()), None::<&str>).map_err(|e| e.to_string())
}

/// The automatic check at start: once a day at most, when the updater is
/// built in and the user left it on; a failure waits for the next start.
fn spawn_update_check(app: &AppHandle) {
    if !updater_enabled() {
        return;
    }
    let handle = app.clone();
    tauri::async_runtime::spawn(async move {
        // Leave the network to the local server's start first; then look
        // every hour whether a check is due (every 12 hours while running).
        let mut wait = Duration::from_secs(8);
        loop {
            let _ = tauri::async_runtime::spawn_blocking(move || std::thread::sleep(wait)).await;
            wait = Duration::from_secs(60 * 60);
            if !settings_snapshot(&handle).update_check_due(now_secs()) {
                continue;
            }
            if let Err(e) = fetch_update(&handle).await {
                eprintln!("ark-studio: update check: {e}");
            }
        }
    });
}

/// "Later" in the app window's update notice: hide it for `version`.
#[tauri::command]
fn dismiss_update(app: AppHandle, webview: tauri::Webview, version: String) -> Result<Value, String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    let version: String = version.trim().trim_start_matches('v').chars().take(40).collect();
    {
        let st = state(&app);
        let mut s = st.settings.lock().unwrap();
        s.dismissed_update = version;
        s.save()?;
    }
    Ok(update_info(&app))
}

/// Closes the "ARK Studio was updated to ..." notice (local Home, About).
#[tauri::command]
fn dismiss_update_notice(app: AppHandle, webview: tauri::Webview) -> Result<Value, String> {
    require_hub(&app, &webview)?;
    *state(&app).notice.lock().unwrap() = None;
    Ok(update_info(&app))
}

/// Settings > App on any ARK page: the app's own settings window at a
/// section ("about", "servers", "local", "window", "data").
#[tauri::command]
async fn open_app_settings(app: AppHandle, webview: tauri::Webview, view: Option<String>) -> Result<(), String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || show_settings(&handle, None, view.as_deref()))
        .await
        .map_err(|e| e.to_string())
}

/// The app's settings window: "Settings of this server" opens the local
/// ARK's own Settings (project, personal and admin settings).
#[tauri::command]
async fn open_local_settings(app: AppHandle, webview: tauri::Webview) -> Result<(), String> {
    if !matches!(caller(&app, &webview), Caller::Bundled) {
        return Err("Only the app's own pages may use this.".into());
    }
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || go_local_at(&handle, "/?settings=overview#/settings"))
        .await
        .map_err(|e| e.to_string())
}

// ---------------------------------------------------------------- server pages
// Every ARK page (local and remote): grant_server_origin, SERVER_PERMISSIONS.

/// What the calling page is: "local" (the local ARK), "remote" (a saved
/// server; `server` is its address), "bundled". A remote page learns nothing
/// about the local side.
#[tauri::command]
fn desktop_context(app: AppHandle, webview: tauri::Webview) -> Result<Value, String> {
    let version = app.package_info().version.to_string();
    match caller(&app, &webview) {
        Caller::Bundled => Ok(json!({ "role": "bundled", "version": version })),
        Caller::Local => Ok(json!({ "role": "local", "version": version, "hub": 1 })),
        Caller::Remote(url) => {
            let saved = servers::saved_for_page(&settings_snapshot(&app).remote_history, &url);
            let linked = saved.as_ref().is_some_and(|e| state(&app).links.lock().unwrap().get(&e.url).is_some());
            Ok(json!({ "role": "remote", "version": version, "server": saved.map(|e| e.url), "linked": linked, "link": 1 }))
        }
        Caller::Other => Err("Not an ARK page of ARK Studio.".into()),
    }
}

/// "Back to this computer": the local ARK Home in the main window (async:
/// it may have to create the main window).
#[tauri::command]
async fn go_local_home(app: AppHandle, webview: tauri::Webview) -> Result<(), String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || go_local(&handle)).await.map_err(|e| e.to_string())
}

// ---------------------------------------------------------------- desktop hub
// The local ARK page (and bundled pages): LOCAL_PERMISSIONS + require_hub.

/// The saved servers, most recently used first.
#[tauri::command]
fn desktop_servers(app: AppHandle, webview: tauri::Webview) -> Result<Value, String> {
    require_hub(&app, &webview)?;
    let s = settings_snapshot(&app);
    Ok(json!({ "servers": servers_json(&app, &s.remote_history) }))
}

/// Whether a saved server answers (GET /api/health, a few seconds at most).
#[tauri::command]
async fn server_status(app: AppHandle, webview: tauri::Webview, url: String) -> Result<Value, String> {
    require_hub(&app, &webview)?;
    let history = settings_snapshot(&app).remote_history;
    let entry = servers::find_saved(&history, &url).ok_or("This server is not in the saved list.")?;
    Ok(probe(&app, &entry.url).await)
}

/// Opens a saved server in the main window: its Home, a project
/// (`project`) or its new-project dialog (`newProject`).
#[tauri::command]
async fn open_server(
    app: AppHandle,
    webview: tauri::Webview,
    url: String,
    project: Option<String>,
    new_project: Option<bool>,
    link: Option<bool>,
    force: Option<bool>,
) -> Result<Value, String> {
    require_hub(&app, &webview)?;
    let target = match (project.filter(|p| !p.trim().is_empty()), new_project.unwrap_or(false), link.unwrap_or(false)) {
        (Some(id), _, _) => Target::Project(id),
        (None, true, _) => Target::NewProject,
        (None, false, true) => Target::LinkDesktop,
        (None, false, false) => Target::Home,
    };
    open_saved(&app, &url, target, force.unwrap_or(false)).await
}

/// Settings from the local ARK page ("Connect to a server…") or the status
/// page. Async: the first call creates the Settings window, and a
/// synchronous command runs on the main thread, where that deadlocks WebView2
/// on Windows.
#[tauri::command]
async fn open_settings(app: AppHandle, webview: tauri::Webview, view: Option<String>) -> Result<(), String> {
    require_hub(&app, &webview)?;
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || show_settings(&handle, None, view.as_deref()))
        .await
        .map_err(|e| e.to_string())
}

// ---------------------------------------------------------------- desktop links

fn now_secs() -> u64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// One request to a saved server: (HTTP status, JSON body or Null).
async fn server_call(
    app: &AppHandle,
    server: &str,
    method: reqwest::Method,
    path: &str,
    token: Option<&str>,
    body: Option<Value>,
) -> Result<(u16, Value), String> {
    let base = settings::normalize_remote(server)?;
    let url = base.join(path).map_err(|e| e.to_string())?;
    let mut req = http(app).request(method, url).header("Accept", "application/json");
    if let Some(t) = token {
        req = req.header("X-ARK-Mobile-Token", t);
    }
    if let Some(b) = body {
        req = req.json(&b);
    }
    let resp = req.send().await.map_err(|e| e.to_string())?;
    let status = resp.status().as_u16();
    let value = resp.json::<Value>().await.unwrap_or(Value::Null);
    Ok((status, value))
}

fn server_message(v: &Value, fallback: &str) -> String {
    v.pointer("/error/message").and_then(Value::as_str).filter(|m| !m.is_empty()).unwrap_or(fallback).to_string()
}

fn take_link(app: &AppHandle, server: &str) -> Option<links::Link> {
    let st = state(app);
    let mut l = st.links.lock().unwrap();
    let gone = l.remove(server);
    if gone.is_some() {
        if let Err(e) = l.save() {
            eprintln!("ark-studio: server links: {e}");
        }
    }
    gone
}

/// Forgets the link of `server` and ends its device session there (best
/// effort: an unreachable server keeps the session until it is revoked).
async fn unlink(app: &AppHandle, server: &str) {
    if let Some(link) = take_link(app, server) {
        let _ = server_call(app, server, reqwest::Method::DELETE, "api/mobile/session", Some(&link.token), None).await;
    }
}

/// "Link this desktop" on a remote page: the page hands over the one-time
/// code it got from its own server (POST /api/desktop/link); the app claims
/// it there and keeps the device token. Only for saved servers, and only
/// for the server the calling page belongs to.
#[tauri::command]
async fn link_server(app: AppHandle, webview: tauri::Webview, code: String) -> Result<Value, String> {
    let Caller::Remote(page) = caller(&app, &webview) else {
        return Err("Only a server's own page can link ARK Studio to it.".into());
    };
    let entry = servers::saved_for_page(&settings_snapshot(&app).remote_history, &page)
        .ok_or("This server is not saved in ARK Studio. Add it under Settings > Servers first.")?;
    if !links::valid_code(&code) {
        return Err("Not a link code.".into());
    }
    let (status, reply) = server_call(
        &app,
        &entry.url,
        reqwest::Method::POST,
        "api/mobile/pair/claim",
        None,
        Some(json!({ "code": code.trim() })),
    )
    .await?;
    if status != 200 {
        return Err(server_message(&reply, "The server refused the link code."));
    }
    let link = links::parse_claim(&reply, now_secs())?;
    let user = link.user.clone();
    let old = {
        let st = state(&app);
        let mut l = st.links.lock().unwrap();
        let old = l.get(&entry.url).cloned();
        l.set(&entry.url, link)?;
        l.save()?;
        old
    };
    // Linking again replaces the old device session.
    if let Some(old) = old {
        let _ = server_call(&app, &entry.url, reqwest::Method::DELETE, "api/mobile/session", Some(&old.token), None).await;
    }
    Ok(json!({ "linked": true, "user": user, "server": entry.label }))
}

fn project_rows(v: &Value) -> Vec<Value> {
    let text = |p: &Value, k: &str| p.get(k).and_then(Value::as_str).unwrap_or_default().chars().take(600).collect::<String>();
    v.get("projects")
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .take(500)
                .filter(|p| p.get("id").and_then(Value::as_str).is_some_and(|id| !id.is_empty()))
                .map(|p| {
                    json!({
                        "id": text(p, "id"),
                        "name": text(p, "name"),
                        "description": text(p, "description"),
                        "updated": text(p, "updated"),
                        "permission": text(p, "permission"),
                    })
                })
                .collect()
        })
        .unwrap_or_default()
}

/// A linked server's projects (for the local page's switcher). Not linked,
/// expired (the link was revoked: it is dropped), offline or an older server
/// are reported as such; nothing blocks.
#[tauri::command]
async fn server_projects(app: AppHandle, webview: tauri::Webview, url: String) -> Result<Value, String> {
    require_hub(&app, &webview)?;
    let entry = servers::find_saved(&settings_snapshot(&app).remote_history, &url).ok_or("This server is not in the saved list.")?;
    let link = state(&app).links.lock().unwrap().get(&entry.url).cloned();
    let Some(link) = link else {
        return Ok(json!({ "url": entry.url, "linked": false }));
    };
    match server_call(&app, &entry.url, reqwest::Method::GET, "api/desktop/projects", Some(&link.token), None).await {
        Ok((200, v)) => Ok(json!({
            "url": entry.url,
            "linked": true,
            "reachable": true,
            "user": v.get("user").cloned().unwrap_or(Value::Null),
            "canCreate": v.get("can_create").and_then(Value::as_bool).unwrap_or(false),
            "projects": project_rows(&v),
        })),
        Ok((401, _)) => {
            take_link(&app, &entry.url);
            Ok(json!({ "url": entry.url, "linked": false, "expired": true, "reachable": true }))
        }
        Ok((404, _)) => Ok(json!({ "url": entry.url, "linked": true, "reachable": true, "unsupported": true })),
        Ok((_, v)) => Ok(json!({ "url": entry.url, "linked": true, "reachable": true,
            "error": server_message(&v, "The server could not list its projects.") })),
        Err(e) => Ok(json!({ "url": entry.url, "linked": true, "reachable": false, "error": e })),
    }
}

/// Creates a project on a linked server (the server checks the account's
/// role). Returns {project: {id, name, ...}, url}.
#[tauri::command]
async fn create_server_project(
    app: AppHandle,
    webview: tauri::Webview,
    url: String,
    name: String,
    description: Option<String>,
    template: Option<String>,
) -> Result<Value, String> {
    require_hub(&app, &webview)?;
    let entry = servers::find_saved(&settings_snapshot(&app).remote_history, &url).ok_or("This server is not in the saved list.")?;
    let link = state(&app).links.lock().unwrap().get(&entry.url).cloned();
    let link = link.ok_or("ARK Studio is not linked to this server.")?;
    let name = name.trim().chars().take(200).collect::<String>();
    if name.is_empty() {
        return Err("Project name is required".into());
    }
    let template = match template.as_deref() {
        Some("paper") => "paper",
        _ => "blank",
    };
    let body = json!({
        "name": name,
        "description": description.unwrap_or_default().chars().take(600).collect::<String>(),
        "template": template,
    });
    let (status, v) =
        server_call(&app, &entry.url, reqwest::Method::POST, "api/desktop/projects", Some(&link.token), Some(body)).await?;
    match status {
        200 => {
            let project = v.get("project").cloned().unwrap_or(Value::Null);
            let row = project_rows(&json!({ "projects": [project] })).into_iter().next();
            row.map(|p| json!({ "project": p, "url": entry.url })).ok_or_else(|| "The server sent no project.".to_string())
        }
        401 => {
            take_link(&app, &entry.url);
            Err(format!("The link to {} has expired or was revoked. Link this desktop again.", entry.label))
        }
        _ => Err(server_message(&v, "The server could not create the project.")),
    }
}

/// Unlinks a server: forgets its token and ends the device session there.
#[tauri::command]
async fn unlink_server(app: AppHandle, webview: tauri::Webview, url: String) -> Result<(), String> {
    require_hub(&app, &webview)?;
    let key = settings::normalize_remote(&url)?.to_string();
    unlink(&app, &key).await;
    Ok(())
}

/// Extensions that must never be launched on a remote page's request; such
/// files are shown in the file manager instead.
const NEVER_LAUNCH: &[&str] = &[
    "exe", "com", "bat", "cmd", "ps1", "psm1", "vbs", "vbe", "js", "jse", "wsf", "wsh", "msi", "msp", "scr", "lnk",
    "pif", "reg", "cpl", "hta", "jar", "app", "command", "sh", "bash", "zsh", "run", "appimage", "desktop", "py", "pl",
];

/// The local-open bridge for ARK pages: opens a file or folder on this
/// computer (a Markdown `file:///` link of a remote ARK). Inside the folders
/// listed in Settings it opens directly; anywhere else it asks first.
#[tauri::command]
async fn open_local_path(app: AppHandle, webview: tauri::Webview, path: String) -> Result<Value, String> {
    let page = webview.url().map_err(|e| e.to_string())?;
    let origin = page.origin().ascii_serialization();
    if !state(&app).granted.lock().unwrap().contains(&origin) {
        return Err("This page may not open local files.".into());
    }
    let raw = path.trim();
    let raw = if raw.to_ascii_lowercase().starts_with("file:") {
        Url::parse(raw).ok().and_then(|u| u.to_file_path().ok()).ok_or("Not a local file link")?
    } else {
        PathBuf::from(raw)
    };
    if !raw.is_absolute() {
        return Err("Only absolute paths can be opened.".into());
    }
    let target = raw.canonicalize().map_err(|_| "Path does not exist on this computer".to_string())?;
    let roots: Vec<PathBuf> = state(&app)
        .settings
        .lock()
        .unwrap()
        .local_open_roots
        .iter()
        .filter_map(|r| PathBuf::from(r).canonicalize().ok())
        .collect();
    let inside = roots.iter().any(|r| target.starts_with(r));
    if !inside {
        let shown = target.to_string_lossy().trim_start_matches(r"\\?\").to_string();
        let answer = app
            .dialog()
            .message(format!(
                "{origin} wants to open this on your computer:\n\n{shown}\n\nOpen it? (Folders you trust can be added in ARK Studio settings.)"
            ))
            .title("ARK Studio")
            .kind(MessageDialogKind::Warning)
            .buttons(MessageDialogButtons::OkCancelCustom("Open".into(), "Cancel".into()))
            .blocking_show_with_result();
        let ok = matches!(answer, MessageDialogResult::Ok) || matches!(answer, MessageDialogResult::Custom(ref s) if s == "Open");
        if !ok {
            return Err("Opening was cancelled.".into());
        }
    }
    let ext = target.extension().map(|e| e.to_string_lossy().to_ascii_lowercase()).unwrap_or_default();
    let opener = app.opener();
    if target.is_file() && NEVER_LAUNCH.contains(&ext.as_str()) {
        opener.reveal_item_in_dir(&target).map_err(|e| e.to_string())?;
        return Ok(json!({ "ok": true, "mode": "revealed", "path": target }));
    }
    opener.open_path(target.to_string_lossy(), None::<&str>).map_err(|e| e.to_string())?;
    Ok(json!({ "ok": true, "mode": "desktop", "path": target }))
}

/// Largest image the copy bridge accepts (PNG bytes).
const COPY_IMAGE_MAX_BYTES: usize = 64 * 1024 * 1024;

/// The image copy bridge for ARK pages (web/js/image-copy.js): puts a PNG,
/// sent as the raw IPC body, on the system clipboard.  Used when the page may
/// not write images itself (an http:// server is no secure context, or the
/// WebView refuses the async Clipboard API).
#[tauri::command]
fn copy_image_png(request: tauri::ipc::Request<'_>) -> Result<(), String> {
    let tauri::ipc::InvokeBody::Raw(bytes) = request.body() else {
        return Err("Expected PNG bytes.".into());
    };
    if bytes.is_empty() || bytes.len() > COPY_IMAGE_MAX_BYTES {
        return Err("The image is empty or too large to copy.".into());
    }
    let image = tauri::image::Image::from_bytes(bytes).map_err(|e| format!("Not a PNG image: {e}"))?;
    let data = arboard::ImageData {
        width: image.width() as usize,
        height: image.height() as usize,
        bytes: std::borrow::Cow::Borrowed(image.rgba()),
    };
    let mut clipboard = arboard::Clipboard::new().map_err(|e| format!("Clipboard unavailable: {e}"))?;
    clipboard.set_image(data).map_err(|e| format!("Could not copy the image: {e}"))
}

// ---------------------------------------------------------------- location sizes

/// This computer's device id (the X-ARK-Host an agent here sends): ARK
/// pages match it against the devices of a project's setup.
#[tauri::command]
fn device_id(app: AppHandle, webview: tauri::Webview) -> Result<Value, String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    measure::device_id()
        .map(|id| json!({ "id": id, "method": "machine-id" }))
        .ok_or_else(|| "This computer has no machine id.".into())
}

static MEASURING: AtomicBool = AtomicBool::new(false);

/// Sizes of this computer's registered resource paths (measure.rs): one
/// call at a time, at most measure::MAX_PATHS paths, sizes and counts only.
#[tauri::command]
async fn measure_paths(app: AppHandle, webview: tauri::Webview, paths: Vec<String>) -> Result<Vec<measure::Stat>, String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    if paths.len() > measure::MAX_PATHS {
        return Err(format!("At most {} paths per call.", measure::MAX_PATHS));
    }
    if MEASURING.swap(true, Ordering::SeqCst) {
        return Err("A measurement is already running.".into());
    }
    let result = tauri::async_runtime::spawn_blocking(move || measure::measure_all(paths)).await;
    MEASURING.store(false, Ordering::SeqCst);
    result.map_err(|e| e.to_string())
}

// ---------------------------------------------------------------- remote open

/// "Open SSH terminal" (remote_open.rs): a terminal window running ssh to
/// the device, in the location's folder. Parts only, checked there.
#[tauri::command]
fn open_ssh_terminal(app: AppHandle, webview: tauri::Webview, target: remote_open::SshTarget, path: Option<String>) -> Result<(), String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    let args = remote_open::ssh_args(&target, path.as_deref())?;
    let script = remote_open::script_path();
    let launch = remote_open::terminal_launch(remote_open::Os::current(), &args, &remote_open::find_program, &script)?;
    remote_open::spawn(&launch, &script)
}

/// Which of `names` are Host aliases (or HostNames) in ~/.ssh/config.
#[tauri::command]
fn ssh_config_match(app: AppHandle, webview: tauri::Webview, names: Vec<String>) -> Result<Vec<remote_open::ConfigMatch>, String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    let hosts = remote_open::parse_ssh_config(&remote_open::read_user_config());
    Ok(remote_open::match_config(&hosts, &names))
}

/// Opens a VS Code / Cursor folder link (vscode://file/..., vscode://vscode-remote/ssh-remote+...).
#[tauri::command]
fn open_editor_url(app: AppHandle, webview: tauri::Webview, url: String) -> Result<(), String> {
    if matches!(caller(&app, &webview), Caller::Other) {
        return Err("Not an ARK page of ARK Studio.".into());
    }
    let target = remote_open::check_editor_url(&url)?;
    app.opener().open_url(target, None::<&str>).map_err(|e| format!("Could not open the editor: {e}"))
}

// ---------------------------------------------------------------- tray

fn open_in_browser(app: &AppHandle) {
    let current = state(app).current.lock().unwrap().clone();
    let target = match current {
        Current::Local => state(app).sidecar.lock().unwrap().as_ref().map(|s| s.claim_url("/")),
        Current::Remote(u) => Some(u),
        _ => None,
    };
    if let Some(t) = target {
        let _ = app.opener().open_url(t, None::<&str>);
    }
}

const SERVER_ITEM: &str = "server:";

/// The system UI language is Chinese (the native tray menu's language; the
/// bundled pages follow navigator.language, which is the same setting).
fn system_zh() -> bool {
    #[cfg(windows)]
    {
        // PRIMARYLANGID(GetUserDefaultUILanguage()) == LANG_CHINESE
        let lang = unsafe { windows_sys::Win32::Globalization::GetUserDefaultUILanguage() };
        lang & 0x3ff == 0x04
    }
    #[cfg(not(windows))]
    {
        ["LC_ALL", "LC_MESSAGES", "LANG"]
            .iter()
            .find_map(|k| std::env::var(k).ok().filter(|v| !v.is_empty()))
            .is_some_and(|v| v.starts_with("zh"))
    }
}

/// Native tray menu strings (English, Simplified Chinese), worded like ui/i18n.js.
fn tray_text(key: &str) -> &'static str {
    let zh = system_zh();
    match key {
        "open" => if zh { "打开 ARK Studio" } else { "Open ARK Studio" },
        "servers" => if zh { "服务器" } else { "Servers" },
        "connect" => if zh { "连接到服务器…" } else { "Connect to a server…" },
        "browser" => if zh { "在浏览器中打开" } else { "Open in browser" },
        "start" => if zh { "启动本机服务器" } else { "Start local server" },
        "stop" => if zh { "停止本机服务器" } else { "Stop local server" },
        "settings" => if zh { "设置…" } else { "Settings…" },
        "data" => if zh { "打开数据文件夹" } else { "Open data folder" },
        "log" => if zh { "查看服务器日志" } else { "Show server log" },
        "quit" => if zh { "退出 ARK Studio" } else { "Quit ARK Studio" },
        "update" => if zh { "更新到 " } else { "Update to " },
        _ => "",
    }
}

fn build_menu(app: &AppHandle) -> tauri::Result<(Menu<tauri::Wry>, TrayItems)> {
    let saved = servers::list(&settings_snapshot(app).remote_history);
    let update = state(app).update.lock().unwrap().clone();
    let header = MenuItem::with_id(app, "header", format!("ARK Studio {}", app.package_info().version), false, None::<&str>)?;
    let open = MenuItem::with_id(app, "open", tray_text("open"), true, None::<&str>)?;
    let servers_menu = Submenu::with_id(app, "servers", tray_text("servers"), true)?;
    for e in &saved {
        servers_menu.append(&MenuItem::with_id(app, format!("{SERVER_ITEM}{}", e.url), &e.label, true, None::<&str>)?)?;
    }
    if !saved.is_empty() {
        servers_menu.append(&PredefinedMenuItem::separator(app)?)?;
    }
    servers_menu.append(&MenuItem::with_id(app, "connect", tray_text("connect"), true, None::<&str>)?)?;
    let browser = MenuItem::with_id(app, "browser", tray_text("browser"), true, None::<&str>)?;
    let start = MenuItem::with_id(app, "start", tray_text("start"), true, None::<&str>)?;
    let stop = MenuItem::with_id(app, "stop", tray_text("stop"), false, None::<&str>)?;
    let settings_item = MenuItem::with_id(app, "settings", tray_text("settings"), true, None::<&str>)?;
    let data = MenuItem::with_id(app, "data", tray_text("data"), true, None::<&str>)?;
    let log = MenuItem::with_id(app, "log", tray_text("log"), true, None::<&str>)?;
    let quit_item = MenuItem::with_id(app, "quit", tray_text("quit"), true, None::<&str>)?;
    let sep = || PredefinedMenuItem::separator(app);
    let menu = Menu::with_items(
        app,
        &[
            &header,
            &sep()?,
            &open,
            &sep()?,
            &servers_menu,
            &browser,
            &sep()?,
            &start,
            &stop,
            &sep()?,
            &settings_item,
            &data,
            &log,
            &sep()?,
            &quit_item,
        ],
    )?;
    if let Some(v) = update {
        let item = MenuItem::with_id(app, "update", format!("{}{v}…", tray_text("update")), true, None::<&str>)?;
        menu.insert(&item, 3)?;
    }
    Ok((menu, TrayItems { start, stop }))
}

/// Rebuilds the tray menu (the saved servers changed).
fn refresh_tray_menu(app: &AppHandle) {
    let Some(tray) = app.tray_by_id(TRAY) else { return };
    if !NATIVE_TRAY_MENU.load(Ordering::SeqCst) {
        // The ARK tray window reads the state each time it opens.
        return;
    }
    match build_menu(app) {
        Ok((menu, items)) => {
            let _ = tray.set_menu(Some(menu));
            *state(app).tray_items.lock().unwrap() = Some(items);
            refresh_tray(app);
        }
        Err(e) => eprintln!("ark-studio: tray menu: {e}"),
    }
}

fn on_tray_menu(app: &AppHandle, id: &str) {
    let handle = app.clone();
    if let Some(url) = id.strip_prefix(SERVER_ITEM) {
        let url = url.to_string();
        tauri::async_runtime::spawn(async move {
            match open_saved(&handle, &url, Target::Home, false).await {
                Ok(v) if v["unreachable"].as_bool().unwrap_or(false) => {
                    show_status(&handle, "remote", &url, &format!("{url} did not answer."));
                }
                Ok(_) => {}
                Err(e) => show_status(&handle, "remote", &url, &e),
            }
        });
        return;
    }
    // Menu events arrive on the main thread: whatever may create a window
    // runs elsewhere (off_main).
    match id {
        "open" => off_main(app, show_app),
        "start" => off_main(app, go_local),
        "connect" => off_main(app, |a| show_settings(a, None, Some("connect"))),
        "update" => off_main(app, |a| show_settings(a, None, Some("about"))),
        "browser" => open_in_browser(app),
        "stop" => {
            std::thread::spawn(move || {
                stop_sidecar(&handle);
                if *state(&handle).current.lock().unwrap() == Current::Local {
                    show_status(&handle, "local", "", "The local server was stopped.");
                }
            });
        }
        "data" => {
            let _ = app.opener().open_path(paths::data_dir().to_string_lossy(), None::<&str>);
        }
        "log" => {
            let _ = app.opener().reveal_item_in_dir(paths::server_log());
        }
        "settings" => off_main(app, |a| show_settings(a, None, None)),
        "quit" => {
            std::thread::spawn(move || quit(&handle));
        }
        _ => {}
    }
}

/// The tray uses the native menu (macOS, Linux; Windows when the ARK tray
/// window could not be built). Windows starts with the ARK window.
static NATIVE_TRAY_MENU: AtomicBool = AtomicBool::new(!cfg!(windows));

/// Right click on Windows: the ARK tray window (tray_popup); if it cannot be
/// built, the native menu takes over from the next click on.
fn open_tray_menu(app: &AppHandle, at: tauri::PhysicalPosition<f64>) {
    if NATIVE_TRAY_MENU.load(Ordering::SeqCst) {
        return;
    }
    if let Err(e) = tray_popup::open(app, at) {
        eprintln!("ark-studio: tray window: {e}; using the native menu");
        NATIVE_TRAY_MENU.store(true, Ordering::SeqCst);
        refresh_tray_menu(app);
    }
}

fn build_tray(app: &AppHandle) -> tauri::Result<()> {
    let mut builder = TrayIconBuilder::with_id(TRAY)
        .tooltip("ARK Studio")
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| on_tray_menu(app, event.id.as_ref()))
        .on_tray_icon_event(|tray, event| match event {
            TrayIconEvent::Click { button: MouseButton::Left, button_state: MouseButtonState::Up, .. } => {
                off_main(tray.app_handle(), show_app);
            }
            TrayIconEvent::Click { button: MouseButton::Right, button_state: MouseButtonState::Up, position, .. } => {
                off_main(tray.app_handle(), move |a| open_tray_menu(a, position));
            }
            _ => {}
        });
    if NATIVE_TRAY_MENU.load(Ordering::SeqCst) {
        let (menu, items) = build_menu(app)?;
        *state(app).tray_items.lock().unwrap() = Some(items);
        builder = builder.menu(&menu);
    }
    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    }
    builder.build(app)?;
    Ok(())
}

/// Watches the sidecar; if it dies while shown, says so instead of leaving
/// the window on a dead page.
fn spawn_monitor(app: AppHandle) {
    std::thread::spawn(move || loop {
        std::thread::sleep(Duration::from_secs(2));
        if state(&app).quitting.load(Ordering::SeqCst) {
            return;
        }
        let died = {
            let st = state(&app);
            let mut guard = st.sidecar.lock().unwrap();
            if guard.as_mut().is_some_and(|s| !s.alive()) {
                guard.take();
                true
            } else {
                false
            }
        };
        if died {
            refresh_tray(&app);
            if *state(&app).current.lock().unwrap() == Current::Local {
                let msg = format!("The local ARK server stopped unexpectedly.\n\n{}", sidecar::log_tail(20));
                show_status(&app, "local", "", &msg);
            }
        }
    });
}

/// Every start opens the local Home (the one default page): the local server
/// starts in the background with the saved folder and port.
fn startup(app: AppHandle) {
    go_local(&app);
}

/// Links of servers that are no longer saved (desktop.json edited by hand)
/// are dropped and their device sessions ended (best effort).
fn drop_stale_links(app: &AppHandle) {
    let stale = {
        let st = state(app);
        let history = st.settings.lock().unwrap().remote_history.clone();
        let mut l = st.links.lock().unwrap();
        let gone = l.retain_saved(&history);
        if !gone.is_empty() {
            let _ = l.save();
        }
        gone
    };
    for (server, link) in stale {
        let handle = app.clone();
        tauri::async_runtime::spawn(async move {
            let _ = server_call(&handle, &server, reqwest::Method::DELETE, "api/mobile/session", Some(&link.token), None).await;
        });
    }
}

/// Stops every ark-server running from this executable's folder (installer
/// hooks: an upgrade must not leave a stale sidecar behind).
pub fn stop_installed_sidecars() {
    sidecar::kill_in_app_dir();
}

pub fn run() {
    // HTTPS to remote servers (reqwest over rustls) needs a crypto provider.
    let _ = rustls::crypto::ring::default_provider().install_default();
    // First start: create the default project folder (next to the app for
    // the Windows installer / portable zip, else in the per-user data dir).
    let _ = std::fs::create_dir_all(paths::default_root());
    sidecar::kill_orphan();
    let context = tauri::generate_context!();
    let config_key = context.config().plugins.0.get("updater").and_then(|u| u.get("pubkey")).and_then(Value::as_str);
    let _ = UPDATER_KEY.set(pick_pubkey(UPDATER_PUBKEY, config_key));
    let mut builder = tauri::Builder::default()
        // A second start brings this one forward (from the main thread: off_main).
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| off_main(app, show_app)))
        .plugin(
            tauri_plugin_window_state::Builder::default()
                .with_denylist(&[SETTINGS])
                .with_state_flags(
                    // Decorations are ours to decide (borderless), never restored.
                    tauri_plugin_window_state::StateFlags::all()
                        & !tauri_plugin_window_state::StateFlags::VISIBLE
                        & !tauri_plugin_window_state::StateFlags::DECORATIONS,
                )
                .build(),
        )
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_notification::init())
        // The plugin's own "open links on click" script calls the IPC, which
        // the ARK page (served by the local or a remote server, not the app)
        // may not use: the click was swallowed and nothing opened. Without it,
        // target=_blank links reach on_new_window, which opens the browser.
        .plugin(tauri_plugin_opener::Builder::new().open_js_links_on_click(false).build());
    if let Some(key) = updater_key() {
        builder = builder.plugin(tauri_plugin_updater::Builder::new().pubkey(key).build());
    }
    let app = builder
        .manage(AppState {
            settings: Mutex::new(Settings::load()),
            links: Mutex::new(Links::load()),
            ..Default::default()
        })
        .invoke_handler(tauri::generate_handler![
            get_state,
            pick_folder,
            connect_local,
            connect_remote,
            save_settings,
            forget_remote,
            open_data_dir,
            open_log,
            stop_local,
            close_window,
            check_update,
            open_releases,
            desktop_context,
            go_local_home,
            desktop_servers,
            server_status,
            open_server,
            open_settings,
            link_server,
            server_projects,
            create_server_project,
            unlink_server,
            open_local_path,
            update_state,
            set_update_auto_check,
            dismiss_update,
            dismiss_update_notice,
            open_app_settings,
            open_local_settings,
            copy_image_png,
            device_id,
            measure_paths,
            open_ssh_terminal,
            ssh_config_match,
            open_editor_url,
            show_download,
            choose_project_folder,
            tray_menu_ready,
            tray_menu_action,
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            drop_stale_links(&handle);
            build_tray(&handle)?;
            note_updated(&handle);
            spawn_monitor(handle.clone());
            spawn_update_check(&handle);
            startup(handle);
            Ok(())
        })
        .on_window_event(|window, event| {
            if window.label() == tray_popup::LABEL {
                // Clicking elsewhere (focus lost) or Alt+F4 only hides the tray window.
                match event {
                    WindowEvent::Focused(false) => tray_popup::hide(window.app_handle()),
                    WindowEvent::CloseRequested { api, .. } => {
                        api.prevent_close();
                        tray_popup::hide(window.app_handle());
                    }
                    _ => {}
                }
                return;
            }
            if let WindowEvent::CloseRequested { api, .. } = event {
                let app = window.app_handle();
                if !state(app).quitting.load(Ordering::SeqCst) {
                    api.prevent_close();
                    close_requested(app, window.label());
                }
            }
        })
        .build(context)
        .expect("error while building ARK Studio");
    app.run(|app, event| match event {
        RunEvent::ExitRequested { api, code, .. } => {
            // Hidden windows keep the app alive in the tray; only an explicit
            // quit (code set) ends it.
            if code.is_none() && !state(app).quitting.load(Ordering::SeqCst) {
                api.prevent_exit();
            }
        }
        RunEvent::Exit => stop_sidecar(app),
        _ => {}
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn updater_key_prefers_the_build_variable_then_the_config() {
        assert_eq!(pick_pubkey(Some(" env "), Some("conf")).as_deref(), Some("env"));
        assert_eq!(pick_pubkey(Some(""), Some(" conf ")).as_deref(), Some("conf"));
        assert_eq!(pick_pubkey(None, Some("conf")).as_deref(), Some("conf"));
        assert_eq!(pick_pubkey(Some("  "), Some("")), None);
        assert_eq!(pick_pubkey(None, None), None);
    }

    #[test]
    fn the_committed_config_carries_the_updater_key_and_endpoint() {
        let conf: Value = serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        let updater = &conf["plugins"]["updater"];
        assert!(updater["pubkey"].as_str().is_some_and(|k| !k.trim().is_empty()));
        let endpoints: Vec<&str> = updater["endpoints"].as_array().unwrap().iter().filter_map(Value::as_str).collect();
        // The latest release first; when it has no latest.json (a patch
        // release published without update bundles) the fixed `updater`
        // release (scripts/release/updater-mirror.mjs) answers.
        assert_eq!(
            endpoints,
            vec![format!("{RELEASES_URL}/latest/download/latest.json"), format!("{RELEASES_URL}/download/updater/latest.json")]
        );
    }

    #[test]
    fn folder_picker_starts_at_the_nearest_existing_folder() {
        let base = std::env::temp_dir().join(format!("ark-picker-test-{}", std::process::id()));
        let fallback = base.join("projects");
        std::fs::create_dir_all(&fallback).unwrap();
        let deep = base.join("missing").join("deeper");
        assert_eq!(picker_start(Some(deep.to_str().unwrap()), &fallback), Some(base.clone()));
        assert_eq!(picker_start(Some(fallback.to_str().unwrap()), &base), Some(fallback.clone()));
        assert_eq!(picker_start(None, &fallback), Some(fallback.clone()));
        assert_eq!(picker_start(Some("  "), &fallback), Some(fallback.clone()));
        assert_eq!(picker_start(Some("relative/dir"), &fallback), Some(fallback.clone()));
        assert_eq!(picker_start(None, &base.join("gone")), None);
        std::fs::remove_dir_all(&base).unwrap();
    }

    #[test]
    fn release_pages() {
        assert_eq!(release_page(None), format!("{RELEASES_URL}/latest"));
        assert_eq!(release_page(Some("2.1.0")), format!("{RELEASES_URL}/tag/v2.1.0"));
        assert_eq!(release_page(Some("v2.1.0-rc.1")), format!("{RELEASES_URL}/tag/v2.1.0-rc.1"));
        // anything else is not a version: the latest release
        assert_eq!(release_page(Some("../../evil")), format!("{RELEASES_URL}/latest"));
        assert_eq!(release_page(Some(" ")), format!("{RELEASES_URL}/latest"));
    }

    #[test]
    fn only_installer_builds_install_updates() {
        for kind in ["nsis", "msi", "deb", "rpm", "appimage", "app"] {
            assert!(self_install(kind), "{kind}");
        }
        assert!(!self_install("portable"));
        // a test binary is no installer build
        if !cfg!(target_os = "macos") {
            assert_eq!(install_kind(), "portable");
        }
    }
}
