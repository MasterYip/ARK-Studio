"""Build the figures and sample files of the built-in ARK Studio tutorial.

    python scripts/dev/build_demo_assets.py            # write demo-assets/tutorial/
    python scripts/dev/build_demo_assets.py --check    # fail if a committed file is stale

Standard library only (plus ARK's own skills package for the goal manifest,
the inspection report and the design theme).  Every output is deterministic, so
re-running the script on an unchanged checkout produces byte-identical files.
The one exception is the short MP4 clip: it needs ``ffmpeg`` on PATH and is
skipped (with a message) when ffmpeg is missing; ``--check`` ignores it.

The SVGs are written by hand on purpose: they are small, diffable and easy to
restyle, and they pass ``arkstudio.skills.design.verify_artifact``.
"""
from __future__ import annotations

import argparse
import csv
import io
import json
import math
import shutil
import struct
import subprocess
import sys
import tempfile
import zlib
from pathlib import Path
from xml.sax.saxutils import escape

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from arkstudio import tags as tag_vocab  # noqa: E402
from arkstudio.skills import agent_team, design  # noqa: E402

OUT = ROOT / "demo-assets" / "tutorial"

# --------------------------------------------------------------- the theme
THEME = design.example_theme()
ROLE = {key: value["value"] for key, value in THEME["color"]["roles"].items()}
SURFACE = ROLE["surface"]
PANEL = ROLE["surface_alt"]
INK = ROLE["text_primary"]
INK2 = ROLE["text_secondary"]
PRIMARY = ROLE["data_primary"]
SECONDARY = ROLE["data_secondary"]
BASELINE = ROLE["baseline"]
SUCCESS = ROLE["success"]
WARNING = ROLE["warning"]
FAILURE = ROLE["failure"]
FOCUS = ROLE["focus"]
SANS = "Inter, 'Segoe UI', 'Helvetica Neue', Arial, sans-serif"
MONO = "'JetBrains Mono', Consolas, monospace"

# --------------------------------------------------------- the example data
# One small, fictional study runs through the whole tutorial: does a 500-step
# learning-rate warm-up remove early loss spikes in a 60M-parameter model?
STEPS = list(range(0, 5001, 250))
SPIKES = {750: 1.35, 1500: 0.9, 2250: 0.6}


def _loss(step: int, warmup: bool) -> float:
    if warmup:
        value = 2.075 + 6.2 * math.exp(-step / 760.0)
    else:
        value = 2.136 + 6.2 * math.exp(-step / 700.0) + SPIKES.get(step, 0.0)
    return round(value, 3)


RESULTS = [(s, _loss(s, True), _loss(s, False)) for s in STEPS]
SWEEP = [  # learning rate, final loss (mean of 3 seeds), spikes per run
    ("1e-4", 2.31, 0.0), ("3e-4", 2.11, 0.3), ("6e-4", 2.05, 0.7),
    ("1e-3", 2.09, 1.7), ("2e-3", 2.38, 4.0), ("4e-3", 3.02, 7.3),
]


# ------------------------------------------------------------- SVG helpers
class Svg:
    def __init__(self, width: int, height: int, title: str, desc: str = "",
                 background: str = SURFACE):
        self.w, self.h = width, height
        self.parts = [
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
            f'viewBox="0 0 {width} {height}" role="img" font-family="{escape(SANS)}">',
            f"<title>{escape(title)}</title>",
        ]
        if desc:
            self.parts.append(f"<desc>{escape(desc)}</desc>")
        self.parts.append(
            "<defs>"
            f'<marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" '
            f'markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="{BASELINE}"/></marker>'
            f'<marker id="arrow-focus" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" '
            f'markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="{PRIMARY}"/></marker>'
            "</defs>")
        self.parts.append(f'<rect width="{width}" height="{height}" fill="{background}"/>')

    def add(self, raw: str) -> None:
        self.parts.append(raw)

    def rect(self, x, y, w, h, *, fill=PANEL, stroke=BASELINE, sw=1, r=8, opacity=1.0,
             dash: str = "") -> None:
        extra = f' stroke-dasharray="{dash}"' if dash else ""
        self.parts.append(
            f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{fill}" '
            f'fill-opacity="{opacity}" stroke="{stroke}" stroke-width="{sw}"{extra}/>')

    def text(self, x, y, value, *, size=14, color=INK, weight=400, anchor="start",
             family: str = "", italic=False) -> None:
        fam = f' font-family="{escape(family)}"' if family else ""
        style = ' font-style="italic"' if italic else ""
        self.parts.append(
            f'<text x="{x}" y="{y}" font-size="{size}" fill="{color}" font-weight="{weight}" '
            f'text-anchor="{anchor}"{fam}{style}>{escape(str(value))}</text>')

    def lines(self, x, y, values, *, size=13, color=INK2, gap=None, **kw) -> None:
        gap = gap or round(size * 1.4)
        for i, value in enumerate(values):
            self.text(x, y + i * gap, value, size=size, color=color, **kw)

    def line(self, x1, y1, x2, y2, *, color=BASELINE, sw=1.5, arrow=False, dash="",
             focus=False) -> None:
        extra = ""
        if arrow:
            extra += f' marker-end="url(#{"arrow-focus" if focus else "arrow"})"'
        if dash:
            extra += f' stroke-dasharray="{dash}"'
        self.parts.append(
            f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{color}" '
            f'stroke-width="{sw}" stroke-linecap="round"{extra}/>')

    def path(self, d, *, color=BASELINE, sw=1.5, fill="none", arrow=False, dash="") -> None:
        extra = ' marker-end="url(#arrow)"' if arrow else ""
        if dash:
            extra += f' stroke-dasharray="{dash}"'
        self.parts.append(f'<path d="{d}" fill="{fill}" stroke="{color}" stroke-width="{sw}" '
                          f'stroke-linecap="round" stroke-linejoin="round"{extra}/>')

    def circle(self, cx, cy, r, *, fill=PRIMARY, stroke="none", sw=0) -> None:
        self.parts.append(f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="{fill}" stroke="{stroke}" '
                          f'stroke-width="{sw}"/>')

    def badge(self, cx, cy, number, *, fill=FAILURE) -> None:
        self.circle(cx, cy, 12, fill=fill)
        self.text(cx, cy + 5, number, size=13, color="#FFFFFF", weight=700, anchor="middle")

    def box(self, x, y, w, h, title, lines=(), *, accent=PRIMARY, fill=PANEL, title_size=15,
            line_size=12) -> None:
        self.rect(x, y, w, h, fill=fill)
        self.rect(x, y, 6, h, fill=accent, stroke="none", r=3)
        self.text(x + 18, y + 26, title, size=title_size, weight=700)
        self.lines(x + 18, y + 26 + round(line_size * 1.7), list(lines), size=line_size)

    def heading(self, title, subtitle="") -> None:
        self.text(32, 44, title, size=24, weight=700)
        if subtitle:
            self.text(32, 70, subtitle, size=14, color=INK2)

    def render(self) -> str:
        return "\n".join(self.parts + ["</svg>"]) + "\n"


