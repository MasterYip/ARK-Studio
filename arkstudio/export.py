"""Export the project as portable text, JSON or CSV."""
from __future__ import annotations

import csv
import io
import json
from datetime import datetime, timezone

from .store import Store
from .tags import KNOWN_TYPES, tag_namespace, tag_value


def project_json(store: Store) -> dict:
    units = store.units_with_tags()
    with store.lock:
        links = [
            {"source": row["source_id"], "target": row["target_id"], "relation": row["relation"],
             "note": row["note"], "created_at": row["created_at"]}
            for row in store.con.execute("SELECT * FROM unit_links ORDER BY id")
        ]
        media = [
            {"id": row["id"], "unit_id": row["unit_id"], "url": row["url"], "kind": row["kind"],
             "caption": row["caption"]}
            for row in store.con.execute("SELECT * FROM unit_media ORDER BY id")
        ]
        comments = [
            {"id": row["id"], "unit_id": row["unit_id"], "unit_uid": row["uid"], "author": row["author"],
             "kind": row["kind"], "body": row["body"], "created_at": row["created_at"],
             "reply_to": row["reply_to"], "resolved_at": row["resolved_at"], "resolved_by": row["resolved_by"]}
            for row in store.con.execute(
                "SELECT c.*, u.uid FROM unit_comments c JOIN units u ON u.id=c.unit_id ORDER BY c.id")
        ]
    return {
        "format": "ark-studio-export",
        "version": 1,
        "exported_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "project": store.manifest,
        "units": units,
        "links": links,
        "media": media,
        "comments": comments,
    }


def project_markdown(store: Store) -> str:
    data = project_json(store)
    manifest = data["project"]
    lines = [
        f"# {manifest.get('name', 'ARK project')}",
        "",
        f"> {manifest.get('description', '')}".rstrip(),
        "",
        f"Exported {data['exported_at']} · {len(data['units'])} units · {len(data['links'])} links",
        "",
        "## Contents",
        "",
    ]
    by_type: dict[str, list[dict]] = {}
    for unit in data["units"]:
        by_type.setdefault(unit["type"], []).append(unit)
    for type_name in sorted(by_type, key=lambda t: list(KNOWN_TYPES).index(t) if t in KNOWN_TYPES else 99):
        lines.append(f"- [{KNOWN_TYPES.get(type_name, {}).get('label', type_name)}](#{type_name}) "
                     f"({len(by_type[type_name])})")
    lines.append("")
    link_map: dict[int, list[dict]] = {}
    for link in data["links"]:
        link_map.setdefault(link["source"], []).append(link)
    uid = {u["id"]: u["uid"] for u in data["units"]}
    title = {u["id"]: u["title"] for u in data["units"]}
    for type_name in sorted(by_type, key=lambda t: list(KNOWN_TYPES).index(t) if t in KNOWN_TYPES else 99):
        lines.append(f"## {type_name}")
        lines.append("")
        for unit in by_type[type_name]:
            lines.append(f"### {unit['uid']} · {unit['title']}")
            lines.append("")
            meta = [f"`{unit['status']}`", f"`{unit['importance']}`"]
            if unit.get("start_at") or unit.get("end_at"):
                meta.append(f"{unit.get('start_at') or '—'} → {unit.get('end_at') or '—'}")
            if unit.get("source_ref"):
                meta.append(f"source `{unit['source_ref']}`")
            if unit.get("assignee"):
                meta.append(f"assignee `{unit['assignee']}`")
            lines.append(" · ".join(meta))
            lines.append("")
            if unit.get("body"):
                lines.append(unit["body"])
                lines.append("")
            tags = [t for t in unit["tags"] if tag_namespace(t) not in {"type", "status", "importance"}]
            if tags:
                lines.append("Tags: " + " ".join(f"`{t}`" for t in tags))
                lines.append("")
            for link in link_map.get(unit["id"], []):
                lines.append(f"- {link['relation']} → {uid.get(link['target'], '?')} · {title.get(link['target'], '')}")
            if link_map.get(unit["id"]):
                lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def project_csv(store: Store) -> str:
    data = project_json(store)
    buf = io.StringIO()
    writer = csv.writer(buf)
    writer.writerow(["uid", "type", "title", "status", "importance", "start_at", "end_at",
                     "tags", "origin", "source_ref", "updated_at", "body", "assignee"])
    for unit in data["units"]:
        writer.writerow([
            unit["uid"], unit["type"], unit["title"], unit["status"], unit["importance"],
            unit.get("start_at") or "", unit.get("end_at") or "",
            " ".join(unit["tags"]), unit.get("origin") or "", unit.get("source_ref") or "",
            unit.get("updated_at") or "", (unit.get("body") or "").replace("\r\n", "\n"),
            unit.get("assignee") or "",
        ])
    return buf.getvalue()


def graph_svg(store: Store, filters: dict | None = None) -> str:
    """A compact circular map for quick sharing; the app's canvas is richer."""
    data = store.graph(filters, limit=240)
    nodes = data["nodes"]
    if not nodes:
        return '<svg xmlns="http://www.w3.org/2000/svg" width="800" height="500"></svg>'
    import math
    width, height = 1200, 760
    cx, cy = width / 2, height / 2
    by_type: dict[str, list[dict]] = {}
    for node in nodes:
        by_type.setdefault(node["type"], []).append(node)
    positions: dict[int, tuple[float, float]] = {}
    radius_step = min(280, 60 + len(nodes) * 2)
    ring_index = 0
    for type_name, group in by_type.items():
        radius = 80 + ring_index * radius_step
        for i, node in enumerate(group):
            angle = (2 * math.pi * i / max(1, len(group))) + ring_index * 0.35
            positions[node["id"]] = (cx + math.cos(angle) * radius, cy + math.sin(angle) * radius)
        ring_index += 1
    colors = {key: val["color"] for key, val in KNOWN_TYPES.items()}
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
        f'viewBox="0 0 {width} {height}" font-family="Segoe UI, sans-serif">',
        '<rect width="100%" height="100%" fill="#0f141c"/>',
        f'<text x="36" y="52" fill="#e8eef6" font-size="24" font-weight="700">'
        f'{store.manifest.get("name", "ARK project")}</text>',
        f'<text x="36" y="78" fill="#93a2b6" font-size="13">'
        f'{len(nodes)} nodes · {len(data["edges"])} links · exported from ARK Studio</text>',
        '<g stroke="#2b3646" stroke-width="1.2" opacity="0.7">',
    ]
    for edge in data["edges"]:
        if edge["source"] in positions and edge["target"] in positions:
            x1, y1 = positions[edge["source"]]
            x2, y2 = positions[edge["target"]]
            parts.append(f'<line x1="{x1:.1f}" y1="{y1:.1f}" x2="{x2:.1f}" y2="{y2:.1f}"/>')
    parts.append("</g>")
    for node in nodes:
        x, y = positions[node["id"]]
        color = colors.get(node["type"], "#94a3b8")
        r = 5 + min(12, node.get("degree", 1) * 0.8)
        parts.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="{r:.1f}" fill="{color}" '
                     f'stroke="#0f141c" stroke-width="2"/>')
        if r >= 8:
            label = node["title"][:32].replace("&", "&amp;").replace("<", "&lt;")
            parts.append(f'<text x="{x + r + 4:.1f}" y="{y + 4:.1f}" fill="#c4cfdd" font-size="10">{label}</text>')
    parts.append("</svg>")
    return "\n".join(parts)
