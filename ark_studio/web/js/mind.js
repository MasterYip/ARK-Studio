import { topicMenuItems } from "./topic-actions.js";
import { api } from "./api.js";
import { t, relLabel } from "./i18n.js";
import { confirmDialog } from "./components.js";
import { mediaSource } from "./editor-media.js";
import { saveMarkerChange } from "./marker-actions.js";

const GRAPH_LAYOUTS = new Set(["force", "mind", "node", "timeline"]);

/** Optional deep link: `?graph=mind` opens a graph in the requested layout. */
export function preferredGraphLayout(fallback = "force") {
  try {
    const value = new URLSearchParams(location.search).get("graph");
    if (GRAPH_LAYOUTS.has(value)) return value;
    const remembered = localStorage.getItem("ark.graphLayout");
    return GRAPH_LAYOUTS.has(remembered) ? remembered : fallback;
  } catch {
    return fallback;
  }
}

/**
 * Shared callbacks for editable GraphView surfaces. The graph owns the visual
 * interaction (selection, inline title, Alt-drag, keyboard); these callbacks own
 * the API writes and return the minimal node/link payloads GraphView inserts
 * locally so editing stays immediate.
 */
export function mindGraphOptions(ctx) {
  const uidOf = (unit) => unit?.uid || unit?.id;
  const parentOf = async (node) => {
    try {
      const data = await api.unit(uidOf(node));
      const link = (data.unit?.links_out || []).find((item) => item.relation === "part_of");
      return link?.target || null;
    } catch {
      return null;
    }
  };

  const onRename = async (node, title) => {
    const previous = node.title;
    node.title = title;
    if (node._pendingCreate) {
      // The node still has a temporary client id.  Apply the title when the
      // create request returns; never make the editor wait for the network.
      node._pendingTitle = title;
      if (node._pendingState) node._pendingState.title = title;
      return;
    }
    // Apply the title immediately; persist in the background so closing the
    // inline editor never waits for a remote round trip.
    api.updateUnit(uidOf(node), { title }).catch((err) => {
      node.title = previous;
      ctx.toast?.(err.message || String(err), "err");
    });
  };

  const onAddOrphan = async (world = {}) => {
    const created = await api.createUnit({
      type: "note",
      title: t("mind.new_node"),
      status: "open",
      importance: "p2",
      origin: "mind",
      tags: ["seq:0"],
    });
    return {
      unit: created.unit,
      link: null,
      parentId: null,
      orphan: true,
      world,
    };
  };

  const onAttachImage = async (node, file) => {
    const data = await new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result || "").split(",", 2)[1] || "");
      reader.onerror = () => reject(reader.error || new Error(t("mind.error.read_image")));
      reader.readAsDataURL(file);
    });
    if (!data) throw new Error(t("mind.error.image_empty"));
    await api.uploadMedia({
      unit: uidOf(node), filename: file.name || "pasted.png",
      mime: file.type || "image/png", data, caption: file.name || "Pasted image", // i18n-ignore: stored data
    });
    const fresh = await api.unit(uidOf(node));
    return { media: fresh.unit.media || [] };
  };

  const onPasteNodes = async (clipboard, targets) => {
    const nodes = [];
    const links = [];
    // With no target the clipboard still creates nodes; they remain orphans
    // (unless the clipboard itself carries links between the pasted nodes).
    const targetList = targets && targets.length ? targets : [null];
    for (const target of targetList) {
      const idMap = new Map();
      for (const item of clipboard.items || []) {
        let unit;
        if (clipboard.source === "infohub") {
          try {
            const imported = await api.infoHubImport({
              path: ctx.S.infoHubPath, key: item.uid || item.key, as_idea: 1, copy: 1,
            });
            unit = imported.unit;
          } catch (err) {
            ctx.toast?.(err.message, "err");
            continue;
          }
        } else {
          const created = await api.createUnit({
            type: item.type || "note",
            title: item.title || t("mind.new_node"),
            status: item.status || "open",
            importance: item.importance || "p2",
            body: item.body || "",
            origin: "mind",
            tags: (item.tags || []).filter((tag) => !/^(type|status|importance):/.test(tag)),
            start_at: item.start_at || null,
            end_at: item.end_at || null,
          });
          unit = created.unit;
        }
        if (!unit) continue;
        idMap.set(item.key, unit);
        nodes.push({ unit, targetId: target?.id || null });
      }
      if (target) {
        for (const rootKey of clipboard.roots || []) {
          const root = idMap.get(rootKey);
          if (!root) continue;
          try {
            const link = await api.createLink({
              source: root.id, target: target.id, relation: "part_of",
              note: "Pasted from Mind Graph", // i18n-ignore: stored data
            });
            if (link?.id) links.push({ id: link.id, source: root.id, target: target.id, relation: "part_of" });
          } catch { /* still keep the copied node as an orphan */ }
        }
      }
      for (const edge of clipboard.links || []) {
        const source = idMap.get(edge.source);
        const targetUnit = idMap.get(edge.target);
        if (!source || !targetUnit) continue;
        const relation = edge.relation || "part_of";
        try {
          const link = await api.createLink({
            source: source.id, target: targetUnit.id, relation,
            note: clipboard.source === "infohub" ? "Pasted from InfoHub" : "Pasted from Mind Graph", // i18n-ignore: stored data
          });
          if (link?.id) links.push({ id: link.id, source: source.id, target: targetUnit.id, relation });
        } catch { /* one broken relation must not abort the paste */ }
      }
    }
    return { nodes, links };
  };

  const onAddNode = (node, where) => {
    // Determine the parent from the already-loaded graph.  The old
    // implementation made several API round trips (detail, children, child
    // details) before the node appeared, which made Enter/Tab feel seconds
    // behind on a remote server.  Build the local node immediately and let the
    // writes reconcile underneath the editor.
    const graph = (ctx.graphs || []).find((item) => item.byId?.has(node.id));
    const parentEdge = graph?.edges?.find((edge) => edge.source === node.id
      && edge.relation === "part_of");
    const parent = where === "child" ? node
      : (parentEdge ? graph?.byId?.get(parentEdge.target) : null);
    const orphan = !parent;
    const seqOf = (unit) => {
      const tag = (unit.tags || []).find((item) => item.startsWith("seq:"));
      const value = tag ? parseFloat(tag.split(":").slice(1).join(":")) : NaN;
      return Number.isFinite(value) ? value : 0;
    };
    const seq = Math.round((seqOf(node)
      + (where === "above" ? -0.5 : where === "below" ? 0.5 : 1)) * 1e6) / 1e6;
    const pendingState = { title: "" };
    const stamp = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const tempId = `local-${stamp}`;
    const tempUid = `LOCAL-${stamp}`;
    const unit = {
      id: tempId, uid: tempUid,
      title: t("mind.new_node"),
      type: node.type || "note",
      status: "open",
      importance: node.importance || "p2",
      body: "",
      tags: [`seq:${seq}`],
      origin: "mind",
      pending: true,
      x: node.x, y: node.y, vx: 0, vy: 0,
    };
    const pending = (async () => {
      const created = await api.createUnit({
        type: unit.type,
        title: unit.title,
        status: unit.status,
        importance: unit.importance,
        origin: "mind",
        tags: unit.tags,
      });
      let link = null;
      if (parent?._pendingCreate) {
        // A child can be added to a node whose create request is still in
        // flight.  Wait for its real id before writing the hierarchy link.
        try { await parent._pendingCreate; } catch { /* parent failed */ }
      }
      if (parent) {
        try {
          link = await api.createLink({
            source: created.unit.id,
            target: parent.id,
            relation: "part_of",
            note: "Created from Mind Graph", // i18n-ignore: stored data
          });
        } catch { /* keep the local hierarchy */ }
      }
      if (pendingState.title) {
        try { await api.updateUnit(created.unit.uid, { title: pendingState.title }); }
        catch { /* title remains local */ }
      }
      return { unit: created.unit, link, parentId: parent?.id || null,
               oldId: tempId };
    })();
    unit._pendingCreate = pending;
    unit._pendingState = pendingState;
    return {
      unit,
      link: null,
      parentId: parent?.id || null,
      orphan,
      pending,
    };
  };

  return {
    // A graph of this project's units (multi-selection inspector, app.js).
    projectUnits: true,
    // Research line tags (timeline graph lanes): the project's namespace.
    linePrefix: () => ctx.S.project?.settings?.branches?.prefix || "branch",
    mode: ctx.S.graphMode || undefined,
    onModeChange: (mode, options) => ctx.syncInspectorMode?.(mode, options),
    getSelectedId: () => ctx.S.selectedId,
    onInsertBetween: async (node) => {
      try {
        return await api.insertBetween(uidOf(node), { title: t("mind.new_node") });
      } catch (err) {
        ctx.toast?.(err.message || String(err), "err");
        return null;
      }
    },
    // Ctrl+Enter on several siblings: one new parent for them (one
    // transaction on the server); Ctrl+Z dissolves it again.
    onGroupNodes: async (nodes, parent, { relation = "part_of", type = "note", tags = [] } = {}) => {
      try {
        return await api.groupUnits({
          units: nodes.map(uidOf), parent: parent ? uidOf(parent) : null, relation,
          title: t("mind.new_node"), type, tags,
        });
      } catch (err) {
        ctx.toast?.(err.message || String(err), "err");
        return null;
      }
    },
    onUngroup: async (node, { relation = "part_of" } = {}) => api.ungroupUnit(uidOf(node), { relation }),
    onRename,
    onAddNode,
    onAddOrphan,
    onAttachImage,
    onMediaOpen: async (item) => {
      const remote = /^[a-z][a-z0-9+.-]*:\/\//i.test(item.url || "");
      await api.openExternal(remote ? { url: item.url } : { path: item.url });
    },
    onPasteNodes,
    onContextMenu: (nodes) => ctx.openBulkEdit?.(nodes),
    onOpenTopic: (node) => ctx.openTopic?.(node.uid || node.id),
    nodeMenuExtras: (nodes, node) => topicMenuItems(ctx, nodes, node),
    onOpenEditor: ctx.openUnitInEditor ? (node) => ctx.openUnitInEditor(node) : undefined,
    onMediaEditor: ctx.openInEditor
      ? (item, node) => ctx.openInEditor(mediaSource(item, node))
      : undefined,
    onConnect: async (source, target) => {
      try {
        const link = await api.createLink({ source: source.id, target: target.id, relation: "unset" });
        ctx.toast?.(t("toast.linked"), "ok");
        return { link, relation: "unset" };
      } catch (err) {
        ctx.toast?.(err.message || String(err), "err");
        return null;
      }
    },
    onEdgeClick: (edge, source, target) => ctx.openEdgeRelation?.(edge, source, target),
    onDeleteLink: async (id) => { await api.deleteLink(id); },
    // Delete / Backspace or the menu on a selected link (graph.js
    // _deleteSelectedEdge): confirm like the inspector's link rows, then
    // DELETE /api/links/{id} (activity link.delete).
    onDeleteEdge: async (edge, source, target) => {
      if (!/^\d+$/.test(String(edge?.id ?? ""))) return false;
      const name = (node, id) => (node ? `${node.uid || node.id} · ${node.title || ""}`.trim() : String(id));
      const ok = await confirmDialog(t("edge.delete_confirm", {
        relation: relLabel(edge.relation || "unset"), source: name(source, edge.source), target: name(target, edge.target),
      }), { title: t("edge.delete"), confirmLabel: t("edge.delete"), danger: true });
      if (!ok) return false;
      try {
        await api.deleteLink(edge.id);
      } catch (err) {
        ctx.toast?.(err.message || String(err), "err");
        return false;
      }
      ctx.toast?.(t("toast.unlinked"), "ok");
      for (const graph of ctx.graphs || []) graph.removeEdge?.(edge.id);
      if (ctx.S?.drawerOpen) ctx.reloadDetail?.();
      return true;
    },
    onRestoreNodes: async (nodes) => {
      for (const node of nodes) await api.restoreUnit(uidOf(node));
    },
    onRestoreLinks: async (edges) => {
      const created = [];
      for (const edge of edges) {
        try {
          created.push(await api.createLink({ source: edge.source, target: edge.target, relation: edge.relation }));
        } catch { created.push(null); /* an equivalent link may already exist */ }
      }
      return created;
    },
    // Markers: one batch for all target nodes, one undo step (marker-actions.js).
    onMarkers: (nodes, change) => saveMarkerChange(ctx, nodes.map(uidOf), change),
    onAddTag: async (nodes, tag) => {
      for (const node of nodes) {
        const data = await api.unit(uidOf(node));
        const current = data.unit || {};
        const tags = [...new Set([...(current.tags || node.tags || []), tag])];
        await api.updateUnit(uidOf(node), { tags });
      }
      ctx.toast?.(t("mind.toast.tagged", { tag }), "ok");
    },
    onDelete: async (nodes, { skipConfirm = false } = {}) => {
      let simple = false;
      if (nodes.length) {
        simple = (await Promise.all(nodes.map(async (node) => {
          try {
            const unit = (await api.unit(uidOf(node))).unit || {};
            const extraLinks = [...(unit.links_out || []), ...(unit.links_in || [])]
              .filter((link) => link.relation !== "part_of");
            return !String(unit.body || "").trim()
              && !(unit.media || []).length
              && (!String(unit.excerpt || "").trim() || unit.excerpt === unit.title)
              && !unit.start_at && !unit.end_at
              && !(unit.assignees || []).length
              && !extraLinks.length;
          } catch {
            return false;
          }
        }))).every(Boolean);
      }
      if (!simple && !skipConfirm) {
        const ok = await confirmDialog(
          nodes.length === 1
            ? t("mind.delete_confirm", { title: nodes[0].title })
            : t("mind.delete_many", { n: nodes.length }),
          { title: t("mind.delete"), confirmLabel: t("action.move_to_trash") },
        );
        if (!ok) return false;
      }
      try {
        for (const node of nodes) await api.archiveUnit(node.uid || node.id);
        ctx.closeUnit?.();
        ctx.toast?.(nodes.length === 1 ? t("mind.deleted") : t("mind.deleted_many", { n: nodes.length }), "ok");
        return true;
      } catch (err) {
        ctx.toast?.(err.message || String(err), "err");
        return false;
      }
    },
    relations: (ctx.S.vocab?.relations || []).map((relation) => relation.id),
    onNotice: (message) => ctx.toast?.(message, "info"),
    onError: (err) => ctx.toast?.(err.message || String(err), "err"),
  };
}
