// Pure helpers for session context management (docs/agents/session-context.md).
// No DOM; the Runs panel (session-library.js) and the Home prompts use them.

export const SESSION_ID_PREFIX = "ark-ses-";
export const SESSION_ID_RE = /^ark-ses-[a-z0-9]{6,32}$/;
export const UPLOAD_INTERVAL_MINUTES = 5;
const ALPHABET = "abcdefghijkmnpqrstuvwxyz23456789";

/** A fresh external session id, e.g. ark-ses-k3v9q2m7xa (10 random chars). */
export function newArkSessionId(random = defaultRandom) {
  const bytes = random(10);
  let out = "";
  for (let i = 0; i < 10; i += 1) out += ALPHABET[bytes[i] % ALPHABET.length];
  return SESSION_ID_PREFIX + out;
}

function defaultRandom(n) {
  const bytes = new Uint8Array(n);
  if (globalThis.crypto?.getRandomValues) globalThis.crypto.getRandomValues(bytes);
  else for (let i = 0; i < n; i += 1) bytes[i] = Math.floor(Math.random() * 256);
  return bytes;
}

export function isArkSessionId(value) {
  return SESSION_ID_RE.test(String(value || ""));
}

export function formatBytes(value) {
  const n = Math.max(0, Number(value) || 0);
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MB`;
}

/** Node ids a session is linked to: its task first, then explicit links. */
export function contextLinks(item) {
  const out = [];
  for (const uid of [item?.task_ref, ...(item?.linked_units || [])]) {
    const value = String(uid || "").trim();
    if (value && !out.includes(value)) out.push(value);
  }
  return out;
}

/** "T-0001, F-0002  T-0001" -> ["T-0001", "F-0002"] (invalid ids dropped). */
export function parseLinks(text) {
  const out = [];
  for (const part of String(text || "").split(/[\s,;]+/)) {
    const uid = part.trim();
    if (uid && /^[A-Za-z0-9_.:-]+$/.test(uid) && !out.includes(uid)) out.push(uid);
  }
  return out.slice(0, 20);
}

export function filterContexts(items, { source = "all", query = "" } = {}) {
  const q = String(query || "").trim().toLowerCase();
  return (items || []).filter((item) => {
    if (source !== "all" && item.source !== source) return false;
    if (!q) return true;
    return [item.id, item.title, item.agent, item.model, item.cwd, item.native_session_id, ...contextLinks(item)]
      .some((value) => String(value || "").toLowerCase().includes(q));
  });
}

/** What Continue can offer for one record. */
export function continueOptions(item) {
  const resume = item?.resume || {};
  return {
    // Resuming in place changes the session: only its creator or an admin may.
    nativeResume: item?.source === "internal" && Boolean(resume.native) && item?.can_manage !== false,
    command: String(resume.command || ""),
    seed: true,
    active: Boolean(item?.active),
  };
}

/** Visibility label key: shared with the project, or private to its creator. */
export function visibilityKey(item) {
  return item?.shared ? "ctx.shared" : "ctx.private";
}

// Prompt text below is an instruction for the user's coding agent, in English
// and Chinese: one {en, zh} pair per line (plain strings are the same in both),
// filled with {{name}} values; tests/js/starter_prompts.test.mjs keeps the two
// versions in step.
/* i18n-ignore-start */
export const SESSION_PROMPT_LINES = {
  id: [
    {
      en: "ARK session id for this conversation: {{session_id}}. Quote it in your first reply and in reports you write to ARK, so this conversation can be found again.",
      zh: "本次对话的 ARK 会话 ID：{{session_id}}。在你的第一条回复和写入 ARK 的报告中注明它，以便之后能再找到这次对话。",
    },
  ],
  off: [
    {
      en: "Session uploads are turned off for this ARK project: do not upload your session transcript.",
      zh: "本 ARK 项目已关闭会话上传：不要上传你的会话记录。",
    },
  ],
  on: [
    {
      en: "Optional, only with my permission: this project can keep a copy of this session's JSONL transcript so I can continue it later from ARK (Execute > Runs > Session context). Ask me once: \"Upload this session's JSONL to ARK every {{interval}} minutes?\" If I say no, do not upload and do not ask again.",
      zh: "可选，且仅在我允许时：本项目可以保存这次会话的 JSONL 记录副本，方便我之后在 ARK 中继续它（执行 > 运行 > 会话上下文）。只问我一次：“每 {{interval}} 分钟把本次会话的 JSONL 上传到 ARK 吗？”如果我说不，就不要上传，也不要再问。",
    },
    {
      en: "If I say yes, start ARK's watcher in a separate terminal with the agent token in ARK_TOKEN (never put the token on the command line):",
      zh: "如果我同意，在单独的终端中启动 ARK 的 watcher，把智能体令牌放在 ARK_TOKEN 中（绝不要把令牌写在命令行上）：",
    },
    "  curl -fsS -H \"Authorization: Bearer $ARK_TOKEN\"{{header_arg}} {{url}}/api/agent/v1/sessions/watcher.py -o ark-session-watch.py",
    "  python ark-session-watch.py --id {{session_id}} --server {{url}}{{project_arg}} --interval {{interval_seconds}}",
    {
      en: "(Where ARK is installed, `ark-server session-watch --id {{session_id}} --server {{url}}{{project_arg}}` does the same.) The watcher finds your session file by this id; pass --file <path to your session .jsonl> if it cannot. Alternatively POST complete new JSONL lines to {{url}}/api/agent/v1/sessions/{{session_id}}/upload with {\"offset\": <next byte offset>, \"content\": \"<lines>\\n\"}; GET {{url}}/api/agent/v1/sessions/{{session_id}} returns the next offset. ARK redacts secrets, but never paste credentials into the conversation.",
      zh: "（在已安装 ARK 的机器上，`ark-server session-watch --id {{session_id}} --server {{url}}{{project_arg}}` 的效果相同。）watcher 通过这个 ID 找到你的会话文件；如果找不到，传入 --file <你的会话 .jsonl 文件路径>。也可以把完整的新 JSONL 行 POST 到 {{url}}/api/agent/v1/sessions/{{session_id}}/upload，请求体为 {\"offset\": <下一个字节偏移>, \"content\": \"<lines>\\n\"}；GET {{url}}/api/agent/v1/sessions/{{session_id}} 返回下一个偏移。ARK 会对密钥做脱敏处理，但绝不要把凭据粘贴到对话中。",
    },
  ],
};

function renderPromptLines(lines, lang, vars) {
  return lines.map((line) => {
    const text = typeof line === "string" ? line : (line[lang] ?? line.en);
    return text.replace(/\{\{(\w+)\}\}/g, (_, key) => String(vars[key] ?? ""));
  }).join("\n");
}

/** Session id + optional upload instructions for a Home starter prompt. */
export function sessionPromptBlock({ sessionId, url, projectId = "", uploads = false,
  interval = UPLOAD_INTERVAL_MINUTES, lang = "en" } = {}) {
  const vars = {
    session_id: sessionId,
    url,
    interval,
    interval_seconds: interval * 60,
    project_arg: projectId ? ` --project ${projectId}` : "",
    header_arg: projectId ? ` -H "X-ARK-Project: ${projectId}"` : "",
  };
  const L = SESSION_PROMPT_LINES;
  return renderPromptLines([...L.id, ...(uploads ? L.on : L.off)], lang, vars);
}
/* i18n-ignore-end */
