// Where a node tab should open: a field (title, body, tags, comment) and,
// for the body, a 1-based line.  Set by whoever opens the tab (Search
// results), taken once by the node editor when the tab is ready.

const pending = new Map();   // tab key ("unit:<uid>") -> { field, line, commentId }

export function setNodeReveal(uid, reveal) {
  const key = `unit:${uid}`;
  if (reveal && typeof reveal === "object") pending.set(key, { ...reveal });
  else pending.delete(key);
}

export function takeNodeReveal(key) {
  const reveal = pending.get(key) || null;
  pending.delete(key);
  return reveal;
}

// The character range of 1-based `line` in `text` (splitlines semantics
// for "\n", "\r\n" and "\r"); null when the line does not exist.
export function lineRange(text, line) {
  const value = String(text ?? "");
  const target = Math.max(1, Math.floor(Number(line) || 1));
  let start = 0;
  for (let n = 1; n < target; n += 1) {
    const lf = value.indexOf("\n", start);
    const cr = value.indexOf("\r", start);
    const next = cr >= 0 && (lf < 0 || cr < lf) ? cr : lf;
    if (next < 0) return null;
    start = next + (value[next] === "\r" && value[next + 1] === "\n" ? 2 : 1);
  }
  let end = start;
  while (end < value.length && value[end] !== "\n" && value[end] !== "\r") end += 1;
  return { start, end };
}
