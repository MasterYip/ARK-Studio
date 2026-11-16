//! ARK Studio desktop shell (Tauri 2).
//!
//! Two borderless windows:
//!
//! * "chooser": the bundled connection chooser and settings (`ui/index.html`),
//!   a compact dialog (transparent with a soft shadow on Windows/Linux).
//! * "main": always a server's own URL (or the bundled offline page):
//!   - Local: the bundled `ark-server` runs as a private loopback sidecar
//!     (see `sidecar.rs`); the window opens its one-time claim URL.
//!   - Remote: the window opens the remote ARK address directly.
//!
//! Window chrome: on Windows and Linux both windows are undecorated; ARK draws
//! the window controls (src/shell.js) and the page marks its drag regions.
//! On macOS the windows keep the native traffic lights over the page
//! (Overlay title bar, hidden title).
//!
//! The tray menu switches servers, starts/stops the local server, opens the
//! current server in the system browser and quits (which stops the sidecar).
//! Closing a window quits, or hides to the tray when that setting is on.

mod paths;
mod settings;
mod sidecar;

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

use serde_json::{json, Value};
use tauri::ipc::CapabilityBuilder;
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::webview::NewWindowResponse;
use tauri::{AppHandle, Manager, RunEvent, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind, MessageDialogResult};
use tauri_plugin_opener::OpenerExt;

use settings::Settings;
use sidecar::Sidecar;

const MAIN: &str = "main";
const CHOOSER: &str = "chooser";
const READY_TIMEOUT: Duration = Duration::from_secs(45);
/// Set at build time by CI when the updater signing key exists.
const UPDATER_PUBKEY: Option<&str> = option_env!("ARK_UPDATER_PUBKEY");
/// Window permissions every page of the app may use (controls, dragging).
const WINDOW_PERMISSIONS: &[&str] = &[
    "core:window:allow-minimize",
    "core:window:allow-toggle-maximize",
    "core:window:allow-internal-toggle-maximize",
    "core:window:allow-is-maximized",
    "core:window:allow-start-dragging",
    "allow-close-window",
];

#[derive(Clone, Debug, Default, PartialEq)]
enum Current {
    #[default]
    None,
    Local,
    Remote(String),
    Offline,
}

#[derive(Default)]
struct AppState {
    sidecar: Mutex<Option<Sidecar>>,
    current: Mutex<Current>,
    granted: Mutex<HashSet<String>>,
    settings: Mutex<Settings>,
    tray_items: Mutex<Option<TrayItems>>,
    quitting: AtomicBool,
}

struct TrayItems {
    start: MenuItem<tauri::Wry>,
    stop: MenuItem<tauri::Wry>,
}

