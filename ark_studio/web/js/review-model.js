// Pure helpers for review requests (dependency-free; see
// tests/js/review_model.test.mjs): the anchors of a review note, the parts
// of a note to render, headline numbers of a result, and the labels of the
// "changes since the last review" toggle.  The server resolves anchors
// (store/anchors.go); this module only splits and labels them.

// Anchor syntax (docs/agents/working-mode.md "Review anchors"):
//   [label](#slug)            heading / {#id} block in the reviewed unit
//   [label](T-0042)           another unit
//   [label](T-0042#slug)      heading / block in another unit
//   [label](media:421@0:08-0:12) or a bare media:421@0:08  attachment, video time
const LINK_RE = /\[([^\]\n]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)/g;
const BARE_MEDIA_RE = /(^|[^\w/(:])(media:\d+(?:@[0-9:.]+(?:-[0-9:.]+)?)?)/g;
const UNIT_RE = /^([A-Z]{1,4}-\d{1,9})(?:#(.+))?$/;
const MEDIA_RE = /^media:(\d+)(?:@([0-9:.]+)(?:-([0-9:.]+))?)?$/;

/** "8", "0:08", "1:02:03" -> seconds, or null. */
export function parseClock(text) {
  const parts = String(text || "").split(":");
  if (!parts.length || parts.length > 3 || parts.some((p) => p === "")) return null;
  let total = 0;
  for (let i = 0; i < parts.length; i++) {
    const v = Number(parts[i]);
    if (!Number.isFinite(v) || v < 0 || (i > 0 && v >= 60)) return null;
    total = total * 60 + v;
  }
  return total;
}

/** Seconds -> "0:08" / "1:02:03". */
export function clockLabel(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
  const pad = (n) => String(n).padStart(2, "0");
  return h ? `${h}:${pad(m)}:${pad(r)}` : `${m}:${pad(r)}`;
}

/** One anchor target -> `{raw, kind, unit, slug, media, start, end}` or null. */
export function parseAnchor(raw) {
  const text = String(raw || "").trim();
  if (text.startsWith("#") && text.length > 1) return { raw: text, kind: "heading", unit: "", slug: text.slice(1) };
  let match = MEDIA_RE.exec(text);
  if (match) {
    const start = match[2] ? parseClock(match[2]) : null;
    const end = match[3] ? parseClock(match[3]) : null;
    if ((match[2] && start == null) || (match[3] && (end == null || end < start))) return null;
    return { raw: text, kind: "media", media: Number(match[1]), start, end };
  }
  match = UNIT_RE.exec(text);
  if (match) return match[2]
    ? { raw: text, kind: "heading", unit: match[1], slug: match[2] }
    : { raw: text, kind: "unit", unit: match[1], slug: "" };
  return null;
}

/** The anchors of a note, in order, once each (labels from the link text). */
export function parseAnchors(note) {
  const out = [];
  const seen = new Set();
  const add = (raw, label = "") => {
    const anchor = parseAnchor(raw);
    if (!anchor || seen.has(anchor.raw)) return;
    seen.add(anchor.raw);
    out.push({ ...anchor, label: String(label).trim() });
  };
  const text = String(note || "");
  for (const m of text.matchAll(LINK_RE)) add(m[2], m[1]);
  const rest = text.replace(LINK_RE, " ");
  for (const m of rest.matchAll(BARE_MEDIA_RE)) add(m[2]);
  return out;
}

/**
 * A note as parts to render: `{text}` runs and `{anchor}` chips (links to
 * anchors and bare media references); other Markdown links keep their text.
 */
export function noteParts(note) {
  const text = String(note || "");
  const parts = [];
  const push = (value) => { if (value) parts.push({ text: value }); };
  let last = 0;
  const tokens = [];
  for (const m of text.matchAll(LINK_RE)) tokens.push({ index: m.index, length: m[0].length, raw: m[2], label: m[1] });
  for (const m of text.matchAll(BARE_MEDIA_RE)) {
    const index = m.index + m[1].length;
    if (!tokens.some((tok) => index >= tok.index && index < tok.index + tok.length)) {
      tokens.push({ index, length: m[2].length, raw: m[2], label: "" });
    }
  }
  tokens.sort((a, b) => a.index - b.index);
  for (const tok of tokens) {
    push(text.slice(last, tok.index));
    const anchor = parseAnchor(tok.raw);
    if (anchor) parts.push({ anchor: { ...anchor, label: tok.label.trim() } });
    else push(tok.label || tok.raw);
    last = tok.index + tok.length;
  }
  push(text.slice(last));
  return parts.map((part) => (part.text != null
    ? { text: part.text.replace(/(\*\*|__|`|~~)/g, "") }
    : part));
}

/** A short label for an anchor chip when the link has no text. */
export function anchorLabel(anchor = {}, selfUid = "") {
  if (anchor.label) return anchor.label;
  if (anchor.kind === "media") {
    const range = anchor.start != null
      ? ` @${clockLabel(anchor.start)}${anchor.end != null ? `–${clockLabel(anchor.end)}` : ""}` : "";
    return `media:${anchor.media}${range}`;
  }
  const unit = anchor.unit && anchor.unit !== selfUid ? anchor.unit : "";
  if (anchor.kind === "heading") return unit ? `${unit} › ${anchor.slug}` : `§ ${anchor.slug}`;
  return unit || anchor.raw || "";
}

/** The unit an anchor opens ("" for media). */
export function anchorUnit(anchor = {}, selfUid = "") {
  if (anchor.kind === "media") return anchor.media_item?.unit || "";
  return anchor.unit || selfUid || "";
}

// --------------------------------------------------------- results table
// Headline number of a result's title: percentages first, then durations,
// sizes, factors, then any decimal.  "" when the title has none.
const NUMBER = String.raw`(?<![\w.-])[~≈<>≤≥]?\d+(?:[.,]\d+)?(?:\s?[–-]\s?\d+(?:[.,]\d+)?)?`;
const NUMBER_PATTERNS = [
  new RegExp(`${NUMBER}\\s?%`),
  new RegExp(`${NUMBER}\\s?(?:ms|µs|us|ns|s|sec|min|h)\\b`),
  new RegExp(`${NUMBER}\\s?(?:[KMGT]i?B|[kKMG]\\b|mm|cm|m\\b)`),
  new RegExp(`${NUMBER}\\s?[x×](?![\\d])`),
  /(?<![\w.-])[~≈]?\d+\.\d+/,
];

export function keyNumber(title = "") {
  const text = String(title || "");
  for (const re of NUMBER_PATTERNS) {
    const m = re.exec(text);
    if (m) return m[0].replace(/\s+/g, " ").trim();
  }
  return "";
}

/** "+12 −3 · 2 new files"-style parts of the changes toggle (null = nothing to show). */
export function changeSummary(changes) {
  if (!changes) return null;
  const added = Number(changes.added) || 0;
  const removed = Number(changes.removed) || 0;
  const media = Number(changes.media) || 0;
  return { added, removed, media, empty: !added && !removed && !media };
}

/** The diff hunks to render: consecutive add/del lines stay together; skip keeps its count. */
export function diffBlocks(hunks = []) {
  return (Array.isArray(hunks) ? hunks : []).map((hunk) => (hunk.op === "skip"
    ? { op: "skip", n: Number(hunk.n) || 0 }
    : { op: hunk.op, text: (hunk.lines || []).join("\n") }));
}

/**
 * Whether the inspector offers Accept / Send back for a unit: it is in
 * review and the person may write (project permission not "read", not a
 * read-only phone pairing).
 */
export function canDecideReview(unit, { permission = "", mobilePermission = "" } = {}) {
  if (unit?.status !== "review") return false;
  if (String(permission).toLowerCase() === "read") return false;
  if (/read/i.test(String(mobilePermission || ""))) return false;
  return true;
}

/** The changes toggle's text, e.g. "Since your last review · +3 −1 · 2 new files". `tr` is i18n t(). */
export function changesText(changes, base, tr) {
  const sum = changeSummary(changes);
  if (!sum) return "";
  const head = base?.kind === "review" ? tr("review_req.since_review") : tr("review_req.since_entered");
  if (sum.empty) return `${head} · ${tr("review_req.no_changes")}`;
  const parts = [];
  if (sum.added || sum.removed) parts.push(`+${sum.added} −${sum.removed}`);
  if (sum.media) parts.push(tr("review_req.new_files", { n: sum.media }));
  return `${head} · ${parts.join(" · ")}`;
}

/**
 * What a review card shows, as text (`tr` is i18n t()): the round, the lead
 * ("Review this" with the note, or "No note on what to review" with the
 * newest part of the body), the previous round and the changes toggle.
 */
export function reviewCardText(item = {}, tr = (key) => key) {
  const prev = item.review_prev;
  const hasPrev = Boolean(prev && (prev.outcome || (prev.comments || []).length));
  return {
    round: item.review_round ? tr("review_req.round", { n: item.review_round }) : "",
    lead: item.review_note ? tr("review_req.look_here") : tr("review_req.no_note"),
    note: item.review_note || "",
    focus: item.review_note ? "" : (item.review_focus || item.excerpt_plain || ""),
    prev: hasPrev
      ? [prev.round ? tr("review_req.prev_round", { n: prev.round }) : tr("review_req.prev_feedback"),
        prev.outcome ? tr(`needs.value.${prev.outcome}`) : ""].filter(Boolean).join(" · ")
      : "",
    open: hasPrev ? (prev.comments || []).filter((c) => !c.resolved).length : 0,
    changes: changesText(item.review_changes, item.review_base, tr),
  };
}
