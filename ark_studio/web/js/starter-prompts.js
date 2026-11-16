// i18n-ignore-file: instructions for the user's coding agent, written in both
// languages here rather than in the UI dictionaries.
//
// The Home "Start with an agent" prompts, in English and Chinese: "setup"
// (set up the environment; the human confirms in ARK; then how to use ARK)
// and "team" (a new session loads the research context, summarises it and
// coordinates the chosen work).  Both point the agent at ARK's documentation
// (GET /api/agent/v1/docs) and carry standing rules for recording work in
// ARK near the top, and the session block last.  Every line is one {en, zh}
// pair (a plain string is the same in both languages), so both versions keep
// the same steps and rules; tests/js/starter_prompts.test.mjs checks that
// every API path, header, JSON field and {{placeholder}} of the English line
// appears verbatim in the Chinese one.  {{name}} is filled at render time.
// No DOM: onboarding.js renders, this module only builds text.
import { sessionPromptBlock } from "./session-context-model.js";

export const PROMPT_LANGS = ["en", "zh"];

/** Prompts follow the interface language. */
export function promptLang(uiLang) {
  return uiLang === "zh" ? "zh" : "en";
}

export function fillPrompt(text, vars = {}) {
  return String(text).replace(/\{\{(\w+)\}\}/g, (_, key) => String(vars[key] ?? ""));
}

export function pickLine(line, lang) {
  if (typeof line === "string") return line;
  return line[lang] ?? line.en;
}

export function renderLines(lines, lang, vars = {}) {
  return lines.map((line) => fillPrompt(pickLine(line, lang), vars)).join("\n");
}

