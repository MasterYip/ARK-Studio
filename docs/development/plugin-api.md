# Plugin API (workbench view plugins)

How workbench views are built as plugins, and how to write a user plugin.
User-facing guide: [View plugins](../guide/plugins.md). The permission set
follows decision D-0003.

| File | Role |
| --- | --- |
| `ark_studio/web/js/view-plugins.js` | manifest validation, the registry, the bridge methods and permission gate (pure; `tests/js/view_plugins.test.mjs`) |
| `ark_studio/web/js/plugin-api.js` | the pure parts of the newer calls: parameter checks, the `node:write` plan (validation, summary, once/session grants, applying a batch), plugin storage with its cap, event subscriptions, rate limits, what plugins see of a node and the theme (same tests) |
| `ark_studio/web/js/plugin-host.js` | registers plugins with the workbench, runs their lifecycle, the sandbox adapter, the bridge handlers and the confirmation dialogs |
| `ark_studio/web/js/plugin-bundle.js` | turns a .zip / .html / .js import into one HTML document (pure) |
| `ark_studio/web/js/plugin-store.js` | installed user plugins in IndexedDB, per user; the per-browser opt-in; the plugins' key/value data (`ark-plugin-data`) |
| `ark_studio/web/js/plugins-panel.js` | Settings → Personal → Plugins |
| `ark_studio/web/plugin-frame.html` | the sandbox host page and the in-frame `window.ark` SDK |
| `ark_studio/server/internal/server/static.go`, `respond.go` (`PluginCSP`) | the frame's Content-Security-Policy (Go only; `plugin_frame_test.go`) |
| `ark_studio/server/internal/api/chat/plugin.go`, `internal/chat/plugin.go` | `POST /api/plugins/ai/chat`, the `ai:chat` permission (Go only; `plugin_test.go`, `internal/server/plugin_chat_test.go`) |

## Manifest

```json
{
  "id": "node-word-count",
  "name": "Node word count",
  "version": "1.0.0",
  "icon": "list",
  "location": "right",
  "titles": { "en": "Word count", "zh": "字数统计" },
  "description": "Counts the words of the selected node.",
  "entry": "index.html",
  "permissions": ["selection:read", "notify"],
  "apiVersion": 1
}
```

| Field | Rule |
| --- | --- |
| `id` | 3-30 characters, `[a-z][a-z0-9-]*[a-z0-9]`; not a built-in view id, not starting with `u-`. The workbench view id is `u-<id>`. |
| `name` | 1-60 characters |
| `version` | `MAJOR.MINOR.PATCH`, optional `-tag` / `+build` |
| `icon` | an ARK icon name from `PLUGIN_ICONS` (`puzzle`, `file`, `folder`, `search`, `graph`, `info`, `list`, `layers`, `sparkles`, `terminal`, `message`, `check`, `clock`, `link`, `pen`, `filter`, `eye`); anything else shows `puzzle` |
| `location` | default container: `left`, `right` or `bottom` (default `right`) |
| `titles` | the view title per language (`en`, `zh`, …), at most 40 characters each; a plain string means `en`. Fallback: the language, its base language, `en`, any title. |
| `entry` | for a .zip: the relative path of the entry `.html` or `.js` (default `index.html`); informative for single-file bundles |
| `permissions` | any of the permissions below; an unknown name fails validation |
| `description` | optional, up to 300 characters |
| `apiVersion` | optional, must be `1` |

Unknown fields are dropped. Bundles are at most 2 MB.

## Permissions

The install dialog lists each requested permission in plain words; install
grants exactly those. Three of them still ask the user afterwards
(`PERMISSION_CONFIRM`).

| Permission | Methods (bridge) | Asks the user |
| --- | --- | --- |
| `selection:read` | `selection.get`, `media.get` | no |
| `graph:read` | `graph.snapshot`, `node.get`, `nodes.search`, `topics.list`, `topic.members` | no |
| `events` | `events.subscribe`, `events.unsubscribe` | no |
| `project:read` | `project.get` | no |
| `storage` | `storage.get`, `storage.set`, `storage.remove`, `storage.keys`, `storage.clear` | no |
| `node:open` | `node.open` | no |
| `notify` | `notify` | no |
| `clipboard:write` | `clipboard.write` | no (rate-limited) |
| `comment:write` | `comment.create` | **every comment** |
| `node:write` | `nodes.write` | **before the first write**: allow once, or for this session |
| `link:open` | `link.open` | **every link**, showing the address |
| `ai:chat` | `ai.chat` | no (rate-limited, logged in Activity) |

