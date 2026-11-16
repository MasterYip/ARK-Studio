"""Academic design protocol and theme validation, adapted for ARK artifacts.

Adapted from the standalone ``academic-design-workflow`` skill (MIT): one
versioned theme is the single source of truth for colour, typography, shape,
spacing, charts, web and motion; downstream artifacts consume semantic tokens
instead of local magic values; outputs are verified at delivery size.

ARK has no YAML/pydantic/Matplotlib dependency by design, so this module
implements the same contract with the standard library and JSON as the
canonical on-disk format.  If PyYAML happens to be installed, ``.yaml`` themes
load too, but they are optional.  The schema checks the vendor invariants that
actually protect an ARK artifact:

* semantic role names, hex validity and opacity bounds;
* shape/stroke references resolve to declared roles;
* text contrast and grayscale survival;
* image/SVG/PDF magic bytes and in-project asset paths;
* external-reference and empty-file checks for generated figures;
* the shared/paper/website/video quality gates as data a reviewer can tick.
"""
from __future__ import annotations

import json
import re
from pathlib import Path, PurePosixPath
from typing import Any, Mapping
from xml.etree import ElementTree

SKILL_ID = "design"
TEMPLATES = ("theme.json", "design_brief.md", "style_review.md")

HEX_RE = re.compile(r"^#(?:[0-9A-Fa-f]{3}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$")
VARIANTS = ("paper", "web", "video")
GEOMETRY = {
    "rectangle", "rounded_rectangle", "capsule", "circle", "line",
    "trapezoid", "cylinder", "layered_cylinder", "clipped_header",
    "rounded_top_rectangle",
}
STROKE_STYLES = {"solid", "dashed", "dotted", "dashdot"}
CAPS = {"butt", "round", "projecting"}
JOINS = {"miter", "round", "bevel"}
EMPHASIS = {"quiet", "normal", "strong"}
DENSITY = {"compact", "balanced", "open"}
ALIGNMENT = {"optical", "geometric"}
READING_ORDER = {"left_to_right", "top_to_bottom", "radial"}
REDUCED_MOTION = {"remove", "crossfade", "shorten"}
CASING = {"none", "uppercase", "lowercase", "title"}

REQUIRED_COLOR_ROLES = (
    "surface",
    "surface_alt",
    "text_primary",
    "text_secondary",
    "data_primary",
    "data_secondary",
    "baseline",
    "uncertainty",
    "success",
    "warning",
    "failure",
    "focus",
)

QUALITY_GATES = {
    "shared": [
        "Theme validates and artifacts name its version.",
        "No unexplained local colors, opacity, font sizes, radii, or motion curves.",
        "Hierarchy survives grayscale and meaning does not rely only on hue.",
        "Text, equations, labels, and annotations remain readable at delivery size.",
    ],
    "paper": [
        "Data transformation and uncertainty are auditable.",
        "Axes, scales, legends, units, and panel labels are unambiguous.",
        "SVG/PDF is unclipped, fonts render correctly, and final-width print works.",
    ],
    "website": [
        "Keyboard focus, headings, alternative text, contrast, and reduced motion pass.",
        "Mobile order is preserved and media does not cause layout shift.",
    ],
    "video": [
        "Safe area, captions, pacing, muted comprehension, and end card pass.",
        "Resolution, frame rate, duration, and encoded playback match the target.",
    ],
}

ASSET_SUFFIXES = {".svg", ".pdf", ".png", ".jpg", ".jpeg", ".webp", ".html", ".css"}
SOURCE_FILE_SUFFIXES = {".py", ".json", ".yaml", ".yml", ".toml", ".csv", ".tsv"}
EXTERNAL_REF_RE = re.compile(r"""(?:href|src|xlink:href)\s*=\s*["'](https?://[^"']+)["']""",
                             re.IGNORECASE)


class DesignError(ValueError):
    """A theme or artifact violates the ARK design protocol."""


def _text(value: Any, name: str) -> str:
    text = str(value or "").strip()
    if not text:
        raise DesignError(f"{name} is required")
    return text


