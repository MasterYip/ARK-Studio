# Chat and the DeepSeek Harness adapter

ARK's **Chat** tab is a project-scoped agent surface. It can read and write the
current project through the same `Store`/HTTP API that the rest of the UI uses.

Open it with the **Chat** tab or **Alt+J** from any view.

## Default: local ARK harness

No model key, network call, or extra process is required. The local harness is
deterministic and deliberately narrow. It maps a message to project tools:

| Message | Tool |
|---|---|
| `Create idea: A learned recovery policy \| Test it...` | `create_unit` |
| `link T-0001 supports C-0001` | `link_units` |
| `mark T-0001 done` | `update_unit` |
| `search scaling` | `search_units` |
| `dispatch T-0001` | `dispatch_task` |
| `summary` / `What should I do next?` | project summary + next actions |

Every write is a normal ARK activity (`assistant:chat`), visible in the graph and
the project activity feed. Chat messages are persisted in `chat_messages` in the
project's `ark.sqlite3`, so they travel with the `.ark` folder.

This local harness is intentionally not an LLM. It will not invent a general
answer when no tool matches; it explains the available commands instead.

## Optional: delegate turns to DeepSeek Harness (DSH)

ARK ships an adapter for a DSH headless process. Install the DSH CLI from
<https://github.com/deepseek-ai/deepseek-harness> (for example
`npx @deepseek-ai/dsh`), configure its model credentials in a DSH home, then
start ARK with a command:

```powershell
$env:ARK_DSH_COMMAND = "dsh headless -"
$env:ARK_DSH_TIMEOUT = "180"
ark-server serve --port 8890
```

`ARK_DSH_COMMAND` is split with shell-like quoting and run with the `.ark`
project folder as `cwd`. The prompt includes the project name, path, ARK API base,
and the agent token. DSH therefore gets a coding-agent surface: it can edit files
and call the token-protected ARK API.

If the command is not on `PATH` or exits non-zero, the turn falls back to the
local harness. The Chat sidebar reports which harness is active.

### Why this is not vendored as a submodule

DSH is a large Cordis/pnpm monorepo with native/runtime packaging; adding it as a
submodule would not make the Python ARK server able to run it without a full
build. ARK keeps the boundary at a headless process instead. If you want to
vendor a checked-out DSH for reproducible deployment, use a submodule and point
the command at its built CLI:

```powershell
git submodule add https://github.com/deepseek-ai/deepseek-harness vendor/dsh
# build it separately, then:
$env:ARK_DSH_COMMAND = "C:\path\to\deepseek-harness\dsh.cmd headless -"
```

The adapter is deliberately opt-in: setting `ARK_DSH_COMMAND` executes a local
command, so only use a trusted command and a trusted DSH profile.

## HTTP endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/chat` | persisted messages, harness status, project summary |
| `POST` | `/api/chat` | append a user message and return the assistant message/tools |
| `DELETE` | `/api/chat` | clear the conversation (project nodes are untouched) |
| `GET` | `/api/chat/status` | harness mode/label/availability |
