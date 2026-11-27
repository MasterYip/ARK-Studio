# Agent messaging (experimental)

Status: experimental, T-0044. The update feed (`GET /api/agent/v1/updates`,
`ark-server agent watch`) is a regular feature; sending messages to agents is
a prototype behind a switch that is off by default
(Settings > Advanced > "Experimental: message agents", stored in the
browser).

This page covers how a person reaches an agent from ARK: what works today,
how it is built, the options for agents ARK did not start, and the security
rules.

## The problem

People answer agents in ARK: comments, Send back and Accept on review nodes,
decisions. An agent learned about them only if it happened to read the node
again. Two things fix that:

1. **The update feed** (built): the agent pulls what people did since its
   cursor, between steps or with a long poll. See
   [Watch for replies](../agents/working-mode.md#watch-for-replies).
2. **Messages** (prototype): a person writes to "my agent" directly, with or
   without a node, and sees the reply in ARK.

## What works end to end

| Path | Delivery | State |
| --- | --- | --- |
| Comment, Send back, Accept, decision, assignment, setup confirmation | `GET /api/agent/v1/updates` (long poll within about 1 s), `ark-server agent watch` | built, tested |
| Message to "any of my agents" or to one token | stored, delivered as kind `message` in the update feed; the agent acks and replies over the agent API; the reply shows in the dialog | built, tested (prototype) |
| Message to an agent ARK started (Execute) | the session's existing steer channel: written to stdin between turns for presets that accept follow-up input, else queued for the next turn | built on the existing steer route; one explicit Send click |
| Message into a workspace terminal (PTY) | not built, on purpose (see below) | rejected |
| Agents in the user's own terminal (Claude Code, Codex) | the agent runs the watcher between steps (prompt rule R6), or a client hook runs it | the prompt rule works today; hooks are documented below, not shipped |

## Design

### Update feed

- **Source.** The project's activity log. Every write already logs an
  activity row with an actor (`human:...`, an account name,
  `assistant:...`, `agent:<user>[/<name>]`, `system:...`). The feed reads
  rows after the cursor, keeps those by people, and classifies them
  (`internal/agentwatch`). Nothing is written for the feed.
- **Cursor.** `u1.<change log id>.<activity id>`, opaque to clients. The
  change log id is renewed when a black-box snapshot is restored, so a cursor
  from before a restore returns `reset: true` instead of skipping or
  repeating rows.
- **Long poll.** `wait` (at most 60 s) holds the request. The store closes a
  signal channel after every committed write (`Store.WriteSignal`, called
  from `noteWrite`), so a waiting request wakes at once and reads again after
  150 ms (Accept writes a comment and then the status; the two arrive as one
  item). A 5 s timer also re-reads, for writes by another process. A waiting
  request leaves the server's admission pool and request deadline, like the
  event stream; each token may hold 4.
- **Classification.** Review answers (`sent_back`, `accepted`, `decided`
  comments) absorb the status change that follows them. Status, assignee and
  importance changes are items only on the agent's units (created by the
  agent or its user, or assigned to the user, own or inherited). Writes made
  with any agent token are never items.

### Messages

- **Storage.** Table `agent_messages` in the project database, created on
  first use, so projects that never use messaging keep the frozen Python
  schema. Columns: sender, owner (whose agents receive it), target, unit,
  body, status, timestamps, a reply thread (JSON, at most 50 entries).
- **Lifecycle.** `queued` (sent) → `delivered` (returned to an agent by the
  feed or `GET /api/agent/v1/messages`) → `read` (`POST .../ack`) →
  `answered` (`POST .../reply`).
- **Feed entry.** Creating a message logs an `agent.message` activity row
  with the message id, owner and target but **no text** (activity is visible
  to every project member). The feed looks the message up and returns it
  only to a matching agent.
- **Targets.** `mine` (any agent token of the sender), `token:<id>` or
  `label:<label>` (one of the sender's tokens), `actor:agent:<user>[/<name>]`
  (one agent of the Agents card, by its write actor) or `host:<device id>`
  (the sender's agents whose `X-ARK-Host` is that device, a row of the Hosts
  card). In this version a person can message only their own agents: owner =
  sender.
- **Routes.** People: `GET`/`POST /api/agent-messages` (signed-in session;
  agent tokens are refused). Agents: `GET /api/agent/v1/messages`,
  `GET /api/agent/v1/messages/{id}`, `POST .../ack`, `POST .../reply`. All
  Go only (`tests/python/contract/go_only.py`).
- **UI.** With the switch on: "Message agent" on the Home connect card,
  "Message the agent working on this" in the node menu, and a right-click
  menu on the rows of the Agents and Hosts cards (Home and Board): "Message
  this agent" (a running ARK session gets a steer, an idle or ended one is
  resumed with the message, an agent working through a token gets it on its
  next check) and "Message agents on this host". The dialog lists the
  targets (any of my agents; running ARK sessions of this project, those
  linked to the node first; each of my tokens), the last messages with their
  status and replies.