def parse_color(value: Any, name: str) -> str:
    text = str(value or "").strip().upper()
    if not HEX_RE.match(text):
        raise DesignError(f"{name} must use #RGB, #RRGGBB, or #RRGGBBAA")
    return text


def _luminance(hex_color: str) -> float:
    value = parse_color(hex_color, "color").lstrip("#")
    if len(value) == 3:
        value = "".join(char * 2 for char in value)
    channels = [int(value[index:index + 2], 16) / 255.0 for index in (0, 2, 4)]
    linear = [channel / 12.92 if channel <= 0.04045
              else ((channel + 0.055) / 1.055) ** 2.4 for channel in channels]
    return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2]


def contrast_ratio(foreground: str, background: str) -> float:
    """WCAG 2.x contrast ratio between two hex colours."""
    a, b = _luminance(foreground), _luminance(background)
    lighter, darker = max(a, b), min(a, b)
    return (lighter + 0.05) / (darker + 0.05)


def _mapping(value: Any, name: str, *, required: bool = True) -> dict:
    if value is None and not required:
        return {}
    if not isinstance(value, Mapping):
        raise DesignError(f"{name} must be an object")
    return dict(value)


def _number(value: Any, name: str, *, minimum: float | None = None,
            maximum: float | None = None) -> float:
    if isinstance(value, bool):
        raise DesignError(f"{name} must be a number")
    try:
        number = float(value)
    except (TypeError, ValueError) as exc:
        raise DesignError(f"{name} must be a number") from exc
    if number != number or number in (float("inf"), float("-inf")):
        raise DesignError(f"{name} must be finite")
    if minimum is not None and number < minimum:
        raise DesignError(f"{name} must be >= {minimum:g}")
    if maximum is not None and number > maximum:
        raise DesignError(f"{name} must be <= {maximum:g}")
    return number


def _integer(value: Any, name: str, *, minimum: int | None = None) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise DesignError(f"{name} must be an integer")
    if minimum is not None and value < minimum:
        raise DesignError(f"{name} must be >= {minimum}")
    return value


def _enum(value: Any, name: str, allowed: set[str] | tuple[str, ...]) -> str:
    text = str(value or "").strip().lower()
    if text not in allowed:
        raise DesignError(f"{name} must be one of: {', '.join(sorted(allowed))}")
    return text


def _string_list(value: Any, name: str, *, required: bool = True) -> list[str]:
    if value is None:
        if required:
            raise DesignError(f"{name} must be a list of strings")
        return []
    if not isinstance(value, (list, tuple)):
        raise DesignError(f"{name} must be a list of strings")
    items = [str(item).strip() for item in value if str(item).strip()]
    if required and not items:
        raise DesignError(f"{name} must contain at least one entry")
    return items


def deep_merge(base: Mapping[str, Any], override: Mapping[str, Any]) -> dict:
    """Merge mappings recursively; lists and scalars replace the parent value."""
    result = dict(base)
    for key, value in override.items():
        if isinstance(value, Mapping) and isinstance(result.get(key), Mapping):
            result[key] = deep_merge(result[key], value)
        else:
            result[key] = value
    return result


def _resolve_variant(theme: Mapping[str, Any], variant: str | None) -> dict:
    document = dict(theme or {})
    variants = document.pop("variants", {}) or {}
    if not variant:
        document["variants"] = variants
        return document
    if not isinstance(variants, Mapping):
        raise DesignError("variants must be an object")
    override = variants.get(variant)
    if override is None:
        document["variants"] = variants
        return document
    if not isinstance(override, Mapping):
        raise DesignError(f"variant {variant!r} must be an object")
    resolved = deep_merge(document, override)
    resolved["variants"] = variants
    return resolved