## Lifecycle and the registry

```js
registry.register(manifest, {
  mount(container, api) {},   // once, with the view's element
  update(state) {},           // the view was shown, or host state changed while shown
  unmount() {},               // disabled or removed
});
```

The built-in views are first-party plugins: `BUILTIN_MANIFESTS` (derived from
`VIEW_DEFAULTS` in `workbench-model.js`, the one list of built-in views)
registered by `app.js` through `registerBuiltinView(id, impl, options)`. The
panel views (Search, Changes, Local graph, Terminal) mount their element in
`mount` and refresh on `update({ reason: "show" })`; Filters, Explorer,
Details and the Assistant are rendered into their container by their owning
modules (`viewHost(id)`). `options` carries the workbench extras only
first-party views use (`applies`, `badge`, `tooltip`, `actions`, `menu`).
`api` for built-ins is `{ ctx }`.

For a user plugin the host-side implementation is the **sandbox adapter**:
`mount` creates the iframe (lazily, the first time the view is shown) and
the grant bar above it, `update` posts the state, `unmount` tells the frame
and removes it. Moving the view to another bar re-parents the iframe, which
reloads it; the adapter answers the new frame with a new channel id, drops
the old frame's event subscriptions, and the plugin starts again.

`update(state)` for user plugins receives, filtered by permission
(`pluginState`):

```js
{ visible, theme: "light" | "dark", lang, location,
  selection /* selection:read only */: { uid, title, type, status, importance, tags } | null }
```

## Writing a plugin (in the frame)

The frame defines `window.ark` before the plugin's own scripts run:

```js
window.ark.view({
  mount(container, api) {          // container: #app if present, else <body>
    container.textContent = "Hello";
  },
  update(state) {                  // see above
    if (state.visible && state.selection) api.getSelectedNode().then(show);
  },
  unmount() {},
});
```

### SDK reference

Every call returns a promise. A refused call rejects with an `Error` whose
`code` is one of `permission_denied`, `unknown_method`, `bad_params`,
`too_large`, `rate_limited`, `declined`, `busy` (another dialog is open; ARK never replaces it), `quota_exceeded`, `not_found`,
`forbidden`, `not_configured` or `failed`; server refusals also carry a
short `message`.