def chart_svg(width: int, height: int, *, title: str, subtitle: str, note: str = "") -> str:
    """The main result: mean training loss, warm-up vs no warm-up (3 seeds)."""
    svg = Svg(width, height, title,
              "Line chart of mean training loss over 5,000 steps. The run without warm-up "
              "shows three loss spikes at steps 750, 1,500 and 2,250; the warm-up run has none.")
    svg.heading(title, subtitle)
    left, top, right, bottom = 78, 104, width - 150, height - 70
    ymin, ymax = 2.0, 9.0

    def px(step):
        return left + (right - left) * step / 5000.0

    def py(value):
        return bottom - (bottom - top) * (value - ymin) / (ymax - ymin)

    for value in range(2, 10):  # recessive grid
        y = round(py(value), 1)
        svg.line(left, y, right, y, color=PANEL, sw=1)
        svg.text(left - 10, y + 4, f"{value:.0f}", size=12, color=INK2, anchor="end")
    for step in range(0, 5001, 1000):
        x = round(px(step), 1)
        svg.line(x, bottom, x, bottom + 5, color=BASELINE, sw=1)
        svg.text(x, bottom + 22, f"{step:,}", size=12, color=INK2, anchor="middle")
    svg.line(left, bottom, right, bottom, color=BASELINE, sw=1)
    svg.text(left, top - 14, "training loss (mean of 3 seeds)", size=12, color=INK2)
    svg.text((left + right) / 2, bottom + 46, "optimizer step", size=12, color=INK2,
             anchor="middle")
    # warm-up window
    svg.rect(round(px(0), 1), top, round(px(500) - px(0), 1), bottom - top, fill=PRIMARY,
             stroke="none", r=0, opacity=0.08)
    svg.text(round(px(250), 1), top + 16, "warm-up", size=11, color=INK2, anchor="middle")

    for index, color, label in ((2, SECONDARY, "no warm-up"), (1, PRIMARY, "500-step warm-up")):
        points = " ".join(f"{px(row[0]):.1f},{py(row[index]):.1f}" for row in RESULTS)
        svg.add(f'<polyline points="{points}" fill="none" stroke="{color}" stroke-width="2" '
                f'stroke-linejoin="round" stroke-linecap="round"/>')
        last = RESULTS[-1]
        svg.circle(round(px(last[0]), 1), round(py(last[index]), 1), 4, fill=color,
                   stroke=SURFACE, sw=2)
        offset = -8 if index == 2 else 14
        svg.text(round(px(last[0]) + 10, 1), round(py(last[index]) + offset, 1), label, size=12,
                 color=INK, weight=600)
    for step in SPIKES:  # selective annotation
        row = next(r for r in RESULTS if r[0] == step)
        svg.circle(round(px(step), 1), round(py(row[2]), 1), 5, fill="none", stroke=FAILURE, sw=2)
    first = min(SPIKES)
    row = next(r for r in RESULTS if r[0] == first)
    svg.text(round(px(first) + 12, 1), round(py(row[2]) - 6, 1), "loss spike", size=12,
             color=FAILURE, weight=600)
    # legend (always present for two series)
    lx, ly = right - 290, top - 10
    for color, label in ((PRIMARY, "500-step warm-up"), (SECONDARY, "no warm-up")):
        svg.rect(lx, ly - 6, 14, 4, fill=color, stroke="none", r=2)
        svg.text(lx + 20, ly, label, size=12, color=INK2)
        lx += 150
    if note:
        svg.text(width - 24, height - 14, note, size=11, color=INK2, anchor="end")
    return svg.render()


# ----------------------------------------------------------------- chapters
def build_start(out: Path) -> None:
    svg = Svg(1200, 560, "The ARK loop",
              "Six workspaces in a loop: Home, Research, Map, Execute, Output and Editor, "
              "with three human checkpoints.")
    svg.heading("The ARK loop", "One project, six workspaces. People decide; agents do the work; "
                "ARK keeps the record.")
    steps = [
        ("Home", ["Set up the environment", "Copy an agent prompt"], "#B7791F"),
        ("Research", ["Idea, then Plan:", "Story · Related · Method", "· Experiment"], "#C2683F"),
        ("Map", ["See the argument", "Find gaps and weak claims"], "#7C5C9E"),
        ("Execute", ["Board · Runs · GPU · Files", "Agents run tasks"], "#1F5A7A"),
        ("Output", ["Assets · Distribution", "Figures, posters, posts"], "#2F7D5B"),
    ]
    x, y, w, h, gap = 32, 120, 208, 128, 24
    for i, (name, lines, color) in enumerate(steps):
        bx = x + i * (w + gap)
        svg.box(bx, y, w, h, f"{i + 1}. {name}", lines, accent=color, title_size=17, line_size=13)
        if i < len(steps) - 1:
            svg.line(bx + w + 2, y + h / 2, bx + w + gap - 2, y + h / 2, arrow=True)
    # loop back
    svg.path(f"M {x + 4 * (w + gap) + w / 2} {y + h + 4} V {y + h + 60} H {x + w / 2} V {y + h + 6}",
             arrow=True, dash="6 5")
    svg.text(600, y + h + 52, "Results feed the next idea", size=13, color=INK2, anchor="middle",
             italic=True)
    # editor + assistant band
    svg.rect(32, 340, 1136, 64, fill=SURFACE, dash="5 4")
    svg.text(52, 368, "Editor", size=15, weight=700)
    svg.text(52, 390, "Open any file or node: images, video, PDF, Markdown, CSV, JSON, code.",
             size=13, color=INK2)
    svg.text(640, 368, "Assistant  (Ctrl+J)", size=15, weight=700)
    svg.text(640, 390, "Ask about the project. It writes through the same store as the UI.",
             size=13, color=INK2)
    # human checkpoints
    svg.text(32, 446, "Human checkpoints", size=16, weight=700)
    checks = [
        ("1", "Confirm the setup", "Home → Review & confirm. Agents cannot confirm it."),
        ("2", "Accept the work", "The reviewer reads the inspection report and accepts."),
        ("3", "Approve risky actions", "Push, GPU jobs, deletion, external posts: ask first."),
    ]
    for i, (num, title, detail) in enumerate(checks):
        cx = 32 + i * 384
        svg.badge(cx + 12, 482, num, fill=FAILURE)
        svg.text(cx + 32, 487, title, size=14, weight=700)
        svg.text(cx + 32, 509, detail, size=12, color=INK2)
    (out / "ark-loop.svg").write_text(svg.render(), encoding="utf-8")

    svg = Svg(1200, 600, "ARK architecture at a glance",
              "Browsers and agents talk HTTP+JSON to one Python server, which owns one folder "
              "per project.")
    svg.heading("Architecture at a glance", "One stdlib Python server. One folder per project. "
                "SQLite is the source of truth.")
    svg.box(32, 110, 300, 150, "People", ["Desktop browser / installed app", "Phone (PWA, QR pairing)",
                                          "Android shell"], accent="#B7791F")
    svg.box(32, 290, 300, 150, "Agents", ["DSH · Codex · Claude Code", "Agent API: /api/agent/v1/…",
                                          "Bearer token, scoped skills"], accent="#7C5C9E")
    svg.box(420, 110, 380, 330, "ARK server  (python -m arkstudio serve)", [
        "REST API + auth + project permissions",
        "Run manager: agent sessions, worktrees",
        "Assistant: local harness or your model",
        "Skills: setup · agent-team · rrsi · design",
        "GPU adapter: probe, queue, dry-run",
        "Workspace API: files, raw previews",
        "",
        "Standard library only. Loopback by default.",
    ], accent=PRIMARY)
    svg.box(888, 110, 280, 330, "my-project.ark/", [
        "project.json · the manifest",
        "ark.sqlite3 · units, links, activity",
        "assets/ · figures, video, PDFs",
        "setup/ · environment profile",
        "exports/ · logs/",
        "",
        "Copy the folder = copy",
        "the whole project.",
    ], accent=SUCCESS)
    for y0 in (185, 365):
        svg.line(336, y0, 414, y0 if y0 == 185 else y0, arrow=True)
    svg.text(374, 176, "HTTP", size=11, color=INK2, anchor="middle")
    svg.text(374, 356, "HTTP", size=11, color=INK2, anchor="middle")
    svg.line(804, 275, 882, 275, arrow=True)
    svg.text(843, 266, "read/write", size=11, color=INK2, anchor="middle")
    svg.badge(420, 110, "A")
    svg.badge(888, 110, "B")
    svg.text(32, 500, "A  Every project-scoped call is checked again on the server; the UI is not the gate.",
             size=13, color=INK2)
    svg.text(32, 524, "B  The graph you see in Map is a view of this database, not a separate file.",
             size=13, color=INK2)
    svg.text(32, 548, "Secrets are stored as references (env:NAME), never as values.", size=13,
             color=INK2)
    (out / "architecture.svg").write_text(svg.render(), encoding="utf-8")