def validate_theme(theme: Mapping[str, Any], *, variant: str | None = None) -> dict:
    """Validate the full theme contract; returns diagnostics, never raises."""
    errors: list[str] = []
    warnings: list[str] = []
    if not isinstance(theme, Mapping):
        return {
            "ok": False,
            "errors": ["theme must be an object"],
            "warnings": [],
            "theme": {},
            "variant": variant or "base",
            "quality_gates": QUALITY_GATES,
        }
    try:
        document = _resolve_variant(theme, variant)
        _validate_document(document, errors, warnings)
    except DesignError as exc:
        errors.append(str(exc))
        document = dict(theme or {})
    if variant and variant not in VARIANTS:
        warnings.append(f"unknown variant {variant!r}; validated the base theme instead")
    return {
        "ok": not errors,
        "errors": errors,
        "warnings": warnings,
        "theme": document,
        "variant": variant or "base",
        "quality_gates": QUALITY_GATES,
    }


def _validate_document(document: Mapping[str, Any], errors: list[str],
                       warnings: list[str]) -> None:
    if not isinstance(document, Mapping):
        raise DesignError("theme must be an object")
    allowed = {"meta", "color", "typography", "shape", "spacing", "layout",
               "chart", "web", "motion", "video", "variants"}
    unknown = sorted(set(document) - allowed)
    for key in unknown:
        errors.append(f"unknown theme system: {key}")
    for key in ("meta", "color", "typography", "shape", "spacing", "layout",
                "chart", "web", "motion", "video"):
        if key not in document:
            errors.append(f"missing theme system: {key}")
    if errors:
        return

    meta = _mapping(document["meta"], "meta")
    _text(meta.get("name"), "meta.name")
    _integer(meta.get("version"), "meta.version", minimum=1)
    _text(meta.get("description"), "meta.description")
    _string_list(meta.get("intent"), "meta.intent")
    _string_list(meta.get("avoid"), "meta.avoid", required=False)

    color = _mapping(document["color"], "color")
    roles = _mapping(color.get("roles"), "color.roles")
    for role in REQUIRED_COLOR_ROLES:
        if role not in roles:
            errors.append(f"missing semantic color role: {role}")
    for role, token in roles.items():
        if not re.match(r"^[a-z][a-z0-9_]*$", str(role)):
            errors.append(f"color role {role!r} must be semantic (lowercase snake_case)")
            continue
        token = _mapping(token, f"color.roles.{role}")
        parse_color(token.get("value"), f"color.roles.{role}.value")
        _number(token.get("opacity", 1.0), f"color.roles.{role}.opacity",
                minimum=0, maximum=1)
        _text(token.get("usage"), f"color.roles.{role}.usage")
    for palette in ("categorical", "sequential", "diverging"):
        for index, value in enumerate(color.get(palette, []) or []):
            parse_color(value, f"color.{palette}[{index}]")
    for name, value in _mapping(color.get("opacity", {}), "color.opacity").items():
        _number(value, f"color.opacity.{name}", minimum=0, maximum=1)

    typography = _mapping(document["typography"], "typography")
    for family in ("sans", "serif", "mono"):
        stack = _mapping(typography.get(family), f"typography.{family}")
        _string_list(stack.get("family"), f"typography.{family}.family")
        _integer(stack.get("weight"), f"typography.{family}.weight")
        if not 100 <= int(stack.get("weight") or 0) <= 900:
            errors.append(f"typography.{family}.weight must be between 100 and 900")
        _enum(stack.get("style", "normal"), f"typography.{family}.style",
              {"normal", "italic"})
    _text(typography.get("math"), "typography.math")
    for name, value in _mapping(typography.get("roles_pt"), "typography.roles_pt").items():
        _number(value, f"typography.roles_pt.{name}", minimum=0.1)
    for name, value in _mapping(typography.get("line_height"), "typography.line_height").items():
        _number(value, f"typography.line_height.{name}", minimum=0.1)
    for name, value in _mapping(typography.get("casing"), "typography.casing").items():
        _enum(value, f"typography.casing.{name}", CASING)

    shape = _mapping(document["shape"], "shape")
    strokes = _mapping(shape.get("strokes"), "shape.strokes")
    for name, stroke in strokes.items():
        stroke = _mapping(stroke, f"shape.strokes.{name}")
        stroke_color = _text(stroke.get("color"), f"shape.strokes.{name}.color")
        if stroke_color not in roles and stroke_color != "none":
            errors.append(f"shape.strokes.{name}.color references unknown color role {stroke_color!r}")
        _number(stroke.get("width_pt"), f"shape.strokes.{name}.width_pt", minimum=0)
        _enum(stroke.get("style", "solid"), f"shape.strokes.{name}.style", STROKE_STYLES)
        _enum(stroke.get("cap", "round"), f"shape.strokes.{name}.cap", CAPS)
        _enum(stroke.get("join", "round"), f"shape.strokes.{name}.join", JOINS)
    shadows = _mapping(shape.get("shadows"), "shape.shadows")
    for name, shadow in shadows.items():
        shadow = _mapping(shadow, f"shape.shadows.{name}")
        shadow_color = _text(shadow.get("color"), f"shape.shadows.{name}.color")
        if shadow_color not in roles and shadow_color != "none":
            errors.append(f"shape.shadows.{name}.color references unknown color role {shadow_color!r}")
        _number(shadow.get("opacity"), f"shape.shadows.{name}.opacity", minimum=0, maximum=1)
        _number(shadow.get("blur"), f"shape.shadows.{name}.blur", minimum=0)
    vocabulary = _mapping(shape.get("vocabulary"), "shape.vocabulary")
    for name, item in vocabulary.items():
        item = _mapping(item, f"shape.vocabulary.{name}")
        _enum(item.get("geometry"), f"shape.vocabulary.{name}.geometry", GEOMETRY)
        fill = _text(item.get("fill"), f"shape.vocabulary.{name}.fill")
        stroke = _mapping(item.get("stroke"), f"shape.vocabulary.{name}.stroke")
        stroke_color = _text(stroke.get("color"), f"shape.vocabulary.{name}.stroke.color")
        if fill not in roles and fill != "none":
            errors.append(f"shape.vocabulary.{name}.fill references unknown color role {fill!r}")
        if stroke_color not in roles and stroke_color != "none":
            errors.append(f"shape.vocabulary.{name}.stroke.color references unknown role {stroke_color!r}")
        _number(item.get("radius", 0), f"shape.vocabulary.{name}.radius", minimum=0)
        padding = item.get("padding", [0, 0])
        if not isinstance(padding, (list, tuple)) or not 2 <= len(padding) <= 4:
            errors.append(f"shape.vocabulary.{name}.padding must have 2-4 values")
        _enum(item.get("emphasis", "normal"), f"shape.vocabulary.{name}.emphasis", EMPHASIS)
    spacing = _mapping(document["spacing"], "spacing")
    _number(spacing.get("base"), "spacing.base", minimum=0.01)
    for index, value in enumerate(spacing.get("scale") or []):
        _number(value, f"spacing.scale[{index}]", minimum=0)
    _enum(spacing.get("density"), "spacing.density", DENSITY)

    layout = _mapping(document["layout"], "layout")
    for name, value in _mapping(layout.get("figure_width_in"), "layout.figure_width_in").items():
        _number(value, f"layout.figure_width_in.{name}", minimum=0.01)
    for name, value in _mapping(layout.get("aspect_ratios"), "layout.aspect_ratios").items():
        _number(value, f"layout.aspect_ratios.{name}", minimum=0.01)
    _integer(layout.get("grid_columns"), "layout.grid_columns", minimum=1)
    _number(layout.get("gutter"), "layout.gutter", minimum=0)
    _number(layout.get("outer_margin"), "layout.outer_margin", minimum=0)
    _enum(layout.get("alignment"), "layout.alignment", ALIGNMENT)
    _enum(layout.get("reading_order"), "layout.reading_order", READING_ORDER)

    chart = _mapping(document["chart"], "chart")
    for key in ("axes", "lines", "markers", "grid", "legend", "uncertainty"):
        _mapping(chart.get(key), f"chart.{key}")
    _string_list(chart.get("rules"), "chart.rules", required=False)

    web = _mapping(document["web"], "web")
    _integer(web.get("content_width_px"), "web.content_width_px", minimum=1)
    for name, value in _mapping(web.get("breakpoints_px"), "web.breakpoints_px").items():
        _integer(value, f"web.breakpoints_px.{name}", minimum=1)
    for name, value in _mapping(web.get("radius_scale_px"), "web.radius_scale_px").items():
        _number(value, f"web.radius_scale_px.{name}", minimum=0)
    _mapping(web.get("focus_ring"), "web.focus_ring")
    for name, value in _mapping(web.get("transitions_ms"), "web.transitions_ms").items():
        _integer(value, f"web.transitions_ms.{name}", minimum=0)

    motion = _mapping(document["motion"], "motion")
    _string_list(motion.get("principles"), "motion.principles", required=False)
    for name, curve in _mapping(motion.get("curves"), "motion.curves").items():
        curve = _mapping(curve, f"motion.curves.{name}")
        _integer(curve.get("duration_ms"), f"motion.curves.{name}.duration_ms", minimum=0)
        _text(curve.get("easing"), f"motion.curves.{name}.easing")
        _number(curve.get("distance_px", 0), f"motion.curves.{name}.distance_px", minimum=0)
        _number(curve.get("opacity_from", 1), f"motion.curves.{name}.opacity_from",
                minimum=0, maximum=1)
    _enum(motion.get("reduced_motion"), "motion.reduced_motion", REDUCED_MOTION)

    video = _mapping(document["video"], "video")
    _integer(video.get("width"), "video.width", minimum=1)
    _integer(video.get("height"), "video.height", minimum=1)
    _integer(video.get("fps"), "video.fps", minimum=1)
    _number(video.get("safe_area_percent"), "video.safe_area_percent", minimum=0, maximum=25)
    _number(video.get("title_duration_s"), "video.title_duration_s", minimum=0.01)
    _text(video.get("transition"), "video.transition")
    _mapping(video.get("caption"), "video.caption")

    # Cross-system invariant: text remains readable and does not rely on opacity.
    surface = str(roles.get("surface", {}).get("value", "")) if isinstance(
        roles.get("surface"), Mapping) else ""
    text_primary = str(roles.get("text_primary", {}).get("value", "")) if isinstance(
        roles.get("text_primary"), Mapping) else ""
    text_secondary = str(roles.get("text_secondary", {}).get("value", "")) if isinstance(
        roles.get("text_secondary"), Mapping) else ""
    try:
        if surface and text_primary and contrast_ratio(text_primary, surface) < 4.5:
            errors.append("text_primary/surface contrast is below WCAG AA 4.5:1")
        if surface and text_secondary and contrast_ratio(text_secondary, surface) < 3.0:
            warnings.append("text_secondary/surface contrast is below 3.0:1")
        for name in ("text_primary", "text_secondary"):
            token = roles.get(name)
            if isinstance(token, Mapping) and float(token.get("opacity", 1)) < 0.9:
                warnings.append(f"{name} uses opacity below 0.9; use a readable text color instead")
        categorical = [str(parse_color(value, "categorical")) for value in
                       (color.get("categorical") or [])]
        if len(categorical) >= 2:
            luminances = sorted({round(_luminance(value), 3) for value in categorical})
            if len(luminances) == 1:
                warnings.append("categorical palette collapses to one luminance in grayscale")
    except DesignError:
        pass

    variants = document.get("variants")
    if variants is not None:
        if not isinstance(variants, Mapping):
            errors.append("variants must be an object")
        for name, override in variants.items():
            if not re.match(r"^[a-z][a-z0-9_-]*$", str(name)):
                errors.append(f"variant name {name!r} must be lowercase snake_case")
            if name not in VARIANTS:
                warnings.append(f"variant {name!r} is not one of paper/web/video")
            if not isinstance(override, Mapping):
                errors.append(f"variant {name!r} must be an object")


