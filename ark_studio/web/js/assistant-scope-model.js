// Pure helpers for the Assistant's reach (assistant-response.js; server:
// internal/chat/scope.go).  The Assistant reads the current project by
// default and the person's other projects on request; a unit from another
// project comes back as [[prj_xxx/T-0042]].  No DOM here, so the rules are
// testable in Node.

import { parseCrossRef } from "./project-move-model.js";

// The label of a reference: "Team · T-0042" for a unit in another project
// the person has in their list, the reference as written otherwise.
export function crossRefLabel(ref, projects = [], currentId = "") {
  const cross = parseCrossRef(ref);
  if (!cross) return String(ref || "");
  if (cross.project === currentId) return cross.uid;
  const name = (projects || []).find((p) => p?.id === cross.project)?.name;
  return name ? `${name} · ${cross.uid}` : String(ref);
}

// The reference a tool step links to: [[prj/uid]] for a unit in another
// project (the result's cite), else the uid.
export function toolUnitRef(result) {
  if (!result?.uid) return "";
  const cross = parseCrossRef(String(result.cite || "").replace(/^\[\[|\]\]$/g, ""));
  return cross ? `${cross.project}/${cross.uid}` : String(result.uid);
}

// The project a tool step ran in, when it was not the current one.
export function toolProjectName(tool, projects = [], currentId = "") {
  const id = tool?.result?.project?.id || tool?.args?.project || "";
  if (!id || id === currentId) return "";
  return tool?.result?.project?.name || (projects || []).find((p) => p?.id === id || p?.name === id)?.name || String(id);
}