// Shared by both prompts, in this order.  "token" or "noToken" is used, never both.
export const START_SHARED = {
  intro: [
    {
      en: "You are my research agent. ARK is my research project system; it keeps the durable record (ideas, claims, tasks, evidence, reports) and you do the work through its agent API.",
      zh: "你是我的研究智能体。ARK 是我的研究项目系统：它保存持久的记录（想法、论断、任务、证据、报告），你通过它的智能体 API 完成工作。",
    },
    {
      en: "ARK: {{url}}  ·  project \"{{project_name}}\"{{project_id_part}}",
      zh: "ARK：{{url}}  ·  项目“{{project_name}}”{{project_id_part}}",
    },
  ],
  token: [
    {
      en: "Your agent token is below; send it as \"Authorization: Bearer …\" with every /api/agent/v1/... call, together with \"X-ARK-Project: {{project_id}}\". It is my personal token: you act as me, with my project permissions. GET {{url}}/api/agent/v1/whoami shows who you act for. Never write the token into files, units or reports.",
      zh: "你的智能体令牌在下方；每次调用 /api/agent/v1/... 时都以 \"Authorization: Bearer …\" 发送它，并同时发送 \"X-ARK-Project: {{project_id}}\"。这是我的个人令牌：你以我的身份、按我的项目权限操作。GET {{url}}/api/agent/v1/whoami 显示你代表谁操作。绝不要把令牌写进文件、节点或报告。",
    },
    { en: "Agent token: {{token}}", zh: "智能体令牌：{{token}}" },
  ],
  noToken: [
    {
      en: "Agent token: send it as \"Authorization: Bearer <token>\" to every /api/agent/v1/... call, together with \"X-ARK-Project: {{project_id}}\". Ask me for a personal agent token (ARK → Settings → Personal → Agent access); it acts as me. If you run on the same machine as ARK, the legacy server token in the .agent-token file of ARK's projects folder also works. Never write the token into files, units or reports.",
      zh: "智能体令牌：每次调用 /api/agent/v1/... 时以 \"Authorization: Bearer <token>\" 发送，并同时发送 \"X-ARK-Project: {{project_id}}\"。向我索取个人智能体令牌（ARK → 设置 → 个人 → 智能体访问令牌）；它以我的身份操作。如果你和 ARK 运行在同一台机器上，ARK 项目目录中 .agent-token 文件里的旧版服务器令牌也可以用。绝不要把令牌写进文件、节点或报告。",
    },
  ],
  docs: [
    {
      en: "Documentation: this prompt is short on purpose. First GET {{url}}/api/agent/v1/docs (same Authorization and X-ARK-Project headers): it lists ARK's documentation with a \"start here\" order. Read the docs you need when you need them with GET /api/agent/v1/docs/<path> (add ?section=<heading-slug> for one section) and follow them where they give more detail than this prompt. If /api/agent/v1/docs returns 404 (older ARK), use GET /api/agent/v1/interface and GET /api/agent/v1/skills instead.",
      zh: "文档：本提示词有意保持简短。先 GET {{url}}/api/agent/v1/docs（使用相同的 Authorization 和 X-ARK-Project 请求头）：它列出 ARK 的全部文档，并给出“从这里开始”的阅读顺序。需要时再用 GET /api/agent/v1/docs/<path> 读取相关文档（加上 ?section=<heading-slug> 只读一节）；文档比本提示词更详细的地方，以文档为准。如果 /api/agent/v1/docs 返回 404（旧版 ARK），改用 GET /api/agent/v1/interface 和 GET /api/agent/v1/skills。",
    },
  ],
  rules: [
    {
      en: "STANDING RULES: they hold for this whole session, for every task, without me reminding you. ARK is the record: work that is not in ARK did not happen.",
      zh: "长期规则：它们在本次会话的全程、对每个任务都有效，不需要我提醒。ARK 就是记录：没有写进 ARK 的工作等于没有发生。",
    },
    {
      en: "R1. Before starting work, GET /api/agent/v1/context and read the units the work concerns (GET /api/agent/v1/units/<ID>).",
      zh: "R1. 开始工作之前，先 GET /api/agent/v1/context，并阅读与这项工作相关的节点（GET /api/agent/v1/units/<ID>）。",
    },
    {
      en: "R2. After every meaningful step (a decision, a result, a finished or blocked piece of work), write it to ARK straight away, without being asked. Do not save it all for the end.",
      zh: "R2. 每完成一个有意义的步骤（一个决定、一个结果、一项完成或受阻的工作），立即写入 ARK，不要等我要求，也不要攒到最后再写。",
    },
    {
      en: "R3. Tasks: every piece of work has a task unit (POST /api/agent/v1/units with type: \"task\") whose status you keep current (POST /api/agent/v1/units/<ID>: open → active → review or blocked → done).",
      zh: "R3. 任务：每项工作都要有一个任务节点（POST /api/agent/v1/units，type: \"task\"），并随时更新它的状态（POST /api/agent/v1/units/<ID>：open → active → review 或 blocked → done）。",
    },
    {
      en: "R4. Progress: comment on the task what you did, the result and the next step (POST /api/agent/v1/units/<ID>/comments with {\"body\": \"...\"}).",
      zh: "R4. 进展：在任务上评论你做了什么、结果如何、下一步是什么（POST /api/agent/v1/units/<ID>/comments，请求体为 {\"body\": \"...\"}）。",
    },
    {
      en: "R5. Evidence: attach figures, logs, reports and result files to the unit they support (POST /api/agent/v1/media/upload for images, POST /api/agent/v1/assets/upload for other files, POST /api/agent/v1/media for links).",
      zh: "R5. 证据：把图表、日志、报告和结果文件附到它们所支撑的节点上（图片用 POST /api/agent/v1/media/upload，其他文件用 POST /api/agent/v1/assets/upload，链接用 POST /api/agent/v1/media）。",
    },
    {
      en: "R6. Decisions and plans: record decisions, new ideas, claims and next steps as units or comments, and link every new unit to the plan or task it belongs to (POST /api/agent/v1/links).",
      zh: "R6. 决定与计划：把决定、新想法、论断和下一步记录为节点或评论，并把每个新节点关联到它所属的计划或任务（POST /api/agent/v1/links）。",
    },
    {
      en: "R7. At the end of each work session, or when I stop you, post a short summary comment on the main task: done, open, blocked, next.",
      zh: "R7. 每次工作结束时，或者我叫停你时，在主任务上发一条简短的总结评论：已完成、未完成、受阻、下一步。",
    },
    {
      en: "R8. If ARK is unreachable or a write fails, tell me, keep a local note (ark-sync-notes.md in your working directory) of everything still to record, and write it to ARK as soon as ARK is reachable again.",
      zh: "R8. 如果无法连接 ARK 或写入失败，告诉我，把所有待记录的内容写进本地笔记（工作目录中的 ark-sync-notes.md），ARK 恢复可用后立即写入 ARK。",
    },
    {
      en: "R9. Store secrets only as references (env:NAME), never credential values. Before any action listed in readiness.requires_human (push, GPU dispatch, deletion, external messages...), stop and ask me.",
      zh: "R9. 密钥只以引用形式保存（env:NAME），绝不保存凭据的值。执行 readiness.requires_human 中列出的任何操作（push、GPU 派发、删除、对外发消息...）之前，先停下来问我。",
    },
  ],
};