def load_theme(path: str | Path) -> dict:
    """Load a JSON theme; YAML is supported only when PyYAML is installed."""
    target = Path(path)
    try:
        raw = target.read_text(encoding="utf-8")
    except OSError as exc:
        raise DesignError(f"cannot read theme: {target}") from exc
    suffix = target.suffix.lower()
    if suffix == ".json":
        document = json.loads(raw)
    elif suffix in {".yaml", ".yml"}:
        try:
            import yaml  # type: ignore
        except ImportError as exc:  # pragma: no cover - optional dependency
            raise DesignError("YAML themes require PyYAML; use JSON or install PyYAML") from exc
        document = yaml.safe_load(raw)
    else:
        raise DesignError("theme must be .json, .yaml or .yml")
    if not isinstance(document, Mapping):
        raise DesignError("theme document must contain a mapping")
    return dict(document)


def validate_asset_path(path: str, *, project_root: str | Path | None = None) -> dict:
    """Enforce ARK's project-relative asset rules for a design artifact."""
    raw = str(path or "").strip().replace("\\", "/")
    errors: list[str] = []
    if not raw:
        return {"ok": False, "errors": ["asset path is required"], "path": ""}
    if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*://", raw) or raw.startswith("data:"):
        errors.append("asset must be a local project file, not a URL or data URI")
    if raw.startswith("/") or re.match(r"^[A-Za-z]:", raw):
        errors.append("asset path must be project-relative")
    parts = PurePosixPath(raw).parts
    if ".." in parts:
        errors.append("asset path must not traverse outside the project")
    if not parts or parts[0] != "assets":
        errors.append("design artifacts must live under the project's assets/ folder")
    if Path(raw).suffix.lower() not in ASSET_SUFFIXES:
        errors.append(f"unsupported asset type: {Path(raw).suffix or '(none)'}")
    if project_root is not None:
        root = Path(project_root).resolve()
        try:
            (root / raw).resolve().relative_to(root)
        except ValueError:
            errors.append("asset path resolves outside the project folder")
    return {
        "ok": not errors,
        "errors": errors,
        "path": str(PurePosixPath(raw)),
        "rules": {
            "project_relative": True,
            "assets_folder": True,
            "allowed_suffixes": sorted(ASSET_SUFFIXES),
        },
    }