def build_home(out: Path) -> None:
    svg = Svg(1200, 620, "The Home workspace, annotated",
              "Wireframe of Home: the setup status badge, two starter prompt cards with Copy "
              "prompt and Preview, the standing-rules note and the Review & confirm button.")
    svg.heading("Home, annotated", "Where every session starts: setup status and copy-ready agent prompts.")
    svg.rect(32, 96, 1136, 488, fill="#FFFFFF")
    svg.text(56, 132, "Start with an agent", size=20, weight=700)
    svg.text(56, 156, "Copy a prompt into your coding agent (DSH, Codex, Claude Code). "
             "ARK keeps the record; the agent does the work.", size=13, color=INK2)
    svg.rect(880, 112, 170, 30, fill="#FDF3E1", stroke=WARNING, r=15)
    svg.text(965, 132, "Setup: waiting for you", size=12, color=INK, weight=600, anchor="middle")
    svg.rect(1060, 112, 92, 30, fill=PRIMARY, stroke="none", r=6)
    svg.text(1106, 132, "Review & confirm", size=10, color="#FFFFFF", weight=700, anchor="middle")
    svg.badge(872, 112, "1")
    svg.badge(1152, 112, "4")
    cards = [
        ("Set up this environment", ["Once per machine or new repo. Setup, then", "you confirm; then what to ask next."]),
        ("Start / continue research team", ["Every new agent session: load context,", "summarise, offer options, coordinate."]),
    ]
    for i, (title, desc) in enumerate(cards):
        cx = 56 + i * 552
        svg.rect(cx, 186, 532, 160, fill=SURFACE)
        svg.text(cx + 16, 218, f"{i + 1}. {title}", size=15, weight=700)
        svg.lines(cx + 16, 244, desc, size=12, color=INK2)
        svg.rect(cx + 16, 296, 104, 32, fill=PRIMARY, stroke="none", r=6)
        svg.text(cx + 68, 317, "Copy prompt", size=12, color="#FFFFFF", weight=700, anchor="middle")
        svg.rect(cx + 130, 296, 80, 32, fill="#FFFFFF", r=6)
        svg.text(cx + 170, 317, "Preview", size=12, color=INK, anchor="middle")
    svg.badge(56, 186, "2")
    svg.badge(72, 296, "3")
    svg.text(56, 372, "Both prompts carry standing rules: read ARK before working; record tasks, progress,",
             size=12, color=INK2, italic=True)
    svg.text(56, 390, "evidence and decisions after every step, without reminders.", size=12, color=INK2,
             italic=True)
    notes = [
        ("1", "Setup status: not started → draft → waiting for you → confirmed → ready."),
        ("2", "Two starter prompts: 1 once per machine or repository, 2 in every new agent session."),
        ("3", "Copy prompt puts the full text on your clipboard. Preview shows it first."),
        ("4", "Review & confirm: one dialog, 1/2 project setup then 2/2 my resources. Only a person confirms."),
    ]
    for i, (num, text) in enumerate(notes):
        svg.badge(68, 452 + i * 30, num)
        svg.text(90, 457 + i * 30, text, size=13, color=INK)
    (out / "home-annotated.svg").write_text(svg.render(), encoding="utf-8")


def build_research(out: Path) -> None:
    # data-model diagram
    svg = Svg(1200, 640, "The ARK data model",
              "Units with one type, one status and one importance, joined by typed links. The "
              "example chain: idea, question, claims, evidence, experiment, task.")
    svg.heading("The data model: units and typed links",
                "Every node is a unit with a stable ID. Every edge is a typed, directed link.")
    nodes = {
        "idea": (60, 140, "Idea", "I-0001", "Warm-up stabilises small models"),
        "question": (60, 300, "Question", "Q-0001", "Does warm-up remove loss spikes?"),
        "claim1": (440, 220, "Claim", "C-0001", "Warm-up removes early spikes"),
        "claim2": (440, 380, "Claim", "C-0002", "Warm-up keeps final loss"),
        "evidence": (820, 140, "Evidence", "E-0001", "0 vs 3 spikes per run"),
        "experiment": (820, 300, "Experiment", "X-0001", "Warm-up vs none, 5k steps"),
        "task": (820, 460, "Task", "T-0001", "Run the 3-seed comparison"),
    }
    colors = {key: tag_vocab.type_color(key) for key in tag_vocab.KNOWN_TYPES}
    type_of = {"idea": "idea", "question": "question", "claim1": "claim", "claim2": "claim",
               "evidence": "evidence", "experiment": "experiment", "task": "task"}
    w, h = 300, 92
    for key, (x, y, label, uid, title) in nodes.items():
        svg.rect(x, y, w, h, fill="#FFFFFF")
        svg.rect(x, y, 6, h, fill=colors[type_of[key]], stroke="none", r=3)
        svg.text(x + 18, y + 26, f"{label} · {uid}", size=12, color=INK2, weight=600,
                 family=MONO)
        svg.text(x + 18, y + 54, title, size=15, weight=700)
        status = {"idea": "active", "question": "active", "claim1": "review", "claim2": "open",
                  "evidence": "review", "experiment": "review", "task": "review"}[key]
        svg.text(x + 18, y + 76, f"status:{status}", size=11, color=INK2, family=MONO)

    def anchor(key, side):
        x, y = nodes[key][0], nodes[key][1]
        return {"top": (x + w / 2, y), "bottom": (x + w / 2, y + h), "left": (x, y + h / 2),
                "right": (x + w, y + h / 2)}[side]

    def edge(a, side_a, b, side_b, label, dx=0, dy=-6):
        x1, y1 = anchor(a, side_a)
        x2, y2 = anchor(b, side_b)
        svg.line(x1, y1, x2, y2, arrow=True)
        svg.text((x1 + x2) / 2 + dx, (y1 + y2) / 2 + dy, label, size=11, color=PRIMARY,
                 weight=600, anchor="middle" if not dx else "start", family=MONO)

    edge("question", "top", "idea", "bottom", "derived_from", dx=8, dy=4)
    edge("claim1", "left", "question", "right", "targets", dy=-8)
    edge("claim2", "left", "question", "right", "targets", dy=16)
    edge("evidence", "left", "claim1", "right", "supports", dy=-8)
    edge("experiment", "top", "evidence", "bottom", "produces", dx=8, dy=4)
    edge("experiment", "left", "claim2", "right", "validates", dy=16)
    edge("task", "top", "experiment", "bottom", "part_of", dx=8, dy=4)
    svg.text(32, 600, "Reserved tags: exactly one type:, one status:, one importance: per unit. "
             "Everything else (topic:, stage:, channel:, color:…) is a free tag.", size=13, color=INK2)
    (out / "data-model.svg").write_text(svg.render(), encoding="utf-8")

    with (out / "results.csv").open("w", encoding="utf-8", newline="") as handle:
        writer = csv.writer(handle, lineterminator="\n")
        writer.writerow(["step", "loss_warmup", "loss_no_warmup"])
        writer.writerows(RESULTS)
    (out / "results-chart.svg").write_text(chart_svg(
        960, 520, title="Warm-up removes early loss spikes",
        subtitle="Mean training loss of a 60M model, 3 seeds per arm. Source: results.csv",
        note="Fictional tutorial data"), encoding="utf-8")
    (out / "warmup-note.md").write_text(NOTE_MD, encoding="utf-8")
    (out / "references.bib").write_text(BIBTEX, encoding="utf-8")
    (out / "warmup-note.pdf").write_bytes(build_pdf())


NOTE_MD = """\
# Does learning-rate warm-up remove early loss spikes?

*A two-page tutorial note. The numbers are fictional and exist to show ARK's
workflow, not to make a scientific claim.*

## Question

Small transformer models (about 60M parameters) sometimes show sharp loss
spikes in the first few thousand steps. Does a 500-step linear warm-up of the
learning rate remove them without hurting the final loss?

## Setup

- Model: 60M-parameter decoder, AdamW, peak learning rate 6e-4.
- Arms: **no warm-up** vs **500-step linear warm-up**, cosine decay after.
- 3 seeds per arm, 5,000 steps each, one GPU with a 20 GB memory claim.
- Spike: a step whose loss is more than 1.5 times its 50-step running mean.

## Result

| Arm | Final loss (mean) | Spikes per run |
|---|---|---|
| no warm-up | 2.14 | 3.0 |
| 500-step warm-up | 2.08 | 0.0 |

Warm-up removed every early spike in all three seeds and did not slow
convergence. See `results.csv` and `results-chart.svg`.

## Limits

Three seeds and one model size. The claim "warm-up keeps the final loss" stays
**open** until the learning-rate sweep (X-0002) finishes.

## References

Goyal et al. 2017 (warm-up for large-batch SGD); Liu et al. 2020 (RAdam and
the variance of adaptive learning rates). BibTeX in `references.bib`.
"""

