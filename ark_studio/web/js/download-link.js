// Downloads a page starts from script.  In the ARK Studio desktop app
// (window.__ARK_DESKTOP__.downloads) an <a download> click is saved by the
// app to the Downloads folder and shown in the window
// (desktop/src-tauri/src/downloads.rs); a browser keeps its own handling.
// Plain <a download> links need nothing from here: both paths save them.

/** Whether the desktop app saves this page's downloads itself. */
export function appSavesDownloads(desktop = globalThis.window?.__ARK_DESKTOP__) {
  return !!(desktop && typeof desktop === "object" && desktop.downloads === true);
}

/**
 * Opens a download URL: in the app as an <a download> click the app saves
 * ("app"); in a browser (and older apps, which hand new tabs to the system
 * browser) in a new tab as before ("tab").
 */
export function openDownload(url, { filename = "", win = globalThis.window, doc = globalThis.document } = {}) {
  if (appSavesDownloads(win?.__ARK_DESKTOP__)) {
    const link = doc.createElement("a");
    link.href = url;
    link.download = filename;
    link.rel = "noopener";
    doc.body.append(link);
    link.click();
    link.remove();
    return "app";
  }
  win.open(url, "_blank");
  return "tab";
}
