"""Export selected units as a portable Markdown bundle (ZIP).

Layout of the archive::

    <bundle>/README.md          index of the exported nodes
    <bundle>/manifest.json      machine-readable list of nodes and files
    <bundle>/nodes/<UID>.md     one note per node, YAML front matter + body
    <bundle>/media/<id>-<name>  ARK attachments referenced by the notes
    <bundle>/files/<path>       project files referenced by relative paths

Links use plain relative Markdown (``[F-0015 · Title](F-0015.md)``), which
VS Code's preview, Obsidian (graph and backlinks) and GitHub all follow.
``[[UID]]`` references and ``media:N`` / ``/api/media/N/raw`` URLs in bodies
are rewritten to those relative paths; videos and audio become ``<video>`` /
``<audio>`` elements because image syntax does not play them in VS Code.
"""
from __future__ import annotations

import json
import re
import zipfile
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Iterable
from urllib.parse import unquote, urlparse

from .store import Store, StoreError

MAX_UNITS = 2000
MAX_DEPTH = 3
# A "supersedes" link points at an older draft, not at supporting material.
SKIP_RELATIONS = frozenset({"supersedes"})

VIDEO_EXT = {"mp4", "webm", "mov", "m4v", "ogv"}
AUDIO_EXT = {"mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "opus"}
IMAGE_EXT = {"png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "avif"}

UID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")
WIKI_RE = re.compile(r"(\\?)\[\[([A-Za-z0-9][A-Za-z0-9_.-]{0,63})(?:\|([^\]]*))?\]\]")
# ![alt](target "title") / [label](target): ARK's renderer uses the same
# bracket rule (no nested "]" in labels).
LINK_RE = re.compile(r'(!?)\[([^\]]*)\]\(\s*(<[^>]*>|[^)\s]+)(\s+"[^"]*")?\s*\)')
HTML_ATTR_RE = re.compile(r'(\b(?:src|href|poster)\s*=\s*)(["\'])(.*?)\2', re.I)
MEDIA_RE = re.compile(r"^(?:media:(\d+)|/api/media/(\d+)/raw(?:[?#].*)?)$", re.I)
FENCE_RE = re.compile(r"^\s{0,3}(`{3,}|~{3,})")

MediaResolver = Callable[[int], Path]


class BundleError(ValueError):
    pass


def _ext(name: str) -> str:
    match = re.search(r"\.([A-Za-z0-9]+)$", str(name or "").split("?", 1)[0].split("#", 1)[0])
    return match.group(1).lower() if match else ""


def _safe_name(name: str, fallback: str = "file") -> str:
    clean = re.sub(r"[^A-Za-z0-9._-]+", "_", str(name or "")).strip("._")
    return clean[:120] or fallback


def _slug(text: str, fallback: str) -> str:
    return re.sub(r"[^A-Za-z0-9._-]+", "-", str(text or "")).strip("-.")[:60] or fallback


def _yaml_str(value) -> str:
    return json.dumps("" if value is None else str(value), ensure_ascii=False)


def _label(text: str) -> str:
    return str(text or "").replace("\\", "\\\\").replace("[", "\\[").replace("]", "\\]")


@dataclass
class _Asset:
    arcname: str          # path inside the bundle, relative to the bundle root
    source: Path | None   # local file to copy; None when missing
    kind: str             # video | audio | image | file
    origin: str           # original reference, for the manifest
    error: str = ""
    alias: bool = False   # same file as an earlier asset; not written again


@dataclass
class _Plan:
    units: dict[int, dict] = field(default_factory=dict)       # id -> hydrated unit
    order: list[int] = field(default_factory=list)
    selected: set[int] = field(default_factory=set)
    via: dict[int, str] = field(default_factory=dict)           # id -> "F-0015 supports"
    media: dict[int, _Asset] = field(default_factory=dict)
    files: dict[str, _Asset] = field(default_factory=dict)
    remote: set[str] = field(default_factory=set)


class MarkdownBundle:
    def __init__(self, store: Store, *, project_path: Path, project_name: str,
                 resolve_media: MediaResolver, base_url: str = ""):
        self.store = store
        self.project_path = Path(project_path).resolve()
        self.project_name = project_name or "ARK project"
        self.resolve_media = resolve_media
        self.base_url = base_url.rstrip("/")
        self.plan = _Plan()
        self._uid_to_id: dict[str, int] = {}
        self._media_rows: dict[int, dict | None] = {}

    # ------------------------------------------------------------ selection
    def collect(self, refs: Iterable[str | int], *, depth: int = 1) -> None:
        depth = max(0, min(MAX_DEPTH, int(depth)))
        seeds: list[int] = []
        with self.store.lock:
            for ref in refs:
                ref = str(ref).strip()
                if not ref:
                    continue
                try:
                    row = self.store._unit_row(ref)
                except StoreError as exc:
                    raise BundleError(str(exc)) from exc
                if row["id"] not in seeds:
                    seeds.append(row["id"])
            if not seeds:
                raise BundleError("select at least one node to export")
            frontier = list(seeds)
            seen = set(seeds)
            self.plan.selected = set(seeds)
            for _level in range(depth):
                nxt: list[int] = []
                for unit_id in frontier:
                    for target, reason in self._references(unit_id):
                        if target in seen:
                            continue
                        seen.add(target)
                        self.plan.via[target] = reason
                        nxt.append(target)
                        if len(seen) > MAX_UNITS:
                            raise BundleError(f"more than {MAX_UNITS} nodes; export a smaller selection")
                frontier = nxt
            ordered = seeds + [i for i in self.plan.via]
            rows = []
            for offset in range(0, len(ordered), 400):
                marks = ",".join("?" * len(ordered[offset:offset + 400]))
                rows.extend(self.store.con.execute(
                    f"SELECT * FROM units WHERE id IN ({marks})", ordered[offset:offset + 400]).fetchall())
            by_id = {row["id"]: row for row in rows}
            hydrated = self.store.hydrate([by_id[i] for i in ordered if i in by_id])
            for unit in hydrated:
                self.plan.units[unit["id"]] = unit
                self._uid_to_id[unit["uid"]] = unit["id"]
            self.plan.order = [i for i in ordered if i in self.plan.units]
            for unit_id in self.plan.order:
                unit = self.plan.units[unit_id]
                unit["_links_out"] = self.store._links_of(unit_id, outgoing=True)
                unit["_links_in"] = self.store._links_of(unit_id, outgoing=False)
                unit["_attachments"] = [dict(r) for r in self.store.con.execute(
                    "SELECT id, url, kind, caption FROM unit_media WHERE unit_id=? ORDER BY id", (unit_id,))]

    def _references(self, unit_id: int) -> list[tuple[int, str]]:
        row = self.store.con.execute("SELECT uid, body FROM units WHERE id=?", (unit_id,)).fetchone()
        out: list[tuple[int, str]] = []
        for match in WIKI_RE.finditer(row["body"] or ""):
            if match.group(1):
                continue
            target = self.store.con.execute("SELECT id FROM units WHERE uid=?", (match.group(2),)).fetchone()
            if target:
                out.append((target["id"], f"{row['uid']} mentions"))
        for link in self.store.con.execute(
                "SELECT target_id, relation FROM unit_links WHERE source_id=? ORDER BY id", (unit_id,)):
            if link["relation"] not in SKIP_RELATIONS:
                out.append((link["target_id"], f"{row['uid']} {link['relation']}"))
        return out
    # --------------------------------------------------------------- assets
    def _media_asset(self, media_id: int) -> _Asset | str | None:
        """Bundle asset for an ARK attachment, a remote URL string, or None."""
        if media_id in self.plan.media:
            return self.plan.media[media_id]
        row = self.store.media_row(media_id)
        if not row:
            return None
        url = str(row["url"] or "")
        if re.match(r"^https?://", url, re.I):
            self.plan.remote.add(url)
            return url
        name = Path(unquote(url.split("?", 1)[0])).name
        ext = _ext(name)
        kind = str(row["kind"] or "").lower()
        if kind not in {"video", "audio", "image"}:
            kind = ("video" if ext in VIDEO_EXT else "audio" if ext in AUDIO_EXT
                    else "image" if ext in IMAGE_EXT else "file")
        asset = _Asset(f"media/{media_id}-{_safe_name(name, f'media-{media_id}')}", None, kind, f"media:{media_id}")
        try:
            asset.source = self.resolve_media(media_id)
        except Exception as exc:  # outside the allowed roots, missing file, ...
            asset.error = str(getattr(exc, "message", "") or exc) or exc.__class__.__name__
        if asset.source is not None:
            # The same file attached to several nodes is stored once.
            same = next((a for a in self.plan.media.values() if a.source == asset.source and not a.alias), None)
            if same:
                asset.arcname, asset.alias = same.arcname, True
        self.plan.media[media_id] = asset
        return asset

    def _file_asset(self, rel: str) -> _Asset | None:
        parts = []
        for part in str(rel).replace("\\", "/").split("/"):
            if not part or part == ".":
                continue
            if part == "..":
                if not parts:
                    return None
                parts.pop()
            else:
                parts.append(part)
        if not parts:
            return None
        key = "/".join(parts)
        if key in self.plan.files:
            return self.plan.files[key]
        target = (self.project_path / key).resolve()
        if target != self.project_path and self.project_path not in target.parents:
            return None
        if not target.is_file():
            return None
        ext = _ext(target.name)
        kind = ("video" if ext in VIDEO_EXT else "audio" if ext in AUDIO_EXT
                else "image" if ext in IMAGE_EXT else "file")
        asset = _Asset("files/" + "/".join(_safe_name(p) for p in parts), target, kind, key)
        self.plan.files[key] = asset
        return asset

    def _rewrite_url(self, raw: str) -> tuple[str, str]:
        """(new url relative to nodes/, kind) for a URL written in a body."""
        url = raw.strip()
        if url.startswith("<") and url.endswith(">"):
            url = url[1:-1].strip()
        media = MEDIA_RE.match(url)
        if media:
            asset = self._media_asset(int(media.group(1) or media.group(2)))
            if isinstance(asset, str):
                return asset, _kind_of(asset)
            if asset is None or asset.source is None:
                return raw, asset.kind if asset else ""
            return "../" + asset.arcname, asset.kind
        if re.match(r"^[a-z][a-z0-9+.-]*:", url, re.I) or url.startswith(("#", "//")):
            return raw, _kind_of(url) if re.match(r"^https?://", url, re.I) else ""
        if url.startswith("/api/workspace/raw"):
            match = re.search(r"[?&]path=([^&#]*)", url)
            asset = self._file_asset(unquote(match.group(1))) if match else None
            return ("../" + asset.arcname, asset.kind) if asset else (raw, "")
        if url.startswith("/"):
            return (self.base_url + url if self.base_url else raw), ""
        path, _, suffix = url.partition("#")
        asset = self._file_asset(unquote(path.split("?", 1)[0]))
        if not asset:
            return raw, ""
        return "../" + asset.arcname + (f"#{suffix}" if suffix else ""), asset.kind

    # ------------------------------------------------------------ rendering
    def _node_link(self, uid: str, label: str | None = None) -> str:
        unit_id = self._uid_to_id.get(uid)
        unit = self.plan.units.get(unit_id) if unit_id else None
        if unit:
            text = label or f"{uid} · {unit['title']}"
            return f"[{_label(text)}]({uid}.md)"
        title = None
        row = self.store.con.execute("SELECT title FROM units WHERE uid=?", (uid,)).fetchone()
        if row:
            title = row["title"]
        text = label or (f"{uid} · {title}" if title else uid)
        if self.base_url and row:
            return f"[{_label(text)}]({self.base_url}/?unit={uid})"
        return text

    def _rewrite_inline(self, text: str) -> str:
        # Leave `code spans` untouched.
        pieces = re.split(r"(`+[^`]*`+)", text)
        out = []
        for piece in pieces:
            if piece.startswith("`"):
                out.append(piece)
                continue
            piece = WIKI_RE.sub(lambda m: m.group(0) if m.group(1)
                                else self._node_link(m.group(2), (m.group(3) or "").strip() or None), piece)
            piece = LINK_RE.sub(self._rewrite_link, piece)
            piece = HTML_ATTR_RE.sub(self._rewrite_attr, piece)
            out.append(piece)
        return "".join(out)

    def _rewrite_attr(self, match: re.Match) -> str:
        raw = match.group(3).replace("&amp;", "&")
        url, _kind = self._rewrite_url(raw)
        if url == raw:
            return match.group(0)
        return f"{match.group(1)}{match.group(2)}{_html_attr(url)}{match.group(2)}"

    def _rewrite_link(self, match: re.Match) -> str:
        bang, label, target, title = match.group(1), match.group(2), match.group(3), match.group(4) or ""
        url, kind = self._rewrite_url(target)
        if " " in url and not url.startswith("<"):
            url = f"<{url}>"
        return f"{bang}[{label}]({url}{title})"

    def _rewrite_body(self, body: str) -> str:
        lines = str(body or "").replace("\r\n", "\n").split("\n")
        out: list[str] = []
        fence = ""
        for line in lines:
            marker = FENCE_RE.match(line)
            if fence:
                out.append(line)
                if marker and marker.group(1)[0] == fence[0] and len(marker.group(1)) >= len(fence) \
                        and not line.strip()[len(marker.group(1)):].strip():
                    fence = ""
                continue
            if marker:
                fence = marker.group(1)
                out.append(line)
                continue
            player = self._player_line(line)
            out.append(player if player is not None else self._rewrite_inline(line))
        return "\n".join(out)

    def _player_line(self, line: str) -> str | None:
        """A line holding only ![caption](video) becomes a playable element."""
        match = re.fullmatch(r"(\s*(?:>\s*)*)" + LINK_RE.pattern + r"\s*", line)
        if not match:
            return None
        prefix, label, target = match.group(1), match.group(3), match.group(4)
        url, kind = self._rewrite_url(target)
        if kind not in {"video", "audio"}:
            return None
        src = _html_attr(url)
        tag = (f'<video controls preload="metadata" src="{src}" style="max-width:100%"></video>'
               if kind == "video" else f'<audio controls preload="metadata" src="{src}"></audio>')
        link = f"<{url}>" if " " in url else url
        caption = f"*{label}* ([open]({link}))" if label else f"[open]({link})"
        return f"{prefix}{tag}\n{prefix}\n{prefix}{caption}"
    def _note(self, unit: dict, exported_at: str) -> str:
        uid = unit["uid"]
        role = "selected" if unit["id"] in self.plan.selected else "referenced"
        tags = [tag for tag in unit.get("tags") or []
                if not tag.startswith(("type:", "status:", "importance:"))]
        front = [
            "---",
            f"uid: {_yaml_str(uid)}",
            f"title: {_yaml_str(unit['title'])}",
            f"aliases: [{_yaml_str(unit['title'])}]",
            f"type: {_yaml_str(unit['type'])}",
            f"status: {_yaml_str(unit['status'])}",
            f"importance: {_yaml_str(unit.get('importance'))}",
            f"tags: [{', '.join(_yaml_str(re.sub(r'[^A-Za-z0-9_/-]+', '-', t)) for t in tags)}]",
            f"bundle_role: {role}",
        ]
        if role == "referenced" and unit["id"] in self.plan.via:
            front.append(f"referenced_by: {_yaml_str(self.plan.via[unit['id']])}")
        for key in ("assignee", "start_at", "end_at", "source_ref", "created_at", "updated_at"):
            if unit.get(key):
                front.append(f"{key}: {_yaml_str(unit[key])}")
        front += [f"ark_project: {_yaml_str(self.project_name)}", f"exported_at: {_yaml_str(exported_at)}", "---", ""]
        meta = [f"`{unit['type']}`", f"`{unit['status']}`"]
        if unit.get("importance"):
            meta.append(f"`{unit['importance']}`")
        if role == "referenced" and unit["id"] in self.plan.via:
            meta.append(f"referenced by {self._via_text(unit['id'])}")
        lines = front + [f"# {uid} · {unit['title']}", "", " · ".join(meta) + " · [Index](../README.md)", ""]
        body = self._rewrite_body(unit.get("body") or "")
        if body.strip():
            lines += [body.rstrip(), ""]
        attachments = [m for m in unit["_attachments"]
                       if not re.search(rf"\b(?:media:{m['id']}|/api/media/{m['id']}/raw)\b", unit.get("body") or "")]
        if attachments:
            lines += ["## Attachments", ""]
            for media in attachments:
                url, kind = self._rewrite_url(f"media:{media['id']}")
                caption = media.get("caption") or Path(str(media.get("url") or "")).name or f"media {media['id']}"
                if url.startswith("media:"):
                    lines += [f"- {caption} (missing)"]
                elif kind in {"video", "audio"}:
                    lines += [self._player_line(f"![{caption}](media:{media['id']})") or f"- [{caption}]({url})", ""]
                elif kind == "image":
                    lines += [f"![{_label(caption)}]({url})", ""]
                else:
                    lines += [f"- [{_label(caption)}]({url})"]
            if lines[-1]:
                lines.append("")
        if unit["_links_out"]:
            lines += ["## Links", ""]
            for link in unit["_links_out"]:
                note = f" — {link['note']}" if link.get("note") else ""
                lines.append(f"- {link['relation']} → {self._node_link(link['target']['uid'])}{note}")
            lines.append("")
        backlinks = [l for l in unit["_links_in"] if l["source"]["uid"] in self._uid_to_id]
        if backlinks:
            lines += ["## Linked from", ""]
            for link in backlinks:
                lines.append(f"- {self._node_link(link['source']['uid'])} {link['relation']} this")
            lines.append("")
        return "\n".join(lines).rstrip() + "\n"

    def _via_text(self, unit_id: int) -> str:
        source_uid, _, relation = self.plan.via[unit_id].partition(" ")
        return f"{self._node_link(source_uid, source_uid)} ({relation})"

    def _readme(self, exported_at: str, root: str) -> str:
        selected = [self.plan.units[i] for i in self.plan.order if i in self.plan.selected]
        referenced = [self.plan.units[i] for i in self.plan.order if i not in self.plan.selected]
        media = [a for a in self.plan.media.values() if not a.alias] + list(self.plan.files.values())
        missing = [a for a in media if a.source is None]
        lines = [
            f"# {self.project_name} · Markdown bundle", "",
            f"Exported {exported_at} · {len(selected)} selected nodes · {len(referenced)} referenced nodes · "
            f"{len(media) - len(missing)} media files", "",
            "Open this folder in VS Code and press `Ctrl+Shift+V` on any note to preview it, "
            "or open the folder as an Obsidian vault. Links between notes are relative Markdown links.", "",
            "## Selected nodes", "",
        ]
        lines += [f"- [{_label(u['uid'] + ' · ' + u['title'])}](nodes/{u['uid']}.md) · `{u['type']}` · `{u['status']}`"
                  for u in selected]
        if referenced:
            lines += ["", "## Referenced nodes", ""]
            for unit in referenced:
                source_uid, _, relation = self.plan.via.get(unit["id"], "").partition(" ")
                lines.append(f"- [{_label(unit['uid'] + ' · ' + unit['title'])}](nodes/{unit['uid']}.md) · "
                             f"`{unit['type']}` · via {source_uid} {relation}".rstrip())
        if missing:
            lines += ["", "## Missing media", "",
                      "These references could not be copied; the notes keep their original target.", ""]
            lines += [f"- `{a.origin}`: {a.error or 'not found'}" for a in missing]
        if self.plan.remote:
            lines += ["", "## Remote media (not downloaded)", ""]
            lines += [f"- <{url}>" for url in sorted(self.plan.remote)]
        return "\n".join(lines).rstrip() + "\n"

    def summary(self) -> dict:
        """Counts and media size for a collected selection, without building the ZIP."""
        with self.store.lock:
            for unit_id in self.plan.order:
                unit = self.plan.units[unit_id]
                self._rewrite_body(unit.get("body") or "")
                for media in unit["_attachments"]:
                    self._rewrite_url(f"media:{media['id']}")
        assets = [a for a in self.plan.media.values() if not a.alias] + list(self.plan.files.values())
        present = [a for a in assets if a.source is not None]
        return {
            "nodes": len(self.plan.order), "selected": len(self.plan.selected),
            "files": len(present), "missing": len(assets) - len(present),
            "bytes": sum(a.source.stat().st_size for a in present),
        }

    # ---------------------------------------------------------------- write
    def write_zip(self, target, *, root: str | None = None) -> dict:
        """Write the bundle to a path or binary file object; returns the manifest."""
        if not self.plan.order:
            raise BundleError("nothing to export; call collect() first")
        exported_at = datetime.now(timezone.utc).isoformat(timespec="seconds")
        first = self.plan.units[self.plan.order[0]]
        root = root or "-".join(filter(None, [
            _slug(self.project_name, "ark"), _slug(first["uid"], "nodes"),
            datetime.now().strftime("%Y%m%d-%H%M")]))
        with self.store.lock:
            notes = {self.plan.units[i]["uid"]: self._note(self.plan.units[i], exported_at) for i in self.plan.order}
        readme = self._readme(exported_at, root)
        assets = [a for a in self.plan.media.values() if not a.alias] + list(self.plan.files.values())
        manifest = {
            "format": "ark-markdown-bundle", "version": 1, "exported_at": exported_at,
            "project": self.project_name, "root": root,
            "nodes": [{"uid": self.plan.units[i]["uid"], "title": self.plan.units[i]["title"],
                       "type": self.plan.units[i]["type"], "status": self.plan.units[i]["status"],
                       "role": "selected" if i in self.plan.selected else "referenced",
                       "via": self.plan.via.get(i), "path": f"nodes/{self.plan.units[i]['uid']}.md"}
                      for i in self.plan.order],
            "files": [{"path": a.arcname, "origin": a.origin, "kind": a.kind,
                       "size": a.source.stat().st_size if a.source else None,
                       **({"error": a.error or "not found"} if a.source is None else {})} for a in assets],
            "remote": sorted(self.plan.remote),
        }
        with zipfile.ZipFile(target, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            archive.writestr(f"{root}/README.md", readme)
            archive.writestr(f"{root}/manifest.json", json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
            for uid, text in notes.items():
                archive.writestr(f"{root}/nodes/{uid}.md", text)
            for asset in assets:
                if asset.source is None:
                    continue
                packed = _ext(asset.arcname) in VIDEO_EXT | AUDIO_EXT | IMAGE_EXT - {"svg", "bmp"}
                archive.write(asset.source, f"{root}/{asset.arcname}",
                              compress_type=zipfile.ZIP_STORED if packed else zipfile.ZIP_DEFLATED)
        return manifest


def _kind_of(url: str) -> str:
    ext = _ext(url)
    return "video" if ext in VIDEO_EXT else "audio" if ext in AUDIO_EXT else "image" if ext in IMAGE_EXT else ""


def _html_attr(value: str) -> str:
    return str(value).replace("&", "&amp;").replace('"', "&quot;").replace("<", "&lt;")


def local_media_resolver(store: Store, project_path: Path, roots: Iterable[Path] = ()) -> MediaResolver:
    """Resolve attachments the way the server does, for offline (CLI) exports."""
    project_path = Path(project_path).resolve()
    allowed = [project_path] + [Path(r).expanduser().resolve() for r in roots]

    def resolve(media_id: int) -> Path:
        row = store.media_row(media_id)
        if not row:
            raise BundleError("media not found")
        url = str(row["url"] or "")
        if url.lower().startswith("file://"):
            raw = unquote(urlparse(url).path)
            if re.match(r"^/[A-Za-z]:/", raw):
                raw = raw[1:]
            path = Path(raw)
        else:
            path = Path(url)
            if not path.is_absolute():
                path = project_path / path
        path = path.expanduser().resolve()
        if not any(path == root or root in path.parents for root in allowed):
            raise BundleError("local media outside the project folder")
        if not path.is_file():
            raise BundleError(f"media file not found: {path.name}")
        return path

    return resolve


def parse_refs(value: str | Iterable[str]) -> list[str]:
    items = re.split(r"[\s,]+", value) if isinstance(value, str) else list(value)
    refs = [str(item).strip() for item in items if str(item).strip()]
    bad = [ref for ref in refs if not (ref.isdigit() or UID_RE.match(ref))]
    if bad:
        raise BundleError(f"invalid node reference: {bad[0]}")
    if len(refs) > MAX_UNITS:
        raise BundleError(f"at most {MAX_UNITS} nodes per export")
    return refs


def bundle_filename(manifest: dict) -> str:
    return f"{manifest['root']}.zip"