// The two Home prompts.  Each renders START_SHARED, then its own sections
// in order, then the session block.
export const STARTER_PROMPTS = [
  {
    id: "setup",
    icon: "settings",
    sections: {
      setup: [
        {
          en: "FIRST: set up this ARK project with ARK's setup skill (GET /api/agent/v1/skills/setup). Several members may run setup; add my resources and machine paths to the shared register and keep everyone else's.",
          zh: "第一步：用 ARK 的 setup 技能（GET /api/agent/v1/skills/setup）完成本 ARK 项目的配置。可能有多位成员运行配置；把我的资源和机器路径加入共享资源清单，并保留其他人登记的内容。",
        },
        {
          en: "1. GET /api/agent/v1/setup (send \"X-ARK-Host: <stable id of this machine>\" if you are not on the ARK server). Read resources, policies.effective and readiness.needs_location. If the project layer and my layer are already confirmed and nothing changed, report that and go to NEXT. A recorded path is not proof of access: verify it before use. For resources in known_but_inaccessible, ask their contributor instead of recreating them.",
          zh: "1. GET /api/agent/v1/setup（如果你不在 ARK 服务器上，发送 \"X-ARK-Host: <本机的稳定标识>\"）。读取 resources、policies.effective 和 readiness.needs_location。如果项目层和我的个人层都已确认且没有变化，告诉我这一点，然后进入“接下来”。已登记的路径不代表你能访问：使用前先验证。known_but_inaccessible 中的资源，去问登记它的成员，不要重新创建。",
        },
        {
          en: "2. Before asking me anything, POST /api/agent/v1/setup/discover with the paths I gave (or paths: [] if none). Then ask only about missing, conflicting or changed details, one question at a time, in the areas scope, code, team, compute, commands and authority. I may answer, point you at a file or folder, say \"don't know\" (record an unknown and what it blocks) or say \"skip\" (add the area to profile.skipped). Do not crawl my disk; use only the paths I give you.",
          zh: "2. 向我提问之前，先用我给出的路径调用 POST /api/agent/v1/setup/discover（如果没有，用 paths: []）。然后只就 scope、code、team、compute、commands、authority 这几个方面中缺失、冲突或有变化的细节提问，每次只问一个。我可以直接回答、给你指一个文件或文件夹、说“不知道”（记为未知项，并注明它阻塞了什么），或说“跳过”（把该方面加入 profile.skipped）。不要遍历我的磁盘；只使用我给你的路径。",
        },
        {
          en: "3. POST /api/agent/v1/setup/draft with {resources: [...], propose: true} for my layer; drafts merge by ID. Project-wide objective and policies use {layer: \"project\", profile: {...}, propose: true}. Add this machine's location to an existing resource with POST /api/agent/v1/setup/location. Never delete another member's records.",
          zh: "3. 对我的个人层，POST /api/agent/v1/setup/draft，请求体为 {resources: [...], propose: true}；草稿按 ID 合并。项目级的目标和策略使用 {layer: \"project\", profile: {...}, propose: true}。给已有资源添加本机位置用 POST /api/agent/v1/setup/location。绝不要删除其他成员的记录。",
        },
        {
          en: "4. Show me the summaries and digests, then wait while I confirm them in ARK (Home → Review & confirm: project layer, then my layer). Only a human confirms: never confirm yourself and do not continue until I have confirmed. A project admin confirms the project layer.",
          zh: "4. 向我展示摘要和 digest，然后等待我在 ARK 中确认（首页 → 查看并确认：先项目层，再我的个人层）。只有人能确认：绝不要自己确认，在我确认之前不要继续。项目层由项目管理员确认。",
        },
        {
          en: "5. After I confirm, POST /api/agent/v1/setup/verify, then tell me what failed and how to fix it, what is now allowed or blocked, and which resources still need a location on this machine.",
          zh: "5. 我确认之后，POST /api/agent/v1/setup/verify，然后告诉我哪些检查失败、如何修复，现在哪些操作被允许或被阻止，以及哪些资源仍需要登记本机位置。",
        },
        {
          en: "During setup, do not probe GPU servers, run project commands or change repositories.",
          zh: "配置期间不要探测 GPU 服务器、不要运行项目命令，也不要修改仓库。",
        },
      ],
      next: [
        {
          en: "NEXT: in a few lines, explain to me how to use ARK for developing and managing this project: ARK keeps the plan, tasks, evidence and decisions (Home, Research, Map, Execute); you do the work and record it under the standing rules; in a new session, the Home prompt \"Start / continue research team\" loads this context again. Then offer me these options, each with the example request I can say, and wait for my choice:",
          zh: "接下来：用几句话向我说明如何用 ARK 开发和管理这个项目：ARK 保存计划、任务、证据和决定（首页、研究、地图、执行）；你负责干活，并按长期规则记录；在新会话中，首页的“启动 / 继续研究团队”提示词会重新加载这些上下文。然后把下面这些选项连同我可以说的示例请求一起给我，等我选择：",
        },
        {
          en: "- \"Plan this idea: <idea, paper or link>\": research question, 2–3 testable hypotheses, the claims that need evidence and what would settle them, the first 3–5 tasks each with one measurable outcome; show me the plan before creating units and ask before creating more than 10.",
          zh: "- “规划这个想法：<想法、论文或链接>”：研究问题、2–3 个可检验的假设、需要证据的论断以及什么证据能判定它们、最初的 3–5 个任务（每个有一个可衡量的结果）；创建节点前先把计划给我看，要创建超过 10 个节点时先问我。",
        },
        {
          en: "- \"Start a research team on <objective>\": follow the agent-team skill (GET /api/agent/v1/skills/agent-team): bounded tasks with an owner, an independent reviewer and a measurable outcome, work in .worktrees/<TASK-ID>, gates cheapest-first.",
          zh: "- “就 <研究目标> 启动研究团队”：按 agent-team 技能（GET /api/agent/v1/skills/agent-team）执行：边界明确的任务，各有负责人、独立评审者和可衡量的结果，在 .worktrees/<TASK-ID> 中工作，关卡按成本从低到高运行。",
        },
        {
          en: "- \"Give me a status check\": under 15 lines on what is running, blocked or waiting for me, which external jobs are live (verified read-only), what changed since the last report and the next 3 actions; change nothing.",
          zh: "- “检查一下项目状态”：用不超过 15 行说明什么正在运行、什么受阻或在等我处理、哪些外部作业仍在运行（以只读方式核实）、自上次汇报以来的变化以及接下来 3 个行动；不做任何修改。",
        },
        {
          en: "- \"Record what we just did\": turn the work in this conversation into task updates, comments, evidence and links in ARK.",
          zh: "- “把我们刚才做的记录下来”：把这次对话中完成的工作整理成 ARK 中的任务更新、评论、证据和关联。",
        },
        {
          en: "- \"Import my existing task folder <path>\": register it as a task_space resource, then create units for its tasks with their status and history, linked to the plan; show me the mapping before writing.",
          zh: "- “导入我现有的任务目录 <路径>”：把它登记为 task_space 资源，然后为其中的任务创建节点（带上状态和历史）并关联到计划；写入之前先把对应关系给我看。",
        },
      ],
    },
  },
  {
    id: "team",
    icon: "tasks",
    sections: {
      context: [
        {
          en: "NOW: this is a new session on an existing ARK project. Load the current research context before doing anything else:",
          zh: "现在：这是在已有 ARK 项目上开始的新会话。做任何事之前，先加载当前的研究上下文：",
        },
        {
          en: "1. GET /api/agent/v1/context: the objective, open and blocked tasks, recent activity and decisions.",
          zh: "1. GET /api/agent/v1/context：研究目标、未完成和受阻的任务、最近的动态和决定。",
        },
        {
          en: "2. GET /api/agent/v1/setup: the confirmed setup profile (repositories, commands, policies.effective, readiness). If setup is not confirmed (readiness.ready is false), tell me what is missing and suggest the Home prompt \"Set up this environment\"; do not start task work.",
          zh: "2. GET /api/agent/v1/setup：已确认的配置档案（仓库、命令、policies.effective、readiness）。如果配置尚未确认（readiness.ready 为 false），告诉我缺什么，并建议我使用首页的“配置当前环境”提示词；不要开始任务工作。",
        },
        {
          en: "3. Read the active, open and blocked tasks and the units they link to (GET /api/agent/v1/units/<ID>), with their latest comments (GET /api/agent/v1/units/<ID>/comments).",
          zh: "3. 阅读进行中、未完成和受阻的任务及其关联的节点（GET /api/agent/v1/units/<ID>），以及它们最新的评论（GET /api/agent/v1/units/<ID>/comments）。",
        },
        {
          en: "4. GET /api/agent/v1/skills/agent-team: the agent-team skill you coordinate with.",
          zh: "4. GET /api/agent/v1/skills/agent-team：你用来协调团队的 agent-team 技能。",
        },
        {
          en: "5. Summarise for me, in under 15 lines, where the project stands: objective, what is running, what is blocked or waiting for me, what changed recently and which decisions are open. Verify external jobs read-only; don't trust old notes. Change nothing yet.",
          zh: "5. 用不超过 15 行向我总结项目现状：研究目标、什么正在运行、什么受阻或在等我处理、最近有什么变化、哪些决定尚未做出。以只读方式核实外部作业，不要相信旧记录。暂时不要做任何修改。",
        },
      ],
      options: [
        {
          en: "THEN: tell me what I can do now, each option with the example request I can say, and wait for my choice:",
          zh: "然后：告诉我现在可以做什么，每个选项附上我可以说的示例请求，并等我选择：",
        },
        {
          en: "- \"Start a research team on <objective>\": bounded tasks, each with an owner, an independent reviewer and a measurable outcome; validate the manifests with POST /api/skills/agent-team/tasks using create: false, then create: true for the agreed tasks (same Authorization and X-ARK-Project headers); work in .worktrees/<TASK-ID>; run the gates cheapest-first.",
          zh: "- “就 <研究目标> 启动研究团队”：边界明确的任务，各有负责人、独立评审者和可衡量的结果；先用 POST /api/skills/agent-team/tasks 并设 create: false 校验任务清单，再对商定的任务使用 create: true（使用相同的 Authorization 和 X-ARK-Project 请求头）；在 .worktrees/<TASK-ID> 中工作；关卡按成本从低到高运行。",
        },
        {
          en: "- \"Continue <task ID>\": read the task, its comments and evidence, then resume from its last recorded next step.",
          zh: "- “继续 <任务 ID>”：阅读该任务、它的评论和证据，然后从最后记录的下一步继续。",
        },
        {
          en: "- \"Plan this idea: <idea, paper or link>\": research question, 2–3 testable hypotheses, the claims that need evidence and what would settle them, the first 3–5 tasks; show me the plan before creating units.",
          zh: "- “规划这个想法：<想法、论文或链接>”：研究问题、2–3 个可检验的假设、需要证据的论断以及什么证据能判定它们、最初的 3–5 个任务；创建节点前先把计划给我看。",
        },
        {
          en: "- \"Give me a status check\": refresh the summary above; change nothing.",
          zh: "- “检查一下项目状态”：刷新上面的总结；不做任何修改。",
        },
        {
          en: "- \"Review the results of <task ID>\": as an independent reviewer, check its evidence against its measurable outcome and record the verdict as a comment.",
          zh: "- “评审 <任务 ID> 的结果”：以独立评审者的身份，对照可衡量的结果检查它的证据，并把结论记录为评论。",
        },
        {
          en: "On my choice, act as the coordinator and follow the agent-team skill. If team execution is unavailable here, say so instead of pretending workers are running.",
          zh: "我选择后，由你担任协调者，按 agent-team 技能执行。如果这里无法以团队方式执行，直接说明，不要假装 worker 正在运行。",
        },
      ],
    },
  },
];

export function starterPromptVars(project, { url = "", token = "" } = {}) {
  return {
    url,
    token,
    project_id: project?.id || "",
    project_name: project?.name || "ARK project",
    project_id_part: project?.id ? ` (id ${project.id})` : "",
  };
}

/** One Home starter prompt; the standing rules come first, the session block last. */
export function starterPromptText(id, project, { url = "", token = "", sessionId = "", lang = "en" } = {}) {
  const item = STARTER_PROMPTS.find((p) => p.id === id);
  if (!item) return "";
  const S = START_SHARED;
  const vars = starterPromptVars(project, { url, token });
  const blocks = [
    [...S.intro, ...(token ? S.token : S.noToken), ...S.docs],
    S.rules,
    ...Object.values(item.sections),
  ].map((lines) => renderLines(lines, lang, vars));
  if (sessionId) {
    blocks.push(sessionPromptBlock({
      sessionId, url, projectId: project?.id || "", lang,
      uploads: Boolean(project?.settings?.sessions?.accept_uploads),
    }));
  }
  return `${blocks.join("\n\n")}\n`;
}
