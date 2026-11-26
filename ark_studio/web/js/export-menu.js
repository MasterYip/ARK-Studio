// "Export as Markdown bundle" entry shared by the unit menus (DOM rows and the
// canvas graph).  The server builds a ZIP of one note per node plus copied
// media (arkstudio/markdown_bundle.py); the browser just downloads it.
//
// A failed download only shows "Couldn't download" (and a file named after
// the JSON error), so the selection is checked first and any error is shown
// as a toast.  The URL names the tab's project because the server's
// per-session project can differ (another tab, or a server restart).

import { t } from "./i18n.js";
import { bundleExportUrl, checkBundleExport } from "./api.js";
import { toast } from "./components.js";
import { S } from "./state.js";
import { saveDownload } from "./download-link.js";

function formatBytes(bytes) {
  const n = Number(bytes) || 0;
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(1)} GB`;
  if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(n / 1024))} KB`;
}

export async function downloadBundle(uids, depth) {
  const list = [...new Set((uids || []).filter(Boolean))];
  if (!list.length) return;
  const options = { depth, project: S.project?.id || "" };
  let info;
  try {
    info = await checkBundleExport(list, options);
  } catch (error) {
    toast(t("export_bundle.failed", { error: error.message }), "err");
    return;
  }
  saveDownload(bundleExportUrl(list, options), { filename: "ark-bundle.zip" });
  toast(t("export_bundle.started", { n: info.nodes, files: info.files, size: formatBytes(info.bytes) }));
}

export function exportBundleMenuItem(units) {
  const uids = (units || []).map((unit) => String(unit?.uid || "")).filter(Boolean);
  if (!uids.length) return null;
  return {
    label: t("export_bundle.menu"), icon: "download", children: [
      { label: t("export_bundle.with_refs"), icon: "link", onClick: () => downloadBundle(uids, 1) },
      { label: t("export_bundle.selected_only"), icon: "file", onClick: () => downloadBundle(uids, 0) },
    ],
  };
}