| `api` call | Permission | Result |
| --- | --- | --- |
| `getSelectedNode()` | `selection:read` | the node in full or `null`: `{ uid, title, type, status, importance, tags, body, media: [{ id, kind, caption, image, name }], links: { out: [...], in: [...] } }`; a link is `{ id, relation, note, uid, title, type, status }` (the other end) |
| `getMediaUrl(id)` | `selection:read` | a `blob:` URL made inside the frame from the bytes the host read (at most 10 MB); without `graph:read` only the selected node's media |
| `getGraph()` | `graph:read` | `{ nodes: [{ uid, title, type, status, importance, tags }], links: [{ source, target, relation }], truncated }` (at most 20,000 nodes) |
| `getNode(uidOrId)` | `graph:read` | the node in full, as `getSelectedNode()` |
| `searchNodes(q, { limit, type })` | `graph:read` | `{ query, nodes: [{ uid, title, type, status, importance, tags, match }] }`; `limit` 1-50 (default 20) |
| `listTopics()` | `graph:read` | `{ topics: [node] }` |
| `getTopicMembers(uid)` | `graph:read` | `{ topic, members: [node] }` |
| `on(event, fn)` / `off(event, fn)` | `events` | `on` returns an unsubscribe function; events below |
| `getProject()` | `project:read` | `{ project: { id, name }, user: { name, role, permission }, theme: { mode, vars }, lang }` |
| `applyPalette(theme)` | — | copies `theme.vars` (ARK's palette variables, e.g. `--bg`, `--ink`, `--accent`) onto the frame's document |
| `storage.get(key)` / `set(key, value)` / `remove(key)` / `keys()` / `clear()` | `storage` | JSON values; keys up to 200 characters; `set` resolves `{ used, cap }` or rejects `quota_exceeded` |
| `openNode(uid, { target })` | `node:open` | `true`; `target` `details` (default), `map` (select on the map) or `editor` |
| `notify(message, kind)` | `notify` | `true`; `kind` is `info`, `ok` or `err`; at most 200 characters |
| `copyText(text)` | `clipboard:write` | `true`; at most 100,000 characters |
| `createComment(uid, body)` | `comment:write` | `{ id }` after the user confirms; body at most 4,000 characters |
| `writeNodes(ops)` | `node:write` | `{ ok, results: [{ op, ok, uid?, id?, ref?, error? }], created: { "$ref": uid }, grant: "once" \| "session" }` |
| `createNode(fields)`, `updateNode(uid, fields)`, `addLink(source, target, relation, note)`, `removeLink(id)` | `node:write` | one-op `writeNodes` |
| `openLink(url)` | `link:open` | `true` after the user confirms; `http:`/`https:` only, no `user:password@`, at most 2,000 characters |
| `chat(messages, { system, tools })` | `ai:chat` | `{ message, proposals, reads, model }`; `messages` is a string or `[{ role: "user" \| "assistant", content }]` |

`api.manifest`, `api.permissions` and `api.state` are also available.

### Events (`events`)

| Event | Data | When |
| --- | --- | --- |
| `selection` | `{ uid }` with `selection:read`, else `{}` | the selected node changed |
| `graph` | `{ uids: [...] }` or `{ uids: null }` (anything may have changed) with `graph:read`, else `{}` | live-sync changes, merged and delivered at most every 500 ms |
| `project` | `{ id, name }` with `project:read`, else `{}` | the user switched projects (session write grants end too) |

### Storage (`storage`)

Per user, plugin and project, in this browser's IndexedDB (`ark-plugin-data`),
never on the server. Keys and values count towards a cap of 1 MB (UTF-8) per
plugin and project. Removing the plugin deletes its data.

### Writes (`node:write`)

`writeNodes(ops)` takes up to 50 operations, applied in order:

```js
api.writeNodes([
  { op: "create", ref: "$c", type: "claim", title: "One-line claim", body: "…", tags: ["summary"] },
  { op: "link", source: "$c", target: "S-0001", relation: "derived_from" },
  { op: "update", uid: "T-0012", status: "done" },
  { op: "unlink", id: 42 },
]);
```

- Fields: `title` (one line, ≤ 300), `type`, `body` (≤ 60,000), `status`,
  `importance` (`p0`-`p3`), `tags` (≤ 50). A `$ref` names a node created
  earlier in the same batch.
- Before the batch, ARK shows a summary ("New nodes: 1 · New links: 1 ·
  Changed nodes: 1", then one line per operation) with **Allow once** and
  **Allow for this session**. A session grant lasts until reload or a
  project switch; a bar above the plugin's view says so and has **Revoke**.
- Writes go through the normal ARK API with the user's own session
  (`POST /api/units`, `PATCH /api/units/{uid}`, `POST /api/links`,
  `DELETE /api/links/{id}`), so project permissions apply. Each request
  carries `X-ARK-Plugin: <id>`; the server records the actor
  `human:plugin:<id>` in Activity and node history (Go only). There is no
  way to delete nodes or projects, change members or settings, or read
  tokens.
- The batch stops at the first failing operation; `results` says which
  ones were applied.

### AI (`ai:chat`)

`chat()` sends the plugin's messages (≤ 20, ≤ 8,000 characters each,
48,000 in all, the last one from the user) and optional `system`
instructions (≤ 4,000) to `POST /api/plugins/ai/chat`. The server uses the
user's or project's effective Assistant configuration (the API key never
leaves the server) and the Assistant's tool-call model:

- read tools (`get_context`, `search_units`, `list_units`, `get_unit`) run
  against the project; `reads` lists them;
- write tools (`create_unit`, `update_unit`, `link_units`) are **not run**:
  they come back as `proposals`, already in `writeNodes` form (a proposed
  unit gets a ref `$n1`, `$n2`, … that later proposals can link to). Pass
  them to `writeNodes()` and the user confirms them as above.

No chat history is read or stored. Each call is logged in Activity
(`plugin.ai.chat`, with the plugin id, model and counts). Without a
configured model the call rejects with `not_configured`.

## The bridge protocol

All messages are plain objects sent with `postMessage(…, "*")` (the frame's
origin is opaque, so no target origin can be named; the parent identifies
the frame by `event.source`).

| Direction | `type` | Fields |
| --- | --- | --- |
| frame → host | `ark-plugin-frame` | hello from a fresh frame document |
| host → frame | `ark-plugin-load` | `channel` (random, per load), `html`, `manifest` (`id`, `name`, `version`), `granted`, `state` |
| frame → host | `ark-plugin-ready` | `channel` |
| frame → host | `ark-plugin-call` | `channel`, `id` (integer), `method`, `params` |
| host → frame | `ark-plugin-reply` | `channel`, `id`, `ok`, `result` or `error` (+ `message`) |
| host → frame | `ark-plugin-update` | `channel`, `state` |
| host → frame | `ark-plugin-event` | `channel`, `event`, `data` |
| host → frame | `ark-plugin-unmount` | `channel` |

`gateRequest(message, { granted, channel, size })` decides every call: the
type, the channel id, an integer `id`, a size limit (64 KB), the method
allow-list (`BRIDGE_METHODS`, own properties only), the permission and the
parameters. It returns the method's rate-limit bucket.

### Rate limits

Per plugin frame, sliding windows (`RATE_LIMITS`; refused calls do not
count):

| Bucket | Limit |
| --- | --- |
| every call | 20 per second |
| `ai.chat` | 3 per 10 s and 20 per minute (the server also allows 20 per minute per user and plugin, `429 RATE_LIMITED`) |
| `nodes.write` | 5 per 10 s and 30 per minute |
| `comment.create` | 5 per minute |
| `clipboard.write` | 3 per 5 s |
| `link.open` | 3 per 10 s |
| `media.get` | 10 per 10 s |
| `storage.*` | 60 per 10 s |

## Security model

| Threat | Control |
| --- | --- |
| Plugin code reads ARK's DOM, cookies, storage, tokens | Never runs in ARK's origin: `sandbox="allow-scripts"` without `allow-same-origin` (opaque origin); `PluginCSP` repeats `sandbox allow-scripts` so the page is sandboxed even opened top-level |
| Exfiltration over the network | `connect-src 'none'`, `default-src 'none'`, images/media/fonts only from `data:`/`blob:`; ARK's page CSP (`default-src 'self'`) blocks frame navigation to other sites; `form-action 'none'`. `link:open` opens a new tab only after the user confirms the shown address |
| Using ARK's API with the user's session | The frame cannot send requests (CSP) or read responses (opaque origin); the only path is the bridge, whose handlers call a fixed set of routes |
| Media | The host reads the bytes and hands a `Blob` to the frame; the frame makes its own `blob:` URL. No ARK URL, cookie or token reaches the plugin |
| Calling more than granted | Method allow-list + permission per method, checked in the host for every call; pushed state and event data filtered by permission |
| Spoofed messages | `event.source` must be the plugin's own frame; per-load random channel id |
| Writes on the user's behalf | `comment:write` confirmed per comment; `node:write` confirmed per batch or per session (summary dialog, revocable grant bar drawn outside the frame); dialog buttons stay disabled for 0.6 s so a click already under way cannot confirm; writes use the user's own permissions and are labelled `human:plugin:<id>`; no delete of nodes or projects, no members, settings or tokens |
| The AI model | The key stays on the server; model-proposed writes are only proposals; calls are rate-limited and logged |
| Flooding | 64 KB per message, per-bucket rate limits, length caps on every parameter, storage cap |
| Other users of the same browser | Plugins and their data stored per user; per-browser opt-in, off by default |

User plugins and their data are stored only in the browser (IndexedDB
`ark-plugins`, `ark-plugin-data`); no server route stores plugin code.

## Examples

- `ark_studio/web/plugins-examples/node-word-count.html`: counts the words
  of the selected node (`selection:read`, `notify`).
- `ark_studio/web/plugins-examples/assistant-plugin.html`: a minimal
  assistant. It reads the selected node, asks the model to summarise it
  into a claim linked to the node (`ai:chat`), lists the proposals and
  applies them with `writeNodes` (`node:write`; ARK asks first).

Both can be added from the Plugins page.

## Tests

`node --test --experimental-vm-modules tests/js` (`view_plugins.test.mjs`:
registry, manifest validation, bundles, the gate for every method and
permission, parameter checks, the write plan and its once/session/revoke
flow, storage cap, events, rate limits, plugin slots in the layout);
`go test ./internal/server -run 'TestPluginFrameCSP|TestPluginAIChatRoute|TestPluginWriteActor'`,
`go test ./internal/chat ./internal/api/chat` (the ai:chat loop against a
fake OpenAI-compatible endpoint).
