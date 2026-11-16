# Provider catalog and agent worktrees

ARK drives local agent CLIs as managed stdio sessions. This slice adds two
honest, read-only layers around that transport:

1. a **provider catalog** that reports what is installed, what ARK can verify
   about credentials, and which capabilities are real; and
2. a **project-scoped Git worktree manager** so a coding agent can run in its
   own checkout without touching the primary working tree.

Neither layer installs software, starts a login flow or pushes/merges commits.
The only network call is the optional model-list request described under
[Model discovery](#model-discovery), which runs on an explicit refresh only.

## Provider catalog

`GET /api/providers` probes the configured task-agent presets and returns the
result. `POST /api/providers/refresh` forces a new probe (the normal response is
cached for 60 seconds). `GET /api/agent-sessions/presets` merges the same state
into each launchable preset.

| Field | Meaning |
|---|---|
| `installed` | the configured command was resolved on `PATH` or as an explicit path |
| `version` | first line of the provider's own `--version`, sanitized and bounded |
| `status` | `ready`, `unauthenticated`, `unknown`, or `missing` |
| `authenticated` | `true`, `false`, or `null` when ARK cannot verify it |
| `auth.sources` | variable names or user-relative file labels only; never values |
| `capabilities.models` | model ids (configured + detected + builtin), kept as a plain list for older readers |
| `capabilities.model_options` | `[{id, label, source, via, default?, efforts?, default_effort?}]`; `source` is `configured`, `detected` or `builtin` |
| `capabilities.models_source` | `configured`, `detected`, `builtin` or `provider_default` |
| `capabilities.models_methods` | discovery methods that returned data, e.g. `cli:codex debug models`, `config:~/.codex/config.toml`, `api:GET /models` |
| `capabilities.models_checked_at` / `models_error` / `models_detail` / `models_reason` | when the list was built, a bounded failure status, a readable explanation, and why a builtin list is used (`no_list_command`, `not_installed`, `list_failed`, `not_run`) |
| `capabilities.default_model` / `default_effort` | the provider's own default, when its config names one |
| `capabilities.efforts` | configured reasoning levels, otherwise the default model's levels; empty means unsupported |
| `capabilities.permission_modes` | configured permission modes (`read_only`, `plan`, `ask`, `accept_edits`, `full_access`) |
| `capabilities.streaming` | `line_buffered` (stdio), not token streaming |
| `capabilities.approval` | adapter-mediated stdin approvals when the preset supports them; otherwise unsupported |
| `health` | overall state plus a human-readable reason |

Detection is deliberately conservative:

| Provider | Credential sources checked (presence only) |
|---|---|
| Claude Code | `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `~/.claude/.credentials.json` |
| Codex CLI | `OPENAI_API_KEY`, `CODEX_API_KEY`, `~/.codex/auth.json` |
| DeepSeek Harness | `TENCENTHUB_API_KEY`, `DEEPSEEK_API_KEY`, `DSH_API_KEY`, ARK's private assistant key |
| OpenCode | provider key variables, `~/.local/share/opencode/auth.json`, `~/.config/opencode/auth.json`, `%APPDATA%/opencode/auth.json` |
| Cursor Agent | `CURSOR_API_KEY`; the keychain is not inspected, so the result is `unknown` |
| Gemini CLI | `GEMINI_API_KEY`, `GOOGLE_API_KEY`, `~/.gemini/oauth_creds.json` |

The version probe runs only known CLIs with `--version`, a four-second timeout,
`shell=False`, and captured output. Version text is stripped of ANSI/control
characters and secret-looking blobs before it is returned. Credential files are
parsed only to answer "is a usable-looking token present?"; no value leaves the
module and nothing is logged.

An installed CLI that ARK cannot inspect (notably DSH's own profile or Cursor's
keychain) is `unknown`, not "unauthenticated". A missing CLI is always
`missing`, even if a credential file exists.

The **Settings → Providers** panel renders this state, shows the exact reason,
and exposes a manual refresh. The launch dialog and session composer label
options with "not installed", "not signed in" or "auth unknown"; a missing
command cannot be launched.

## Model discovery

Each catalog probe also builds a model list per provider. Every method is
read-only and bounded, and none starts a model run or spends tokens.

| Provider | Methods, in order |
|---|---|
| Codex CLI | `~/.codex/config.toml` (`model`, `model_reasoning_effort`, `[profiles.*].model`); `codex debug models --bundled` on every probe; `codex debug models` (lets Codex refresh its own catalog cache) only on an explicit refresh. Per-model reasoning levels come from the catalog. |
| OpenCode | `opencode models`; `~/.config/opencode/opencode.json[c]` (`model`, `small_model`, `provider.*.models`) |
| Claude Code | `~/.claude/settings.json` (`model`, `effortLevel`, `env.ANTHROPIC_MODEL` and `ANTHROPIC_DEFAULT_*_MODEL`), then the builtin aliases `opus`/`sonnet`/`haiku`/`fable`. Claude Code has no list command. Effort levels are the ones `claude --help` documents. |
| Gemini CLI | `~/.gemini/settings.json` `model`, then builtin. There is no list command. |
| DeepSeek Harness | the ARK assistant model; on an explicit refresh, `GET {base_url}/models` on the assistant endpoint (Settings → Assistant) with its key. DSH has no list command. |
| Cursor Agent | builtin only. There is no verified list command. |

Rules:

- List commands run with `shell=False`, stdin closed, a minimal environment
  (no API keys), an 8-second timeout and an 8 MB output cap. HTTP uses a
  5-second timeout and accepts http(s) only. A timeout, non-zero exit,
  malformed output or HTTP error becomes `models_error`; it is never raised.
- Results of refresh-only methods are kept until the next refresh, so the
  60-second TTL re-probe never touches the network.
- Config readers look up the named model keys only. Tokens, base URLs and
  other values in the same files are never read into the response, and any
  value that looks like a secret is dropped even if it sits in a model key.
- Models in the preset (`ARK_TASK_<ID>_MODELS`) always win and are marked
  `configured`. Configured `ARK_TASK_<ID>_EFFORTS` likewise override the
  per-model levels.
- When no full list is available, a curated builtin list is added and
  labelled `builtin`, with the reason, so the picker is never empty.
- Session launch validates `effort` against the selected model's levels.
  Codex receives `-m <model>` and `-c model_reasoning_effort=<effort>`.
  Claude Code receives `--model` and `--effort`. Templates may also use
  `{model}` and `{effort}`.

In **Execute → Runs**, the composer's "model · effort" chip opens a picker
grouped by Configured / Detected / Built-in, with the provider default first,
plus **Custom…** for free-text ids. It shows where the list came from and how
old it is, and a refresh button that calls `POST /api/providers/refresh`. The
effort list follows the selected model. Provider, model and effort stay locked
while a session runs.

## Worktrees

`GET /api/worktrees` lists the repository, safe root, main worktree and every
managed linked worktree with its branch, HEAD, clean/dirty count, active session
ids and conflict flag.

`POST /api/worktrees/create` accepts:

```json
{"name": "feature login", "base": "main"}
```

or an explicit `{"branch": "agent/feature-login"}`. Rules:

- the open ARK project must be inside a Git repository;
- the **safe root** is `$ARK_AGENT_WORKTREE_ROOT` when set, otherwise
  `<repository-parent>/<repository-name>-worktrees` (a sibling, so the primary
  checkout stays clean);
- the base checkout must have zero uncommitted/untracked changes;
- the branch is validated against Git ref rules, defaults to `agent/<slug>`,
  and cannot be the main branch or already checked out elsewhere;
- the target folder must be new and must stay under the safe root;
- at most 12 managed worktrees are allowed.

`POST /api/worktrees/remove` requires `confirm: true` (the UI asks through a
danger dialog), refuses the main worktree and paths outside the root, and
refuses a dirty worktree unless `force: true` is also explicit. Removal only
runs `git worktree remove`; ARK never falls back to deleting a folder itself.
An active session blocks removal, and branch deletion is opt-in and
best-effort.

## Worktree sessions and parallel metadata

`POST /api/agent-sessions` accepts an optional `worktree` reference (path,
branch, or folder name). The session then starts at the worktree root instead
of its private workspace, receives `ARK_TASK_WORKTREE` and
`ARK_TASK_WORKTREE_BRANCH`, and its contract tells it it may edit that worktree
but must not push, merge, or delete it. Worktree editing is separate OS access:
the session keeps its normal permission mode (`read_only`, `plan`, `ask`,
`accept_edits`, or `full_access`). Plan mode explicitly forbids worktree edits;
approval modes keep their approval protocol and may write only in the session
workspace or the selected worktree; only `full_access` receives ARK's project
write token.

Only worktrees under the managed root are accepted. A second active session in
the same worktree is refused with `409` unless the request sends
`allow_shared_worktree: true`; shared sessions are then marked `conflict` in
their payload, in `GET /api/agent-sessions/groups`, and in the worktree listing.
`GET /api/agent-sessions` also returns `groups`, active session ids and conflict
counts. The session rail shows a parallel-group summary and can group all
sessions by worktree; the session header and **Overview** show the branch, path,
HEAD and clean-at-launch state.

## Limitations

- The catalog reports ARK's configured presets, not an exhaustive registry of
  every agent on the machine.
- Model lists and effort levels are only what the operator configured (plus the
  DSH assistant model); ARK does not invent a provider catalog.
- Credential detection is best-effort and never proves a login works. The
  catalog has no "test connection" that spends a request.
- Provider-native approval UIs are not implemented. ARK's adapter-mediated
  `ark-permission-v1` stdin protocol is the supported approval path and is only
  advertised for presets that can read a decision on stdin.
- Editor/terminal surfaces are owned by the Files workspace; interactive PTYs
  remain out of scope for the session transport.
- Worktrees require Git; ARK projects outside a repository report
  `available: false` with a human-readable reason.
- An `ARK_AGENT_WORKTREE_ROOT` inside the repository must be ignored by Git,
  otherwise it makes the base checkout dirty and later creations are refused.
