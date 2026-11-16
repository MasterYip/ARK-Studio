# Markdown bundles

A Markdown bundle is a ZIP of selected nodes as plain Markdown notes, with
their media copied in. Unzip it and open the folder in VS Code (`Ctrl+Shift+V`
previews a note) or as an Obsidian vault.

## Exporting

- **In the app:** right-click a node (list rows, boards, outlines, or the
  canvas graph, where the menu covers every selected node) and choose
  **Export as Markdown bundle**:
  - **With referenced nodes and media** adds the nodes the selection points to
    (one hop).
  - **Selected nodes only** exports only the selection.
- **HTTP:** `GET /api/export?format=bundle&units=F-0015,F-0016&depth=1`
  returns `application/zip`. It uses the same login, and the same
  `mobile_token` query parameter for paired devices, as the other
  `/api/export` formats. `depth` is 0–3 (default 1).
- **CLI**, for a project folder on the server:

  ```bash
  python -m arkstudio export --project /path/My.ark --format bundle \
      --units F-0015,F-0016 --depth 1 --out /tmp/
  ```

  `--out` can be a folder (the archive is named automatically) or a `.zip`
  path. `--media-root DIR` allows local attachments outside the project
  folder, as `serve --media-root` does. The command prints the archive path
  and lists any media it could not copy.

## Downloading With An Agent Token

A personal `arka_` token with `read` or `write` scope can download bundles
without signing in through a browser. Select an authorized project with
`X-ARK-Project`. The token owner's current project grant still applies.

```sh
curl --fail --get "$ARK_URL/api/agent/v1/export" \
  -H "Authorization: Bearer $ARK_TOKEN" \
  -H "X-ARK-Project: $ARK_PROJECT" \
  --data-urlencode 'format=bundle' \
  --data-urlencode 'units=F-0045' \
  --data-urlencode 'depth=1' \
  --output reply-bundle.zip
```

Use `depth=0` for the selected nodes only. Add `check=1` to get JSON counts
(`selected`, `nodes`, `files`, `missing`, `bytes`) instead of a ZIP. The agent
export endpoint also accepts `json`, `markdown`, `csv` and `svg` formats.
For personal tokens, the existing `/api/export` URL is a GET/HEAD alias.
Other browser APIs keep their existing authentication requirements.

Download an individual attachment using its media id:

```sh
curl --fail "$ARK_URL/api/agent/v1/media/17/raw?download=1" \
  -H "Authorization: Bearer $ARK_TOKEN" \
  -H "X-ARK-Project: $ARK_PROJECT" \
  --output figure.png
```

The `raw_url` returned in a unit's media list (`/api/media/17/raw`) also works
with a personal token. Media supports `HEAD` and single-range `Range` requests;
`download=1` adds an attachment disposition. Remote HTTP media returns a redirect
to its original URL; ARK does not copy remote media into bundles. Local media
must be inside the project or a server-configured media root. Missing files
return 404, and files outside allowed roots return 403.

Send tokens only in `Authorization: Bearer ...` or `X-ARK-Token`, never in a
URL. Invalid, expired and revoked tokens return 401; disabled accounts and
revoked project grants also stop downloads. The legacy shared server token
works on `/api/agent/v1/*` with its existing authentication rules; account-mode
deployments still require a browser session for that legacy token.

## What counts as referenced

Referenced nodes are the targets of `[[UID]]` mentions in a body and of the
node's outgoing links. `supersedes` links are skipped because they point at
older drafts, not supporting material. Each level of `depth` follows one more
hop. An export holds at most 2,000 nodes.

## Layout

```text
<project>-<first UID>-<date>/
  README.md        index: selected nodes, referenced nodes (and why), missing media
  manifest.json    nodes, copied files and their origins
  nodes/<UID>.md   one note per node
  media/<id>-<name> attachments (a file attached to several nodes is stored once)
  files/<path>     project files referenced by relative paths
```

Each note starts with YAML front matter (`uid`, `title`, `aliases`, `type`,
`status`, `tags`, `bundle_role`, dates) followed by the body and sections for
remaining attachments, outgoing links and links from other exported nodes.

## Rewriting rules

- `[[UID]]` becomes a relative link `[UID · Title](UID.md)` when the node is in
  the bundle. Otherwise it becomes plain `UID · Title`. Escaped `\[[UID]]` and
  code are left alone.
- `media:N` and `/api/media/N/raw` in Markdown links, images and HTML
  `src`/`href` attributes point to the copied file under `media/`.
- A line holding only a video or audio (`![caption](media:N)`) becomes a
  `<video controls>` / `<audio controls>` element plus a caption link, because
  VS Code's preview does not play media written as images.
- Relative project paths and `/api/workspace/raw?path=…` are copied under
  `files/`. Remote `http(s)` media stay remote and are listed in the README.
- Embedded HTML, including colours, is kept as written.

Media that cannot be copied (missing, or outside the allowed folders) keeps
its original reference and is listed under **Missing media** in the README.