def verify_artifact(path: str | Path, *, project_root: str | Path | None = None,
                    kind: str = "auto") -> dict:
    """Verify a generated figure/site/video artifact before delivery."""
    checks: list[dict] = []
    errors: list[str] = []
    warnings: list[str] = []
    target = Path(path)
    raw = str(path)

    if project_root is not None:
        root = Path(project_root).resolve()
        candidate = Path(raw)
        if not candidate.is_absolute():
            candidate = root / candidate
        try:
            candidate.resolve().relative_to(root)
            checks.append({"id": "in_project", "ok": True, "detail": str(candidate)})
        except ValueError:
            errors.append("artifact resolves outside the ARK project folder")
            checks.append({"id": "in_project", "ok": False, "detail": str(candidate)})
        target = candidate

    if not target.is_file():
        errors.append(f"artifact not found: {target}")
        return {"ok": False, "path": str(target), "checks": checks,
                "errors": errors, "warnings": warnings, "kind": kind}
    checks.append({"id": "exists", "ok": True, "detail": str(target)})

    size = target.stat().st_size
    if size <= 0:
        errors.append("artifact is empty")
    checks.append({"id": "non_empty", "ok": size > 0, "detail": f"{size} bytes"})

    suffix = target.suffix.lower()
    if kind == "auto":
        kind = {".svg": "svg", ".pdf": "pdf", ".png": "png", ".jpg": "jpeg",
                ".jpeg": "jpeg", ".webp": "webp", ".html": "html",
                ".css": "css"}.get(suffix, "unknown")
    checks.append({"id": "kind", "ok": kind != "unknown", "detail": kind})

    if kind == "svg":
        try:
            tree = ElementTree.parse(target)
            root = tree.getroot()
            valid = root.tag.split("}")[-1] == "svg"
            checks.append({"id": "svg_xml", "ok": valid,
                           "detail": root.tag})
            if not valid:
                errors.append("SVG root element is not <svg>")
            external = sorted({match.group(1) for match in
                               EXTERNAL_REF_RE.finditer(target.read_text(encoding="utf-8", errors="replace"))})
            checks.append({"id": "self_contained", "ok": not external,
                           "detail": ", ".join(external) or "no external references"})
            if external:
                errors.append("SVG references external assets: " + ", ".join(external))
        except ElementTree.ParseError as exc:
            errors.append(f"SVG is not well-formed XML: {exc}")
            checks.append({"id": "svg_xml", "ok": False, "detail": str(exc)})
    elif kind in {"png", "jpeg", "pdf"}:
        magic = target.read_bytes()[:8]
        expected = {
            "png": magic.startswith(b"\x89PNG\r\n\x1a\n"),
            "jpeg": magic.startswith(b"\xff\xd8\xff"),
            "pdf": magic.startswith(b"%PDF-"),
        }
        ok = expected.get(kind, False)
        checks.append({"id": "magic", "ok": ok, "detail": magic.hex()})
        if not ok:
            errors.append(f"file does not look like a valid {kind}")
    elif kind == "html":
        text = target.read_text(encoding="utf-8", errors="replace")
        has_viewport = bool(re.search(r'<meta[^>]+name=["\']viewport["\']', text, re.IGNORECASE))
        external = sorted(set(EXTERNAL_REF_RE.findall(text)))
        checks.append({"id": "viewport", "ok": has_viewport,
                       "detail": "meta viewport present" if has_viewport else "missing"})
        checks.append({"id": "self_contained", "ok": not external,
                       "detail": ", ".join(external) or "no external references"})
        if not has_viewport:
            warnings.append("HTML artifact has no viewport meta tag")
        if external:
            warnings.append("HTML references external assets; disclose and pin them")
    elif kind == "css":
        text = target.read_text(encoding="utf-8", errors="replace")
        has_vars = "--" in text
        checks.append({"id": "tokens", "ok": has_vars, "detail": "CSS custom properties"})
        if not has_vars:
            warnings.append("CSS artifact declares no custom-property tokens")

    return {
        "ok": not errors,
        "path": str(target),
        "kind": kind,
        "size": size,
        "checks": checks,
        "errors": errors,
        "warnings": warnings,
        "quality_gates": QUALITY_GATES,
    }