BIBTEX = """\
@article{goyal2017accurate,
  title   = {Accurate, Large Minibatch SGD: Training ImageNet in 1 Hour},
  author  = {Goyal, Priya and Doll{\\'a}r, Piotr and Girshick, Ross and Noordhuis, Pieter and
             Wesolowski, Lukasz and Kyrola, Aapo and Tulloch, Andrew and Jia, Yangqing and He, Kaiming},
  journal = {arXiv preprint arXiv:1706.02677},
  year    = {2017}
}

@inproceedings{liu2020variance,
  title     = {On the Variance of the Adaptive Learning Rate and Beyond},
  author    = {Liu, Liyuan and Jiang, Haoming and He, Pengcheng and Chen, Weizhu and
               Liu, Xiaodong and Gao, Jianfeng and Han, Jiawei},
  booktitle = {International Conference on Learning Representations},
  year      = {2020}
}
"""


def build_pdf() -> bytes:
    """A one-page, text-and-vector PDF version of the tutorial note."""
    def esc(text: str) -> str:
        return text.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)")

    ops: list[str] = []

    def text(x, y, value, size=10, font="F1"):
        ops.append(f"BT /{font} {size} Tf {x} {y} Td ({esc(value)}) Tj ET")

    def rgb(hex_color: str) -> str:
        r, g, b = (int(hex_color[i:i + 2], 16) / 255 for i in (1, 3, 5))
        return f"{r:.3f} {g:.3f} {b:.3f}"

    text(56, 790, "Does learning-rate warm-up remove early loss spikes?", 16, "F2")
    text(56, 772, "ARK Studio tutorial note  -  fictional data, for demonstration only", 9)
    y = 744
    blocks = [
        ("Question", ["Small transformers (about 60M parameters) sometimes show sharp loss spikes",
                      "early in training. Does a 500-step linear warm-up remove them without",
                      "hurting the final loss?"]),
        ("Setup", ["60M decoder, AdamW, peak learning rate 6e-4, cosine decay. Two arms:",
                   "no warm-up vs 500-step warm-up. 3 seeds per arm, 5,000 steps each.",
                   "Spike: loss above 1.5x its 50-step running mean."]),
        ("Result", ["Warm-up: final loss 2.08, 0.0 spikes per run.",
                    "No warm-up: final loss 2.14, 3.0 spikes per run (steps 750, 1500, 2250).",
                    "Warm-up removed every early spike in all seeds (Figure 1)."]),
    ]
    for heading, body in blocks:
        text(56, y, heading, 12, "F2")
        y -= 16
        for line in body:
            text(56, y, line, 10)
            y -= 14
        y -= 10
    # Figure 1: a small vector line chart
    left, bottom, width, height = 80, y - 210, 440, 190
    ops.append(f"{rgb(BASELINE)} RG 0.6 w {left} {bottom} m {left + width} {bottom} l S")
    ops.append(f"{left} {bottom} m {left} {bottom + height} l S")

    def pt(step, value):
        return (left + width * step / 5000.0, bottom + height * (value - 2.0) / 7.0)

    for index, color in ((2, SECONDARY), (1, PRIMARY)):
        coords = [pt(row[0], row[index]) for row in RESULTS]
        path = f"{coords[0][0]:.1f} {coords[0][1]:.1f} m " + " ".join(
            f"{x:.1f} {yv:.1f} l" for x, yv in coords[1:])
        ops.append(f"{rgb(color)} RG 1.6 w {path} S")
    ops.append("0 0 0 rg")
    text(left, bottom - 14, "0", 8)
    text(left + width - 20, bottom - 14, "5,000 steps", 8)
    text(left - 18, bottom + height - 4, "9", 8)
    text(left - 18, bottom, "2", 8)
    ops.append(f"{rgb(PRIMARY)} rg")
    text(left + width - 120, bottom + 60, "500-step warm-up", 9, "F2")
    ops.append(f"{rgb(SECONDARY)} rg")
    text(left + width - 120, bottom + 74, "no warm-up", 9, "F2")
    ops.append("0 0 0 rg")
    text(56, bottom - 34, "Figure 1. Mean training loss over 5,000 steps, 3 seeds per arm.", 9)
    y = bottom - 64
    text(56, y, "Limits", 12, "F2")
    for i, line in enumerate(["Three seeds and one model size. The claim 'warm-up keeps the final",
                              "loss' stays open until the learning-rate sweep (X-0002) finishes."]):
        text(56, y - 16 - i * 14, line, 10)
    text(56, y - 62, "References: Goyal et al. 2017; Liu et al. 2020 (see references.bib).", 9)

    content = "\n".join(ops).encode("latin-1")
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R "
        b"/Resources << /Font << /F1 5 0 R /F2 6 0 R >> >> >>",
        b"<< /Length " + str(len(content)).encode() + b" >>\nstream\n" + content + b"\nendstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>",
        b"<< /Title (Warm-up tutorial note) /Producer (ARK Studio build_demo_assets.py) >>",
    ]
    out = io.BytesIO()
    out.write(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
    offsets = []
    for number, body in enumerate(objects, start=1):
        offsets.append(out.tell())
        out.write(f"{number} 0 obj\n".encode() + body + b"\nendobj\n")
    xref = out.tell()
    out.write(f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode())
    for offset in offsets:
        out.write(f"{offset:010d} 00000 n \n".encode())
    out.write(f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R /Info 7 0 R >>\n"
              f"startxref\n{xref}\n%%EOF\n".encode())
    return out.getvalue()


def build_map(out: Path) -> None:
    types = list(tag_vocab.KNOWN_TYPES.items())
    cols = 4
    rows = math.ceil(len(types) / cols)
    svg = Svg(1200, 150 + rows * 54 + 80, "Map legend: unit types and colours",
              "Every unit type with its colour and ID prefix, as drawn on the Map.")
    svg.heading("Map legend", "Node colour = type. The ID prefix tells you the type in text, too.")
    for i, (key, info) in enumerate(types):
        cx = 32 + (i % cols) * 288
        cy = 110 + (i // cols) * 54
        svg.rect(cx, cy, 270, 42, fill="#FFFFFF")
        svg.circle(cx + 24, cy + 21, 10, fill=info["color"], stroke=INK, sw=0.5)
        svg.text(cx + 44, cy + 26, info["label"], size=14, weight=600)
        svg.text(cx + 254, cy + 26, f"{info['prefix']}-0001", size=12, color=INK2, anchor="end",
                 family=MONO)
    y = 110 + rows * 54 + 30
    svg.text(32, y, "Status shows as the ring: open · active · review · blocked · done. "
             "Importance p0–p3 sets the size.", size=13, color=INK2)
    (out / "map-legend.svg").write_text(svg.render(), encoding="utf-8")


# The example task used for the goal manifest (rendered from the agent-team templates).
def example_manifest_task() -> dict:
    task = agent_team.example_task({"id": "ark-demo", "name": "ARK Studio Demo",
                                    "next_task_id": "T-0001"})
    task.update({
        "title": "Run the 3-seed warm-up comparison",
        "owner": "agent:codex",
        "reviewer": "human:you",
        "repository": "warmup-study",
        "base_revision": "main@4f2c1aa",
        "budget": "one GPU (20 GB claim), 2 GPU-hours, no new dependency",
        "background": "Early loss spikes appear in the 60M model without warm-up (I-0001).",
        "objective": "Train both arms for 3 seeds and report final loss and spikes per run.",
        "deliverables": ["results.csv with per-step mean loss", "results-chart.svg",
                         "inspection report with commands and checksums"],
        "non_goals": ["tuning other hyper-parameters", "changing the model code"],
        "owned_paths": ["configs/warmup/**", "reports/warmup/**"],
        "acceptance": [
            {"command": "python train.py --config configs/warmup/smoke.yaml --steps 50",
             "threshold": "exits 0, loss is finite", "evidence": "smoke log"},
            {"command": "python analyse.py reports/warmup/ --out results.csv",
             "threshold": "3 seeds per arm present", "evidence": "results.csv"},
        ],
        "task_structure": "sequential",
        "team": {"mode": "solo", "size": 1, "heterogeneous": False,
                 "rationale": "one route, one GPU; a team would only add coordination cost",
                 "roles": ["worker"]},
        "handoff": {"contract": "Reviewer receives results.csv, the chart, logs and residual risks.",
                    "inputs": ["main@4f2c1aa"], "outputs": ["results.csv", "results-chart.svg"]},
        "risks": ["the shared GPU is busy and the job waits in the queue"],
        "stop_conditions": ["loss becomes NaN", "the GPU claim cannot be met for 2 hours"],
    })
    return task


def goal_manifest_markdown() -> str:
    from arkstudio.skills import protocol
    original = protocol.utc_now
    protocol.utc_now = lambda: "2026-11-02T09:00:00+00:00"  # deterministic output
    try:
        result = agent_team.prepare_task(example_manifest_task())
    finally:
        protocol.utc_now = original
    if not result["ok"]:
        raise SystemExit(f"example manifest is invalid: {result['errors']}")
    return result["markdown"]


def inspection_report_markdown() -> str:
    return agent_team.render_inspection_report({
        "task_id": "T-0001",
        "owner": "agent:codex",
        "reviewer": "human:you",
        "phase": "review",
        "status": "waiting",
        "base_revision": "main@4f2c1aa",
        "candidate_revision": "task/T-0001@9b7e310",
        "integration_revision": "not merged (waiting for review)",
        "results": ("Both arms trained for 3 seeds x 5,000 steps. Warm-up: final loss 2.08, "
                    "0.0 spikes per run. No warm-up: final loss 2.14, 3.0 spikes per run."),
        "deliverables": "- results.csv\n- results-chart.svg\n- run logs (3 per arm)",
        "changed_files": "- configs/warmup/arm_a.yaml\n- configs/warmup/arm_b.yaml\n"
                         "- reports/warmup/results.csv",
        "commands": ("1. `python train.py --config configs/warmup/smoke.yaml --steps 50` → exit 0\n"
                     "2. `python train.py --config configs/warmup/arm_b.yaml --seed {0,1,2}` → exit 0\n"
                     "3. `python analyse.py reports/warmup/ --out results.csv` → 6 runs found"),
        "deviations": "- None. The job waited 14 minutes in the GPU queue for its 20 GB claim.",
        "artifact_inventory": ("- reports/warmup/results.csv  sha256:3f1d…a9\n"
                               "- reports/warmup/results-chart.svg  sha256:77b0…12"),
        "residual_risks": ("- Three seeds, one model size.\n"
                           "- Claim C-0002 (final loss unchanged) still needs the LR sweep X-0002."),
    })


RUN_LOG = """\
[2026-11-02 09:14:03] run T-0001  provider=codex  worktree=.worktrees/T-0001  access=accept-edits
[2026-11-02 09:14:05] gate scope   ok   2 files changed, all inside configs/warmup/**
[2026-11-02 09:14:09] gate static  ok   yaml parses, no secrets found
[2026-11-02 09:14:41] gate smoke   ok   50 steps, loss 8.21 -> 7.96, finite
[2026-11-02 09:14:42] needs human  GPU dispatch (readiness.requires_human)  -> waiting for approval
[2026-11-02 09:16:10] approved     by human:you  "go, one GPU only"
[2026-11-02 09:16:11] gpu queue    job warmup-b-s0  claim gpu_memory_mb=20000  state=queued
[2026-11-02 09:30:27] gpu queue    job warmup-b-s0  -> lab-a100:GPU 2 (util 7%, free 61 GB)
[2026-11-02 10:02:55] train        arm=warmup seed=0 step=5000 loss=2.081 spikes=0
[2026-11-02 10:35:12] train        arm=none   seed=0 step=5000 loss=2.143 spikes=3 (750,1500,2250)
[2026-11-02 11:41:30] analyse      6 runs, results.csv written (21 rows)
[2026-11-02 11:41:33] report       inspection_report.md attached to T-0001 -> status=review
"""

GPU_REQUEST = {
    "_comment": ("Example only. The tutorial never submits this. POST /api/gpu/jobs adds a job to "
                 "the queue; the dispatcher keeps GPUs whose available memory fits the claim, "
                 "then takes the least-utilised one."),
    "name": "warmup-b-s0",
    "task": "T-0001",
    "command": "python train.py --config configs/warmup/arm_b.yaml --seed 0",
    "work_dir": "/data/projects/warmup-study",
    "gpus": 1,
    "gpu_memory_mb": 20000,
    "timeout_minutes": 120,
    "env": {"WANDB_MODE": "offline"},
}


def build_execute(out: Path) -> None:
    (out / "goal_manifest.md").write_text(goal_manifest_markdown(), encoding="utf-8")
    (out / "inspection_report.md").write_text(inspection_report_markdown(), encoding="utf-8")
    (out / "run-log-excerpt.log").write_text(RUN_LOG, encoding="utf-8")
    (out / "gpu-queue-request.json").write_text(json.dumps(GPU_REQUEST, indent=2) + "\n",
                                                encoding="utf-8")
    # GPU dispatch figure
    gpus = [  # name, util %, free MB, claimed-by-ARK MB
        ("GPU 0", 92, 30000, 0), ("GPU 1", 15, 12000, 0), ("GPU 2", 7, 61000, 0),
        ("GPU 3", 3, 40000, 30000),
    ]
    claim = 20000
    svg = Svg(1200, 560, "GPU dispatch example",
              "Four GPUs; a job claiming 20 GB. GPU 1 and GPU 3 are filtered out for memory; "
              "GPU 2 wins because it is the least utilised of the GPUs that fit.")
    svg.heading("How the GPU queue picks a GPU",
                f"Job warmup-b-s0 claims gpu_memory_mb = {claim:,} (20 GB) on one GPU.")
    svg.text(32, 118, "1  Filter: available = free − already claimed by ARK ≥ claim", size=15,
             weight=700)
    svg.text(632, 118, "2  Rank: least utilised first (ties: more memory)", size=15, weight=700)
    for i, (name, util, free, claimed) in enumerate(gpus):
        y = 146 + i * 92
        available = free - claimed
        fits = available >= claim
        win = name == "GPU 2"
        svg.rect(32, y, 560, 76, fill="#FFFFFF" if fits else PANEL,
                 stroke=SUCCESS if win else BASELINE, sw=2.5 if win else 1)
        svg.text(52, y + 30, name, size=16, weight=700)
        bar_w = 300
        svg.rect(150, y + 16, bar_w, 16, fill=PANEL, stroke="none", r=4)
        svg.rect(150, y + 16, round(bar_w * min(free, 80000) / 80000), 16, fill=PRIMARY,
                 stroke="none", r=4, opacity=0.35)
        svg.rect(150, y + 16, round(bar_w * min(available, 80000) / 80000), 16, fill=PRIMARY,
                 stroke="none", r=4)
        cx = 150 + round(bar_w * claim / 80000)
        svg.line(cx, y + 10, cx, y + 38, color=FAILURE, sw=2)
        svg.text(460, y + 29, f"{available / 1000:.0f} GB avail.", size=12, color=INK2)
        svg.text(150, y + 56, (f"free {free / 1000:.0f} GB" +
                               (f" − {claimed / 1000:.0f} GB launching" if claimed else "")),
                 size=12, color=INK2)
        mark = "fits" if fits else "too small → filtered out"
        svg.text(572, y + 56, mark, size=12, color=SUCCESS if fits else FAILURE, weight=600,
                 anchor="end")
        if fits:
            rx = 632
            svg.rect(rx, y, 536, 76, fill="#FFFFFF", stroke=SUCCESS if win else BASELINE,
                     sw=2.5 if win else 1)
            svg.text(rx + 20, y + 30, f"{name}  ·  utilisation {util}%", size=15, weight=700)
            svg.rect(rx + 20, y + 44, 300, 12, fill=PANEL, stroke="none", r=4)
            svg.rect(rx + 20, y + 44, 3 * util, 12, fill=SECONDARY, stroke="none", r=4)
            svg.text(rx + 516, y + 30, "chosen ✓" if win else "skipped: busier", size=13,
                     color=SUCCESS if win else INK2, weight=700, anchor="end")
            svg.line(596, y + 38, rx - 4, y + 38, arrow=True)
    svg.text(32, 530, "The red tick on each bar is the 20 GB claim. The claim is reserved on "
             "the chosen GPU before the next job is considered.", size=13, color=INK2)
    (out / "gpu-dispatch.svg").write_text(svg.render(), encoding="utf-8")


def build_output(out: Path) -> None:
    shutil.copyfile(ROOT / "arkstudio" / "skills" / "assets" / "design" / "theme.json",
                    out / "theme.json")
    roles = THEME["color"]["roles"]
    svg = Svg(1200, 820, "Colour board: academic-clean theme",
              "The design skill's example theme: twelve colour roles, categorical, sequential "
              "and diverging ramps, and the type scale.")
    svg.heading("Colour board · academic-clean",
                "From the design skill's theme.json. Name colours by role, never by appearance.")
    for i, (name, info) in enumerate(roles.items()):
        col, row = i % 4, i // 4
        x, y = 32 + col * 288, 100 + row * 150
        value = info["value"]
        svg.rect(x, y, 270, 132, fill="#FFFFFF")
        svg.rect(x + 10, y + 10, 250, 64, fill=value, stroke=BASELINE, sw=0.5, r=6,
                 opacity=info.get("opacity", 1.0))
        ink = "#FFFFFF" if design.contrast_ratio("#FFFFFF", value) >= 4.5 else INK
        ratio = design.contrast_ratio(value, SURFACE)
        svg.text(x + 22, y + 50, value, size=14, color=ink, weight=700, family=MONO)
        svg.text(x + 14, y + 96, name, size=14, weight=700, family=MONO)
        svg.text(x + 14, y + 116, info["usage"][:38], size=11, color=INK2)
        svg.text(x + 256, y + 96, f"{ratio:.1f}:1", size=11, color=INK2, anchor="end")
    y = 568
    for label, ramp in (("categorical", THEME["color"]["categorical"]),
                        ("sequential", THEME["color"]["sequential"]),
                        ("diverging", THEME["color"]["diverging"])):
        svg.text(32, y + 22, label, size=13, weight=700, family=MONO)
        for j, value in enumerate(ramp):
            svg.rect(170 + j * 96, y, 90, 34, fill=value, stroke=BASELINE, sw=0.5, r=4)
        svg.text(170 + len(ramp) * 96 + 10, y + 22, " · ".join(ramp), size=11, color=INK2,
                 family=MONO)
        y += 50
    typo = THEME["typography"]
    svg.text(32, y + 30, "Aa", size=30, weight=700)
    svg.text(90, y + 22, f"sans  {typo['sans']['family'][0]}   ·   serif  "
             f"{typo['serif']['family'][0]}   ·   mono  {typo['mono']['family'][0]}", size=13)
    sizes = typo["roles_pt"]
    svg.text(90, y + 42, "sizes (pt): " + ", ".join(f"{k} {v:g}" for k, v in sizes.items()),
             size=12, color=INK2)
    (out / "colour-board.svg").write_text(svg.render(), encoding="utf-8")

    # poster / cover
    svg = Svg(900, 1200, "Poster: warm-up removes early loss spikes",
              "A poster cover in the academic-clean theme with the main result chart.")
    svg.rect(0, 0, 900, 220, fill=PRIMARY, stroke="none", r=0)
    svg.text(60, 96, "Warm-up removes", size=54, color="#FFFFFF", weight=700)
    svg.text(60, 158, "early loss spikes", size=54, color="#FFFFFF", weight=700)
    svg.text(60, 198, "A 60M-parameter study · ARK Studio tutorial", size=20, color="#E6EEF2")
    svg.add(f'<g transform="translate(30 250) scale(0.875)">'
            + chart_svg(960, 520, title="Mean training loss, 3 seeds per arm",
                        subtitle="Shaded: the 500-step warm-up window")
            .split("\n", 1)[1].rsplit("</svg>", 1)[0].replace("<title>", "<desc>")
            .replace("</title>", "</desc>").replace('id="arrow"', 'id="arrow-p"')
            .replace('id="arrow-focus"', 'id="arrow-focus-p"')
            + "</g>")
    stats = [("0.0", "spikes per run", "with warm-up"), ("3.0", "spikes per run", "without"),
             ("2.08", "final loss", "vs 2.14 without")]
    for i, (big, label, sub) in enumerate(stats):
        x = 60 + i * 270
        svg.rect(x, 740, 240, 170, fill=PANEL)
        svg.text(x + 24, 812, big, size=56, color=PRIMARY if i != 1 else SECONDARY, weight=700)
        svg.text(x + 24, 850, label, size=18, weight=600)
        svg.text(x + 24, 876, sub, size=15, color=INK2)
    for i, (label, value) in enumerate((
            ("Question", "Does a 500-step warm-up remove early loss spikes?"),
            ("Method", "2 arms × 3 seeds × 5,000 steps, one GPU (20 GB claim)"),
            ("Limits", "one model size; the final-loss claim is still open"))):
        svg.text(60, 980 + i * 36, label, size=18, weight=700)
        svg.text(170, 980 + i * 36, value, size=18)
    svg.text(60, 1150, "Fictional data · built with ARK Studio · theme: academic-clean", size=14,
             color=INK2)
    (out / "poster-cover.svg").write_text(svg.render(), encoding="utf-8")

    (out / "fig-main-result.svg").write_text(chart_svg(
        720, 420, title="Figure 1 · Warm-up removes early spikes",
        subtitle="Mean training loss, 3 seeds per arm"), encoding="utf-8")


SWEEP_HTML = """\
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Learning-rate sweep</title>
<style>
  :root { --surface:#F7F5EF; --panel:#ECE8DE; --ink:#111827; --ink2:#4B5563;
          --a:#1F5A7A; --b:#C2683F; --grid:#E2DED3; }
  @media (prefers-color-scheme: dark) {
    :root { --surface:#14181F; --panel:#1F2530; --ink:#E5E7EB; --ink2:#9CA3AF;
            --a:#5B9BC4; --b:#D98157; --grid:#2A3140; }
  }
  body { margin:0; padding:16px; background:var(--surface); color:var(--ink);
         font:14px/1.45 Inter, "Segoe UI", Arial, sans-serif; }
  h1 { font-size:17px; margin:0 0 2px; }
  p { margin:0 0 10px; color:var(--ink2); font-size:13px; }
  .row { display:flex; gap:8px; margin:0 0 8px; }
  button { font:inherit; font-size:12px; padding:4px 10px; border-radius:6px; cursor:pointer;
           border:1px solid var(--ink2); background:transparent; color:var(--ink); }
  button[aria-pressed="true"] { background:var(--ink); color:var(--surface); }
  #tip { position:fixed; pointer-events:none; background:var(--ink); color:var(--surface);
         padding:4px 8px; border-radius:6px; font-size:12px; display:none; }
  svg text { fill:var(--ink2); font-size:11px; }
  table { border-collapse:collapse; font-size:12px; margin-top:8px; }
  td, th { padding:2px 10px; border-bottom:1px solid var(--grid); text-align:right; }
</style>
</head>
<body>
<h1>Learning-rate sweep (X-0002)</h1>
<p>Final loss and loss spikes per run for six peak learning rates, mean of 3 seeds.
Fictional tutorial data. Hover a bar for the exact value.</p>
<div class="row" role="group" aria-label="Metric">
  <button type="button" data-metric="loss" aria-pressed="true">Final loss</button>
  <button type="button" data-metric="spikes" aria-pressed="false">Spikes per run</button>
</div>
<div id="chart"></div>
<div id="tip"></div>
<table id="table" aria-label="Data table"></table>
<script>
(() => {
  const rows = __ROWS__;
  const chart = document.getElementById("chart");
  const tip = document.getElementById("tip");
  const W = 560, H = 260, L = 48, R = 12, T = 14, B = 34;
  function draw(metric) {
    const key = metric === "loss" ? 1 : 2;
    const max = metric === "loss" ? 3.5 : 8;
    const step = (W - L - R) / rows.length;
    let out = "";
    for (let g = 0; g <= 4; g += 1) {
      const v = max * g / 4, y = H - B - (H - T - B) * g / 4;
      out += `<line x1="${L}" x2="${W - R}" y1="${y}" y2="${y}" stroke="var(--grid)"/>`;
      out += `<text x="${L - 6}" y="${y + 4}" text-anchor="end">${v.toFixed(metric === "loss" ? 1 : 0)}</text>`;
    }
    rows.forEach((r, i) => {
      const h = (H - T - B) * r[key] / max, x = L + i * step + step * 0.2;
      const best = metric === "loss" ? r[1] === Math.min(...rows.map((q) => q[1])) : false;
      out += `<rect data-i="${i}" x="${x}" y="${H - B - h}" width="${step * 0.6}" height="${Math.max(h, 1)}"
        rx="4" fill="${key === 1 ? "var(--a)" : "var(--b)"}" opacity="${best ? 1 : 0.78}"/>`;
      out += `<text x="${x + step * 0.3}" y="${H - B + 16}" text-anchor="middle">${r[0]}</text>`;
    });
    out += `<text x="${(W + L) / 2}" y="${H - 2}" text-anchor="middle">peak learning rate</text>`;
    chart.innerHTML = `<svg viewBox="0 0 ${W} ${H}" width="100%" role="img"
      aria-label="Bar chart of ${metric === "loss" ? "final loss" : "spikes per run"} by learning rate">${out}</svg>`;
    chart.querySelectorAll("rect").forEach((el) => {
      el.addEventListener("mousemove", (ev) => {
        const r = rows[Number(el.dataset.i)];
        tip.textContent = `lr ${r[0]} · ${metric === "loss" ? "final loss " + r[1].toFixed(2) : r[2].toFixed(1) + " spikes/run"}`;
        tip.style.display = "block";
        tip.style.left = `${ev.clientX + 12}px`;
        tip.style.top = `${ev.clientY + 12}px`;
      });
      el.addEventListener("mouseleave", () => { tip.style.display = "none"; });
    });
  }
  document.querySelectorAll("button[data-metric]").forEach((button) => {
    button.addEventListener("click", () => {
      document.querySelectorAll("button[data-metric]").forEach((b) =>
        b.setAttribute("aria-pressed", String(b === button)));
      draw(button.dataset.metric);
    });
  });
  document.getElementById("table").innerHTML = "<tr><th>lr</th><th>final loss</th><th>spikes/run</th></tr>" +
    rows.map((r) => `<tr><td>${r[0]}</td><td>${r[1].toFixed(2)}</td><td>${r[2].toFixed(1)}</td></tr>`).join("");
  draw("loss");
})();
</script>
</body>
</html>
"""


def sweep_html() -> str:
    return SWEEP_HTML.replace("__ROWS__", json.dumps([list(row) for row in SWEEP]))


# ---------------------------------------------------- raster: frames + PNG
FONT5 = {  # 5x7 bitmap digits/letters used for frame labels
    "0": "01110100011001110101110011000101110", "1": "00100011000010000100001000010001110",
    "2": "01110100010000100010001000100011111", "3": "11111000100010000010000011000101110",
    "4": "00010001100101010010111110001000010", "5": "11111100001111000001000011000101110",
    "6": "00110010001000011110100011000101110", "7": "11111000010001000100010000100001000",
    "8": "01110100011000101110100011000101110", "9": "01110100011000101111000010001001100",
    ",": "00000000000000000000001100010001000", " ": "0" * 35,
}


def _hex(color: str) -> tuple[int, int, int]:
    return tuple(int(color[i:i + 2], 16) for i in (1, 3, 5))


def render_frame(progress: float, width: int = 640, height: int = 360) -> bytes:
    """Raw RGB24 frame: the two loss curves drawn up to ``progress`` (0..1)."""
    buf = bytearray(bytes(_hex(SURFACE)) * (width * height))

    def put(x, y, color):
        if 0 <= x < width and 0 <= y < height:
            i = (y * width + x) * 3
            buf[i:i + 3] = bytes(color)

    def fill(x0, y0, x1, y1, color):
        for y in range(max(0, y0), min(height, y1)):
            row = bytes(color) * max(0, min(width, x1) - max(0, x0))
            i = (y * width + max(0, x0)) * 3
            buf[i:i + len(row)] = row

    def seg(x0, y0, x1, y1, color, thick=2):
        n = max(abs(x1 - x0), abs(y1 - y0), 1)
        for k in range(int(n) + 1):
            x = round(x0 + (x1 - x0) * k / n)
            y = round(y0 + (y1 - y0) * k / n)
            fill(x - thick // 2, y - thick // 2, x + thick - thick // 2, y + thick - thick // 2, color)

    def label(x, y, text, color, scale=2):
        for ch in text:
            glyph = FONT5.get(ch, FONT5[" "])
            for gy in range(7):
                for gx in range(5):
                    if glyph[gy * 5 + gx] == "1":
                        fill(x + gx * scale, y + gy * scale, x + (gx + 1) * scale,
                             y + (gy + 1) * scale, color)
            x += 6 * scale

    left, top, right, bottom = 60, 30, width - 30, height - 50
    grid = _hex(PANEL)
    for v in range(2, 10, 1):
        y = round(bottom - (bottom - top) * (v - 2) / 7)
        fill(left, y, right, y + 1, grid)
        label(left - 18, y - 6, str(v), _hex(INK2))
    fill(left, bottom, right, bottom + 2, _hex(BASELINE))
    fill(left, top, round(left + (right - left) * 0.1), bottom, (226, 233, 236))
    end_step = 5000 * progress
    for index, color in ((2, _hex(SECONDARY)), (1, _hex(PRIMARY))):
        prev = None
        for step in range(0, 5001, 25):
            if step > end_step:
                break
            # interpolate between the 250-step samples
            i = min(step // 250, len(RESULTS) - 2)
            a, b = RESULTS[i], RESULTS[i + 1]
            t = (step - a[0]) / 250.0
            value = a[index] + (b[index] - a[index]) * t
            x = round(left + (right - left) * step / 5000)
            y = round(bottom - (bottom - top) * (value - 2.0) / 7.0)
            if prev:
                seg(prev[0], prev[1], x, y, color, 3)
            prev = (x, y)
        if prev:
            fill(prev[0] - 4, prev[1] - 4, prev[0] + 5, prev[1] + 5, color)
    label(left, height - 30, f"{int(end_step):,}".rjust(5), _hex(INK), 3)
    fill(right - 150, height - 36, right - 120, height - 32, _hex(PRIMARY))
    fill(right - 150, height - 22, right - 120, height - 18, _hex(SECONDARY))
    return bytes(buf)


def png_bytes(rgb: bytes, width: int, height: int) -> bytes:
    raw = b"".join(b"\x00" + rgb[y * width * 3:(y + 1) * width * 3] for y in range(height))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return (struct.pack(">I", len(data)) + kind + data
                + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF))

    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b""))


def build_video(out: Path, *, fps: int = 12, seconds: float = 4.0) -> str:
    ffmpeg = shutil.which("ffmpeg")
    if not ffmpeg:
        return "skipped: ffmpeg not found on PATH"
    width, height = 640, 360
    frames = int(fps * seconds)
    hold = fps  # one second on the final frame
    with tempfile.TemporaryDirectory() as tmp:
        raw = Path(tmp) / "frames.rgb"
        with raw.open("wb") as handle:
            for i in range(frames + hold):
                handle.write(render_frame(min(1.0, i / frames), width, height))
        cmd = [ffmpeg, "-y", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgb24",
               "-s", f"{width}x{height}", "-r", str(fps), "-i", str(raw),
               "-c:v", "libx264", "-pix_fmt", "yuv420p", "-preset", "veryslow", "-crf", "28",
               "-movflags", "+faststart", "-map_metadata", "-1", "-fflags", "+bitexact",
               "-flags:v", "+bitexact", str(out / "warmup-curves.mp4")]
        result = subprocess.run(cmd, capture_output=True, text=True)
        if result.returncode != 0:
            return f"skipped: ffmpeg failed: {result.stderr.strip()[:200]}"
    return "ok"


def build_editor(out: Path) -> str:
    with (out / "lr-sweep.csv").open("w", encoding="utf-8", newline="") as handle:
        writer = csv.writer(handle, lineterminator="\n")
        writer.writerow(["learning_rate", "final_loss", "spikes_per_run"])
        writer.writerows(SWEEP)
    (out / "lr-sweep.html").write_text(sweep_html(), encoding="utf-8")
    (out / "warmup-curves-poster.png").write_bytes(png_bytes(render_frame(1.0), 640, 360))
    return build_video(out)


def build_agents(out: Path) -> None:
    svg = Svg(1200, 520, "Human-in-the-loop: where ARK asks you",
              "A run timeline with three gates where a person must act: setup confirmation, "
              "approval before risky actions, and reviewer acceptance.")
    svg.heading("Where the agent stops and asks you",
                "Agents do the work. Three decisions stay with a person.")
    lane = [("Agent reads", "GET /setup, /context,", "/skills/agent-team"),
            ("Setup", "draft → proposed", "→ YOU confirm"),
            ("Manifest", "validate goal", "manifest"),
            ("Work", "worktree, gates", "cheapest first"),
            ("Risky step?", "push, GPU, delete,", "post → YOU approve"),
            ("Report", "inspection report", "+ evidence"),
            ("Review", "YOU (or reviewer)", "accept or return")]
    human = {1, 4, 6}
    for i, (title, l1, l2) in enumerate(lane):
        x = 32 + i * 164
        fill = "#FDF3E1" if i in human else "#FFFFFF"
        svg.rect(x, 130, 148, 130, fill=fill, stroke=WARNING if i in human else BASELINE,
                 sw=2 if i in human else 1)
        svg.text(x + 74, 164, title, size=15, weight=700, anchor="middle")
        svg.text(x + 74, 196, l1, size=12, color=INK2, anchor="middle")
        svg.text(x + 74, 216, l2, size=12, color=INK2, anchor="middle")
        if i in human:
            svg.text(x + 74, 246, "● human", size=12, color=WARNING, weight=700, anchor="middle")
        if i < len(lane) - 1:
            svg.line(x + 150, 195, x + 162, 195, arrow=True)
    svg.text(32, 316, "What the agent may do on its own comes from the confirmed setup profile "
             "(the authority section).", size=14)
    svg.text(32, 342, "Anything listed in readiness.requires_human waits for you, even if the "
             "agent is sure.", size=14)
    svg.box(32, 380, 556, 110, "Agent token", [
        "Assistant → Agent API → Reveal token.",
        "Send it as  Authorization: Bearer <token>.",
        "Never paste it into files, units or reports."], accent=FAILURE, line_size=13)
    svg.box(612, 380, 556, 110, "Skills the agent reads", [
        "setup · agent-team · rrsi · design",
        "GET /api/agent/v1/skills/<name>",
        "Settings → Skills shows the same catalogue."], accent=PRIMARY, line_size=13)
    (out / "human-in-the-loop.svg").write_text(svg.render(), encoding="utf-8")


def build_settings(out: Path) -> None:
    svg = Svg(1200, 520, "Settings at a glance",
              "Settings tabs grouped by scope: Project, Personal and Admin, plus the precedence "
              "rule.")
    svg.heading("Settings at a glance", "Three scopes. A personal value always wins over a project value.")
    groups = [
        ("Project", "shared with the project's members", PRIMARY,
         ["Graph", "Project agents", "Project assistant", "Skills", "Providers", "Data", "Mobile",
          "Remote", "GPU"]),
        ("Personal", "only you see these", SECONDARY,
         ["Assistant (your model + key)", "Agents (your commands)", "Account", "Linux account",
          "Shortcuts"]),
        ("Admin", "organisation owners and admins", "#7C5C9E",
         ["Users", "Teams", "Project grants", "Linux identities"]),
    ]
    for i, (name, hint, color, items) in enumerate(groups):
        x = 32 + i * 384
        svg.rect(x, 100, 360, 300, fill="#FFFFFF")
        svg.rect(x, 100, 360, 8, fill=color, stroke="none", r=4)
        svg.text(x + 20, 140, name, size=18, weight=700)
        svg.text(x + 20, 162, hint, size=12, color=INK2)
        for j, item in enumerate(items):
            svg.text(x + 20 + (j // 5) * 170, 196 + (j % 5) * 30, "• " + item, size=13)
    svg.text(32, 440, "Precedence, field by field:", size=14, weight=700)
    chain = ["personal", "project", "server / env", "built-in default"]
    x = 240
    for k, item in enumerate(chain):
        svg.rect(x, 420, 150, 30, fill=PANEL, r=15)
        svg.text(x + 75, 440, item, size=13, anchor="middle", weight=600)
        if k < len(chain) - 1:
            svg.text(x + 168, 441, ">", size=16, color=INK2, anchor="middle", weight=700)
        x += 186
    svg.text(32, 486, "Mobile pairing defaults to full access and lasts until you revoke it in "
             "Settings → Mobile.", size=13, color=INK2)
    (out / "settings-map.svg").write_text(svg.render(), encoding="utf-8")


CRLF, LF = bytes([13, 10]), bytes([10])
TEXT_SUFFIXES = {".svg", ".md", ".csv", ".json", ".log", ".bib", ".html"}

BUILDERS = {
    "start": build_start, "home": build_home, "research": build_research, "map": build_map,
    "execute": build_execute, "output": build_output, "editor": build_editor,
    "agents": build_agents, "settings": build_settings,
}


def build(target: Path) -> dict:
    notes = {}
    for chapter, builder in BUILDERS.items():
        folder = target / chapter
        folder.mkdir(parents=True, exist_ok=True)
        result = builder(folder)
        if isinstance(result, str):
            notes[chapter] = result
    # Text assets are LF-only on every OS (and -text in .gitattributes), so the
    # bytes a Windows build writes match the committed ones.
    for path in target.rglob("*"):
        if path.is_file() and path.suffix in TEXT_SUFFIXES:
            data = path.read_bytes()
            if CRLF in data:
                path.write_bytes(data.replace(CRLF, LF))
    return notes


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--check", action="store_true",
                        help="rebuild into a temp folder and compare with the committed files")
    args = parser.parse_args(argv)
    if not args.check:
        notes = build(OUT)
        total = sum(p.stat().st_size for p in OUT.rglob("*") if p.is_file())
        for chapter, note in notes.items():
            if note != "ok":
                print(f"{chapter}: {note}")
        print(f"wrote {sum(1 for p in OUT.rglob('*') if p.is_file())} files, {total:,} bytes "
              f"to {OUT.relative_to(ROOT)}")
        return 0
    with tempfile.TemporaryDirectory() as tmp:
        fresh = Path(tmp)
        build(fresh)
        stale = []
        for path in sorted(fresh.rglob("*")):
            if not path.is_file() or path.suffix == ".mp4":
                continue
            committed = OUT / path.relative_to(fresh)
            if not committed.is_file() or committed.read_bytes() != path.read_bytes():
                stale.append(str(path.relative_to(fresh)))
        if stale:
            print("stale demo assets: " + ", ".join(stale))
            return 1
        print("demo assets are up to date")
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