### Agents that ARK started

ARK already has a supported input channel into its own agent sessions: the
steer queue (`POST /api/agent-sessions/{id}/steer`, internal/sessions
`Steer`). For presets that run with follow-up input (stream JSON on stdin)
the text is written to the process between turns; for the others it is
queued and sent with the next turn. The message dialog uses that route when
the person picks a running session, after an explicit Send. The steer item
is the record (it shows in the session's queue in Execute).

Workspace terminals are interactive PTYs. Writing a message into one types
into whatever the program shows at that moment: a half-written prompt, a
permission question ("allow this command? y/n") or a pager. That can answer
a question the person never saw, so ARK does not do it. Starting an agent
from Execute (which uses the steer channel) is the supported way.

## Agents ARK did not start

| Option | How | Feasible |
| --- | --- | --- |
| Prompt rule | R6 of both Home starter prompts: run `ark-server agent watch --once` (or poll the route) between steps, at least every 10 minutes during long work and before reporting done | today, any agent |
| Background watcher | `ark-server agent watch >> ark-updates.log &`; the agent reads the log between steps | today; the agent still has to look |
| Claude Code hooks | a project-level hook runs the watcher and hands new updates to the model (below) | today, with the user's own `.claude/settings.json`; not shipped by ARK |
| `ark-server session-watch` | uploads the agent's session JSONL to ARK at an interval | outbound only: it has no channel back into the agent, so it cannot deliver messages |
| MCP server | `ark-server mcp` (stdio) with tools such as `ark_updates`, `ark_reply`, and an `ark://updates` resource with subscriptions | later: a new command; tools are pulled by the model like the watcher, and whether a client shows resource-update notifications to the model mid-task depends on the client |
| Codex CLI | its `notify` setting runs a program after each turn, which is outbound; we found no supported way to add text to a running interactive session from outside | prompt rule today; re-check the Codex docs before building anything |

### Claude Code hooks (sketch)

Claude Code runs hook commands from `.claude/settings.json` in the project
(no system-wide install). Two events fit:

- **Stop**: when Claude is about to end its turn, a hook that exits with
  status 2 blocks the stop and its stderr is given to Claude, which then
  carries on with it. The hook input has `stop_hook_active`; return 0 when
  it is true, so Claude is not held in a loop.
- **PostToolUse**: after each tool call, a hook can add context
  (`hookSpecificOutput.additionalContext` in its JSON output).

A Stop hook wrapper (POSIX shell; the watcher keeps its own cursor file so
it does not race a background watcher):

```bash
#!/bin/sh
# .claude/hooks/ark-updates.sh: hand new ARK updates to Claude before it stops.
input=$(cat)
case "$input" in *'"stop_hook_active":true'*) exit 0 ;; esac
out=$(ark-server agent watch --once --for-you --state .ark-watch-cursor-hook 2>/dev/null)
[ -z "$out" ] && exit 0
printf 'New updates in ARK; handle them before you stop:\n%s\n' "$out" >&2
exit 2
```

```json
{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "sh .claude/hooks/ark-updates.sh"}]}]}}
```

Check the event names and output fields against the Claude Code hooks
documentation of the version in use before relying on this; ARK does not
ship or install these files.

## Security

- **Who can send.** Only signed-in project members with write access, through
  their browser session. Agent tokens cannot send messages; a read-only
  paired phone cannot either.
- **Who receives.** Only agents acting for the sender's own account (personal
  tokens of that user; the legacy server token on a local server). Other
  members' agents never see the message; the activity row carries no text.
- **Scopes.** Reading messages and the feed needs read scope; ack and reply
  need write scope.
- **Limits.** 4000 characters per message or reply; 20 messages per sender
  and project in 10 minutes (429 `MESSAGE_RATE_LIMIT`); at most 4 waiting
  update requests per token (429 `AGENT_WAIT_LIMIT`).
- **No secrets.** Messages and replies that the server's secret detector
  recognises (ARK, provider and cloud API keys, bearer tokens, private keys,
  `password=...`-style assignments) are refused
  with 400 `MESSAGE_SECRET`; the UI says not to paste them. Send references
  (`env:NAME`) instead.
- **Retention.** Messages stay in the project database (and so in its
  backups and black-box snapshots) until the project is deleted; there is no
  delete route yet.

## Open questions

- Messages to another member's agent (owner ≠ sender) need a consent model;
  not built.
- Show message replies in the node's comment thread as well as in the
  dialog?
- A server-side switch for messaging per project, beyond the browser switch.
- Remove or archive old messages.
