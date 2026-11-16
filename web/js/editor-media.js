// Node attachments as Editor sources.  Kept apart from views/editor.js so the
// inspector and the graph can use them without loading the Editor.
import { baseName, viewerFor } from "./editor-model.js";

export function mediaSource(item, unit = null) {
  const name = baseName(item.url || item.caption || `attachment-${item.id}`);
  return {
    kind: "media", mediaId: item.id, mediaUrl: item.url, rawUrl: item.raw_url || "",
    name, title: name, caption: item.caption || "", unit: unit?.uid || unit?.id || "",
    unitTitle: unit?.title || "", mediaKind: item.kind || "",
  };
}

// Attachments the Editor can render (others keep the inspector popover).
export function mediaPreviewable(item) {
  const viewer = viewerFor({ name: item.url, url: item.url, kind: item.kind, source: "media" });
  return !["link", "unsupported"].includes(viewer);
}