fn updater_enabled() -> bool {
    UPDATER_PUBKEY.is_some_and(|k| !k.trim().is_empty())
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

/// Origin of the bundled pages (`app.windows[].useHttpsScheme` is false).
fn app_origin() -> Url {
    let base = if cfg!(windows) { "http://tauri.localhost/" } else { "tauri://localhost/" };
    Url::parse(base).expect("static url")
}

/// A page of the bundled UI (`index.html`, `offline.html?...`).
fn app_page(page: &str) -> Url {
    app_origin().join(page).expect("bundled page url")
}

fn focus(w: &WebviewWindow) {
    let _ = w.unminimize();
    let _ = w.show();
    let _ = w.set_focus();
}

// ---------------------------------------------------------------- windows

/// Chrome configuration handed to src/shell.js for one window.
fn shell_script(app: &AppHandle, label: &str) -> String {
    let (chrome, controls, inset): (&str, Vec<&str>, u32) = if cfg!(target_os = "macos") {
        ("overlay", vec![], 80)
    } else if label == CHOOSER {
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
    let builder = WebviewWindowBuilder::new(app, label, url)
        .title("ARK Studio")
        .initialization_script(shell_script(app, label))
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

fn chooser_window(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    if let Some(w) = app.get_webview_window(CHOOSER) {
        return Ok(w);
    }
    // Created blank and then navigated (show_chooser): the shell script is
    // registered before the first real page loads.
    let builder = base_builder(app, CHOOSER, WebviewUrl::External("about:blank".parse().expect("url")))
        .resizable(false)
        .maximizable(false)
        .center();
    // Windows/Linux: a transparent window around an ARK card with its own
    // rounded corners and soft 30 px shadow in a 28 px margin (like the
    // former Python chooser).
    #[cfg(not(target_os = "macos"))]
    let builder = builder.inner_size(600.0, 760.0).transparent(true).shadow(false);
    #[cfg(target_os = "macos")]
    let builder = builder.inner_size(560.0, 740.0);
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
            // Links that ask for a new window open in the system browser; the
            // local server needs a fresh claim there.
            let target = {
                let st = state(&handle);
                let guard = st.sidecar.lock().unwrap();
                match guard.as_ref() {
                    Some(s) if url.origin().ascii_serialization() == s.origin() => {
                        let mut path = url.path().to_string();
                        if let Some(q) = url.query() {
                            path = format!("{path}?{q}");
                        }
                        s.claim_url(&path)
                    }
                    _ => url.to_string(),
                }
            };
            if target.starts_with("http://") || target.starts_with("https://") || target.starts_with("mailto:") {
                let _ = handle.opener().open_url(target, None::<&str>);
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

/// Shows `url` in the main window and hides the chooser.
fn open_main(app: &AppHandle, url: Url) {
    match main_window(app) {
        Ok(w) => {
            let _ = w.navigate(url);
            focus(&w);
        }
        Err(e) => eprintln!("ark-studio: main window: {e}"),
    }
    if let Some(c) = app.get_webview_window(CHOOSER) {
        let _ = c.hide();
    }
}

fn show_chooser(app: &AppHandle, notice: Option<&str>, view: Option<&str>) {
    let mut q = url::form_urlencoded::Serializer::new(String::new());
    if let Some(n) = notice {
        q.append_pair("notice", n);
    }
    if let Some(v) = view {
        q.append_pair("view", v);
    }
    let q = q.finish();
    let page = if q.is_empty() { "index.html".to_string() } else { format!("index.html?{q}") };
    match chooser_window(app) {
        Ok(w) => {
            let _ = w.navigate(app_page(&page));
            focus(&w);
        }
        Err(e) => eprintln!("ark-studio: chooser window: {e}"),
    }
}

/// Brings the app forward: the main window when it shows a server, else the chooser.
fn show_app(app: &AppHandle) {
    let current = state(app).current.lock().unwrap().clone();
    match (current, app.get_webview_window(MAIN)) {
        (Current::None, _) | (_, None) => show_chooser(app, None, None),
        (_, Some(w)) => focus(&w),
    }
}

fn show_offline(app: &AppHandle, kind: &str, target: &str, message: &str) {
    let q: String = url::form_urlencoded::Serializer::new(String::new())
        .append_pair("kind", kind)
        .append_pair("url", target)
        .append_pair("message", message)
        .finish();
    *state(app).current.lock().unwrap() = Current::Offline;
    open_main(app, app_page(&format!("offline.html?{q}")));
}

/// Lets pages of `origin` use the window controls and the local-open
/// bridge, and nothing else.
fn grant_server_origin(app: &AppHandle, origin: &str) {
    let st = state(app);
    let mut granted = st.granted.lock().unwrap();
    if granted.contains(origin) {
        return;
    }
    let id = format!("ark-server-{}", granted.len() + 1);
    let mut cap = CapabilityBuilder::new(id).remote(format!("{origin}/*")).window(MAIN).permission("allow-open-local-path");
    for p in WINDOW_PERMISSIONS {
        cap = cap.permission(*p);
    }
    match app.add_capability(cap) {
        Ok(()) => {
            granted.insert(origin.to_string());
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

/// Starts (or reuses) the local server for `root` and opens it.
fn start_local(app: &AppHandle, root: PathBuf, port: u16) -> Result<Value, String> {
    {
        let st = state(app);
        let mut guard = st.sidecar.lock().unwrap();
        let reuse = guard.as_mut().is_some_and(|s| s.alive() && s.root == root);
        if !reuse {
            if let Some(mut old) = guard.take() {
                old.stop();
            }
            let mut fresh = sidecar::start(&root, port)?;
            fresh.wait_ready(READY_TIMEOUT)?;
            *guard = Some(fresh);
        }
    }
    let (origin, claim, actual, requested) = {
        let st = state(app);
        let guard = st.sidecar.lock().unwrap();
        let s = guard.as_ref().ok_or("ark-server is not running")?;
        (s.origin(), s.claim_url("/"), s.port, s.requested_port)
    };
    grant_server_origin(app, &origin);
    *state(app).current.lock().unwrap() = Current::Local;
    refresh_tray(app);
    open_main(app, Url::parse(&claim).map_err(|e| e.to_string())?);
    Ok(json!({ "port": actual, "requestedPort": requested, "fallback": requested != 0 && actual != requested }))
}

fn open_remote(app: &AppHandle, url: &Url) {
    let origin = url.origin().ascii_serialization();
    grant_server_origin(app, &origin);
    *state(app).current.lock().unwrap() = Current::Remote(url.to_string());
    open_main(app, url.clone());
}

fn git_found() -> bool {
    paths::find_in_path("git").is_some()
}

fn quit(app: &AppHandle) {
    state(app).quitting.store(true, Ordering::SeqCst);
    stop_sidecar(app);
    app.exit(0);
}

/// Close button / Alt+F4 policy. The chooser just goes away while ARK is
/// open in the main window; otherwise closing quits, or hides to the tray
/// when "Keep running in the tray" is on.
fn close_requested(app: &AppHandle, label: &str) {
    let main_visible = app.get_webview_window(MAIN).is_some_and(|w| w.is_visible().unwrap_or(false));
    if label == CHOOSER && main_visible {
        if let Some(c) = app.get_webview_window(CHOOSER) {
            let _ = c.hide();
        }
        return;
    }
    if state(app).settings.lock().unwrap().close_to_tray {
        if let Some(w) = app.get_webview_window(label) {
            let _ = w.hide();
        }
        return;
    }
    let handle = app.clone();
    std::thread::spawn(move || quit(&handle));
}

// ---------------------------------------------------------------- commands

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
        "defaultRoot": paths::default_root(),
        "defaultPort": settings::DEFAULT_PORT,
        "dataDir": paths::data_dir(),
        "layout": paths::layout(),
        "logFile": paths::server_log(),
        "gitFound": git_found(),
        "serverFound": sidecar::server_exe().is_ok(),
        "local": local,
        "updater": updater_enabled(),
    })
}

#[tauri::command]
async fn pick_folder(app: AppHandle, current: Option<String>) -> Option<String> {
    let mut dialog = app.dialog().file().set_title("Choose the folder for your ARK projects");
    if let Some(c) = current.filter(|c| Path::new(c).is_dir()) {
        dialog = dialog.set_directory(c);
    }
    dialog.blocking_pick_folder().and_then(|p| p.into_path().ok()).map(|p| p.to_string_lossy().to_string())
}

#[tauri::command]
async fn connect_local(app: AppHandle, root: String, port: u16, auto_connect: bool) -> Result<Value, String> {
    let root_path = if root.trim().is_empty() { paths::default_root() } else { PathBuf::from(root.trim()) };
    if !root_path.is_absolute() {
        return Err("Choose an absolute folder path.".into());
    }
    {
        let st = state(&app);
        let mut s = st.settings.lock().unwrap();
        s.mode = "local".into();
        s.local_root = if root_path == paths::default_root() { String::new() } else { root_path.to_string_lossy().into() };
        s.local_port = port;
        s.auto_connect = auto_connect;
        s.save()?;
    }
    let port = if port == 0 { settings::DEFAULT_PORT } else { port };
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || start_local(&handle, root_path, port))
        .await
        .map_err(|e| e.to_string())?
}

#[tauri::command]
async fn connect_remote(app: AppHandle, url: String, auto_connect: bool) -> Result<String, String> {
    let url = settings::normalize_remote(&url)?;
    {
        let st = state(&app);
        let mut s = st.settings.lock().unwrap();
        s.mode = "remote".into();
        s.auto_connect = auto_connect;
        s.remember_remote(url.as_str());
        s.save()?;
    }
    let handle = app.clone();
    let target = url.clone();
    tauri::async_runtime::spawn_blocking(move || open_remote(&handle, &target)).await.map_err(|e| e.to_string())?;
    Ok(url.to_string())
}

#[tauri::command]
fn save_settings(app: AppHandle, patch: Value) -> Result<Value, String> {
    let st = state(&app);
    let mut s = st.settings.lock().unwrap();
    if let Some(v) = patch.get("autoConnect").and_then(Value::as_bool) {
        s.auto_connect = v;
    }
    if let Some(v) = patch.get("closeToTray").and_then(Value::as_bool) {
        s.close_to_tray = v;
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
    s.save()?;
    Ok(serde_json::to_value(&*s).unwrap_or(Value::Null))
}

#[tauri::command]
fn forget_remote(app: AppHandle, url: String) -> Result<(), String> {
    let st = state(&app);
    let mut s = st.settings.lock().unwrap();
    s.remote_history.retain(|u| *u != url);
    if s.remote_url == url {
        s.remote_url.clear();
    }
    s.save()
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

#[tauri::command]
fn open_chooser(app: AppHandle) {
    show_chooser(&app, None, None);
}

#[tauri::command]
fn close_window(app: AppHandle, window: tauri::Window) {
    close_requested(&app, window.label());
}

#[tauri::command]
async fn check_update(app: AppHandle, install: bool) -> Result<Value, String> {
    if !updater_enabled() {
        return Ok(json!({ "enabled": false }));
    }
    use tauri_plugin_updater::UpdaterExt;
    let updater = app.updater().map_err(|e| e.to_string())?;
    let Some(update) = updater.check().await.map_err(|e| e.to_string())? else {
        return Ok(json!({ "enabled": true, "available": false }));
    };
    let version = update.version.clone();
    if install {
        stop_sidecar(&app);
        update.download_and_install(|_, _| {}, || {}).await.map_err(|e| e.to_string())?;
        app.restart();
    }
    Ok(json!({ "enabled": true, "available": true, "version": version }))
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

fn build_tray(app: &AppHandle) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, "open", "Open ARK Studio", true, None::<&str>)?;
    let switch = MenuItem::with_id(app, "switch", "Switch server…", true, None::<&str>)?;
    let browser = MenuItem::with_id(app, "browser", "Open in browser", true, None::<&str>)?;
    let start = MenuItem::with_id(app, "start", "Start local server", true, None::<&str>)?;
    let stop = MenuItem::with_id(app, "stop", "Stop local server", false, None::<&str>)?;
    let data = MenuItem::with_id(app, "data", "Open data folder", true, None::<&str>)?;
    let log = MenuItem::with_id(app, "log", "Show server log", true, None::<&str>)?;
    let settings_item = MenuItem::with_id(app, "settings", "Settings…", true, None::<&str>)?;
    let quit_item = MenuItem::with_id(app, "quit", "Quit ARK Studio", true, None::<&str>)?;
    let sep = || PredefinedMenuItem::separator(app);
    let menu = Menu::with_items(
        app,
        &[&open, &switch, &browser, &sep()?, &start, &stop, &sep()?, &data, &log, &settings_item, &sep()?, &quit_item],
    )?;
    *state(app).tray_items.lock().unwrap() = Some(TrayItems { start, stop });

    let mut builder = TrayIconBuilder::with_id("ark")
        .tooltip("ARK Studio")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| {
            let handle = app.clone();
            match event.id.as_ref() {
                "open" => show_app(app),
                "switch" => show_chooser(app, None, None),
                "browser" => open_in_browser(app),
                "start" => {
                    std::thread::spawn(move || {
                        let s = state(&handle).settings.lock().unwrap().clone();
                        if let Err(e) = start_local(&handle, s.root(), s.port()) {
                            show_offline(&handle, "local", "", &e);
                        }
                    });
                }
                "stop" => {
                    std::thread::spawn(move || {
                        stop_sidecar(&handle);
                        if *state(&handle).current.lock().unwrap() == Current::Local {
                            *state(&handle).current.lock().unwrap() = Current::None;
                            if let Some(w) = handle.get_webview_window(MAIN) {
                                let _ = w.hide();
                            }
                            show_chooser(&handle, Some("The local server was stopped."), None);
                        }
                    });
                }
                "data" => {
                    let _ = app.opener().open_path(paths::data_dir().to_string_lossy(), None::<&str>);
                }
                "log" => {
                    let _ = app.opener().reveal_item_in_dir(paths::server_log());
                }
                "settings" => show_chooser(app, None, Some("settings")),
                "quit" => {
                    std::thread::spawn(move || quit(&handle));
                }
                _ => {}
            }
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click { button: MouseButton::Left, button_state: MouseButtonState::Up, .. } = event {
                show_app(tray.app_handle());
            }
        });
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
                show_offline(&app, "local", "", &msg);
            }
        }
    });
}

