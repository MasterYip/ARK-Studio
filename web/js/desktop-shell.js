// Desktop shell mode: ARK running inside the ARK Studio desktop app, whose
// windows have no native title bar. The app injects window.__ARK_DESKTOP__
// and draws the window controls (minimize / maximize / close) at the top
// right (#ark-desktop-controls); on macOS the native traffic lights sit at the
// top left instead (chrome "overlay").
//
// Here the web app opts in: it marks the empty space of its own top bar and
// startup screen as the window's drag region (double-click maximizes; the
// shell implements both via data-tauri-drag-region) and, through
// html[data-desktop-shell], CSS leaves room for the controls (app.css,
// startup.css). Buttons, links and inputs inside those areas stay clickable.
// Normal browsers have no __ARK_DESKTOP__ and nothing changes.

export const DRAG_ATTR = "data-tauri-drag-region";
export const DRAG_REGIONS = ["#topbar", "#ark-startup"];

/** "custom" (ARK-drawn controls), "overlay" (macOS traffic lights) or "" (not in the desktop app). */
export function desktopChrome(desktop) {
  if (!desktop || typeof desktop !== "object" || !desktop.window) return "";
  return desktop.chrome === "overlay" ? "overlay" : "custom";
}

export function installDesktopShell(desktop = globalThis.window?.__ARK_DESKTOP__, doc = globalThis.document) {
  const chrome = desktopChrome(desktop);
  if (!chrome || !doc?.documentElement) return "";
  doc.documentElement.setAttribute("data-desktop-shell", chrome);
  for (const selector of DRAG_REGIONS) {
    const el = doc.querySelector(selector);
    if (el && !el.hasAttribute(DRAG_ATTR)) el.setAttribute(DRAG_ATTR, "deep");
  }
  return chrome;
}