def example_theme() -> dict:
    return _read_json_asset("design", "theme.json")


def render_design_brief(context: Mapping[str, Any] | None = None) -> str:
    merged = {
        "project_name": "ARK project",
        "audience": "researchers and maintainers",
        "venue": "internal report",
        "mediums": ["paper figure", "project website"],
        "qualities": ["precise", "calm", "technical"],
        "anti_goals": ["decorative gradients", "low-contrast gray text"],
        "theme_name": "academic-clean",
        "theme_version": "1",
        "artifact": "assets/figure.svg",
        **dict(context or {}),
    }
    return _render("design", "design_brief.md", context=merged)


def render_style_review(context: Mapping[str, Any] | None = None) -> str:
    merged = {
        "artifact": "assets/figure.svg",
        "theme_name": "academic-clean",
        "theme_version": "1",
        "mode": "paper",
        "shared_checks": QUALITY_GATES["shared"],
        "mode_checks": QUALITY_GATES["paper"],
        "verification": "_Run verify_artifact() before ticking these gates._",
        **dict(context or {}),
    }
    return _render("design", "style_review.md", context=merged)


def _render(*parts: str, context: Mapping[str, Any]) -> str:
    from . import protocol
    template = (Path(__file__).resolve().parent / "assets" / Path(*parts)).read_text(encoding="utf-8")
    return protocol.substitute(template, context)


def _read_json_asset(*parts: str) -> dict:
    path = Path(__file__).resolve().parent / "assets" / Path(*parts)
    return json.loads(path.read_text(encoding="utf-8"))