fn startup(app: AppHandle) {
    let s = state(&app).settings.lock().unwrap().clone();
    let auto = s.auto_connect && (s.mode == "local" || s.mode == "remote");
    if !auto {
        show_chooser(&app, None, None);
        return;
    }
    std::thread::spawn(move || match s.mode.as_str() {
        "local" => {
            if let Err(e) = start_local(&app, s.root(), s.port()) {
                show_offline(&app, "local", "", &e);
            }
        }
        _ => match settings::normalize_remote(&s.remote_url) {
            Ok(u) => open_remote(&app, &u),
            Err(_) => show_chooser(&app, None, None),
        },
    });
}

/// Stops every ark-server running from this executable's folder (installer
/// hooks: an upgrade must not leave a stale sidecar behind).
pub fn stop_installed_sidecars() {
    sidecar::kill_in_app_dir();
}

pub fn run() {
    // First start: create the default project folder (next to the app for
    // the Windows installer / portable zip, else in the per-user data dir).
    let _ = std::fs::create_dir_all(paths::default_root());
    sidecar::kill_orphan();
    let mut builder = tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| show_app(app)))
        .plugin(
            tauri_plugin_window_state::Builder::default()
                .with_denylist(&[CHOOSER])
                .with_state_flags(
                    // Decorations are ours to decide (borderless), never restored.
                    tauri_plugin_window_state::StateFlags::all()
                        & !tauri_plugin_window_state::StateFlags::VISIBLE
                        & !tauri_plugin_window_state::StateFlags::DECORATIONS,
                )
                .build(),
        )
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init());
    if let Some(key) = UPDATER_PUBKEY.filter(|k| !k.trim().is_empty()) {
        builder = builder.plugin(tauri_plugin_updater::Builder::new().pubkey(key).build());
    }
    let app = builder
        .manage(AppState { settings: Mutex::new(Settings::load()), ..Default::default() })
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
            open_chooser,
            check_update,
            open_local_path,
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            build_tray(&handle)?;
            spawn_monitor(handle.clone());
            startup(handle);
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                let app = window.app_handle();
                if !state(app).quitting.load(Ordering::SeqCst) {
                    api.prevent_close();
                    close_requested(app, window.label());
                }
            }
        })
        .build(tauri::generate_context!())
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
