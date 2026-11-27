#![cfg_attr(not(windows), allow(dead_code))]
//! Windows: the tray icon's right-click menu as a small ARK-styled window
//! (ui/tray.html) instead of the native Win32 menu. Other systems keep the
//! native menu (`build_menu`); so does Windows when the window cannot be built.
//!
//! Flow: a right click stores the cursor position and asks the page to
//! refresh (or creates the hidden window, whose page then loads). The page
//! renders from `get_state`, measures its card and calls `tray_menu_ready`
//! with the size in CSS pixels; the window is then sized for the monitor under
//! the cursor, placed inside that monitor's work area (so it clears the
//! taskbar on any edge), shown and focused. Losing focus, Escape or a chosen
//! item hides it; items run through the same handler as the native menu.

use tauri::{AppHandle, Manager, PhysicalPosition, PhysicalSize, WebviewUrl, WebviewWindow, WebviewWindowBuilder};

pub const LABEL: &str = "tray-menu";

/// Where the last right click happened (physical pixels).
static ANCHOR: std::sync::Mutex<Option<(f64, f64)>> = std::sync::Mutex::new(None);

/// Top-left corner for a `size` popup opened at `anchor` inside `work`
/// (x, y, width, height; all physical pixels). Like a context menu it opens
/// to the lower right of the cursor and flips left/up where it would leave
/// the work area, then is clamped into it.
pub fn place(anchor: (i32, i32), size: (u32, u32), work: (i32, i32, u32, u32)) -> (i32, i32) {
    let (ax, ay) = anchor;
    let (w, h) = (size.0 as i32, size.1 as i32);
    let (wx, wy, ww, wh) = (work.0, work.1, work.2 as i32, work.3 as i32);
    let mut x = if ax + w > wx + ww { ax - w } else { ax };
    let mut y = if ay + h > wy + wh { ay - h } else { ay };
    x = x.min(wx + ww - w).max(wx);
    y = y.min(wy + wh - h).max(wy);
    (x, y)
}

fn window(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    if let Some(w) = app.get_webview_window(LABEL) {
        return Ok(w);
    }
    let builder = WebviewWindowBuilder::new(app, LABEL, WebviewUrl::App("tray.html".into()))
        .title("ARK Studio")
        .decorations(false)
        .transparent(true)
        .shadow(false)
        .resizable(false)
        .maximizable(false)
        .minimizable(false)
        .skip_taskbar(true)
        .always_on_top(true)
        .visible(false)
        .inner_size(280.0, 360.0)
        .disable_drag_drop_handler();
    // One WebView2 environment: the same browser arguments as the other windows.
    #[cfg(windows)]
    let builder = match crate::proxy::webview2_startup_args() {
        Some(args) => builder.additional_browser_args(&args),
        None => builder,
    };
    builder.build()
}

/// Right click on the tray icon. Runs off the main thread (`off_main`):
/// the first call creates the window.
pub fn open(app: &AppHandle, at: PhysicalPosition<f64>) -> tauri::Result<()> {
    *ANCHOR.lock().unwrap() = Some((at.x, at.y));
    let existed = app.get_webview_window(LABEL).is_some();
    let w = window(app)?;
    if existed {
        // A page that is already loaded re-renders and reports its size again.
        w.eval("window.__arkTrayRefresh && window.__arkTrayRefresh()")?;
    }
    Ok(())
}

pub fn hide(app: &AppHandle) {
    if let Some(w) = app.get_webview_window(LABEL) {
        let _ = w.hide();
    }
}

/// The page's card measures `width` x `height` CSS pixels: size, place, show.
pub fn ready(app: &AppHandle, width: f64, height: f64) -> Result<(), String> {
    let w = app.get_webview_window(LABEL).ok_or("no tray menu window")?;
    let Some((ax, ay)) = *ANCHOR.lock().unwrap() else { return Ok(()) };
    let monitor = app
        .monitor_from_point(ax, ay)
        .ok()
        .flatten()
        .or_else(|| app.primary_monitor().ok().flatten())
        .ok_or("no monitor")?;
    let scale = monitor.scale_factor();
    let size = PhysicalSize::new((width.clamp(120.0, 640.0) * scale).ceil() as u32, (height.clamp(60.0, 900.0) * scale).ceil() as u32);
    let area = monitor.work_area();
    let (x, y) = place(
        (ax.round() as i32, ay.round() as i32),
        (size.width, size.height),
        (area.position.x, area.position.y, area.size.width, area.size.height),
    );
    // Position first: the window then takes that monitor's scaling, and the
    // physical size below is exact for it.
    w.set_position(PhysicalPosition::new(x, y)).map_err(|e| e.to_string())?;
    w.set_size(size).map_err(|e| e.to_string())?;
    w.show().map_err(|e| e.to_string())?;
    w.set_focus().map_err(|e| e.to_string())?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::place;

    const WORK: (i32, i32, u32, u32) = (0, 0, 1920, 1040); // taskbar at the bottom

    #[test]
    fn opens_above_a_bottom_taskbar_and_left_at_the_right_edge() {
        assert_eq!(place((1800, 1060), (300, 400), WORK), (1500, 640));
    }

    #[test]
    fn opens_below_a_top_taskbar() {
        assert_eq!(place((1800, 20), (300, 400), (0, 40, 1920, 1040)), (1500, 40));
    }

    #[test]
    fn clears_a_left_taskbar() {
        assert_eq!(place((20, 900), (300, 400), (60, 0, 1860, 1080)), (60, 500));
    }

    #[test]
    fn second_monitor_with_negative_coordinates() {
        assert_eq!(place((-100, 1050), (300, 400), (-1920, 0, 1920, 1040)), (-400, 640));
    }

    #[test]
    fn larger_than_the_work_area_sticks_to_its_top_left() {
        assert_eq!(place((500, 500), (3000, 2000), WORK), (0, 0));
    }
}
