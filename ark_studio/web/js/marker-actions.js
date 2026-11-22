// Saving marker changes: one POST /api/units/batch with tags_add /
// tags_remove for every target unit (one activity entry per unit, one batch
// id), and the undo that puts each unit's own tags back.  Used by the graph
// menu and marker shortcuts (mind.js onMarkers) and the inspectors.

import { api } from "./api.js";
import { batchTagsPayload, undoPayload, changedBefore } from "./selection-model.js";
import { syncUnitLocally, announceUnitSaved } from "./unit-edit.js";

function publish(ctx, units) {
  for (const unit of units || []) {
    syncUnitLocally(ctx, unit);
    announceUnitSaved(unit, "markers");
  }
}

/**
 * Apply `change` ({add, remove} marker tags) to the units `refs` (ids or
 * uids).  Returns null when there is nothing to change, else
 * `{ updated, skipped, undo }`; `undo()` restores the changed units and
 * resolves to their saved rows (null when nothing changed).
 */
export async function saveMarkerChange(ctx, refs, change) {
  if (!refs?.length || (!change?.add?.length && !change?.remove?.length)) return null;
  const before = (await api.unitsBatch(refs)).units || [];
  if (!before.length) return null;
  const reply = await api.batchUpdate(batchTagsPayload(before, change));
  publish(ctx, reply.updated);
  const changed = changedBefore(before, reply);
  return {
    updated: reply.updated || [],
    skipped: reply.skipped || [],
    undo: changed.length ? async () => {
      const back = await api.batchUpdate(undoPayload(changed, { tags: true }));
      publish(ctx, back.updated);
      return back.updated || [];
    } : null,
  };
}
