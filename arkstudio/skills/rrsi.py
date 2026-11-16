"""RRSI search regularizers, adapted to ARK agent work.

RRSI ("Regularized Recursive Self-Improvement of Agent Harnesses", Apache-2.0,
Google Research) evolves a frozen model's harness — prompts, control flow,
tools, memory, context management, skills and subagents — while regularising
the *search* rather than the harness content.  ARK does not ship a benchmark
runner, but a coding agent working inside ARK needs the same discipline when it
improves its own prompts, skills or tools.

This module adapts the reusable core algorithm, not the paper's benchmark
harnesses:

* the editable component vocabulary ``K`` and structural subset ``K_str``,
  including diff-based component recovery (:mod:`rrsi.components`);
* the annealed edit budget (:mod:`rrsi.schedule`);
* the edit history, stall/exploration/pruning logic (:mod:`rrsi.history`);
* the noise-adjusted floor, cost rule, shaped within-band rule and argmax
  selection (:mod:`rrsi.selection`);
* the leakage critic vocabulary, rendered role briefs and a JSONL evidence
  store so a run is auditable inside an ARK project.

No model client, benchmark, Docker, git mutation or third-party dependency is
required: agents call these functions and keep their own worktrees.
"""
from __future__ import annotations

import json
import math
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable, Mapping

SKILL_ID = "rrsi"
TEMPLATES = ("proposer.md", "analyst.md", "critic.md", "domain.json")

# Editable harness components.  Adapted from vendor/rrsi/rrsi/components.py.
COMPONENTS = (
    "prompt",
    "control_flow",
    "config",
    "output_plumbing",
    "context_mgmt",
    "client_tool",
    "skill",
    "memory",
    "subagent",
)
STRUCTURAL_COMPONENTS = ("client_tool", "skill", "memory", "subagent")

# Structural substrate signals.  ARK paths are added to the generic signals so
# a diff touching arkstudio/skills/ is not mislabelled as a prompt edit.
GENERIC_SIGNALS = (
    ("memory", (r"\bMemory\(", r"\.remember\(", r"\.recall\(", r"_STATE_DIR")),
    ("skill", (r"skills/", r"SkillRegistry", r"skill_use", r"skill_catalog",
               r"upload_skills", r"SKILL\.md")),
    ("client_tool", (r"ToolRegistry", r"register_tool", r"tool_spec", r"CLIENT_TOOLS")),
    ("subagent", (r"\bsubcall\(", r"sub_agent", r"subagent")),
)

# Default leakage patterns for an ARK coding task.  A candidate harness must
# not encode the answer, the hidden test, or a benchmark-specific trigger.
DEFAULT_LEAKAGE_PATTERNS = (
    r"\btests?/",
    r"\btest_[a-z0-9_]+\.py\b",
    r"\bexpected_(?:output|result|answer)\b",
    r"\bassert(?:Equal|In|True)\b",
    r"\bhidden[_ ]tests?\b",
    r"\bbenchmark[_ ]task\b",
    r"\bT-\d{3,}\b",  # a concrete ARK task id is task-specific content
)

_STRING_LINE = re.compile(r'^[+-]\s*(?:[frb]?["\']|""")')
_COMPONENT_RE = re.compile(r"^[a-z][a-z0-9_]*$")


class RrsiError(ValueError):
    """A round, edit record or configuration violates the RRSI contract."""


def _as_list(value: Any) -> list:
    if value is None:
        return []
    return list(value) if isinstance(value, (list, tuple)) else [value]


def _number(value: Any, name: str) -> float:
    if isinstance(value, bool):
        raise RrsiError(f"{name} must be a number")
    try:
        number = float(value)
    except (TypeError, ValueError) as exc:
        raise RrsiError(f"{name} must be a number") from exc
    if not math.isfinite(number):
        raise RrsiError(f"{name} must be finite")
    return number


def text_only(diff: str) -> bool:
    """True when every changed line is a string literal or a comment."""
    changed = [line for line in str(diff or "").splitlines()
               if (line.startswith("+") or line.startswith("-"))
               and not line.startswith("+++") and not line.startswith("---")
               and line[1:].strip()]
    if not changed:
        return False
    return all(_STRING_LINE.match(line) or line[1:].lstrip().startswith("#")
               for line in changed)


def classify_diff(diff: str, domain_signals: Iterable[tuple[str, Iterable[str]]] = ()) -> str:
    """First matching component in (domain signals, then generic); default prompt."""
    if text_only(diff):
        return "prompt"
    for component, patterns in list(domain_signals) + list(GENERIC_SIGNALS):
        if any(re.search(pattern, diff) for pattern in patterns):
            return component if component in COMPONENTS else "prompt"
    return "prompt"


def has_evidence(component: str, diff: str,
                 domain_signals: Iterable[tuple[str, Iterable[str]]] = ()) -> bool:
    """Does the diff carry a signal for the declared component?

    A proposer can otherwise name a skill/memory/tool edit without shipping
    one, or relabel a prompt tweak to fill an exploration slot.
    """
    for candidate, patterns in list(domain_signals) + list(GENERIC_SIGNALS):
        if candidate == component and any(re.search(pattern, diff) for pattern in patterns):
            return True
    return False


def normalize_component(declared: str | None, diff: str,
                        domain_signals: Iterable[tuple[str, Iterable[str]]] = ()) -> str:
    """Keep a declared component only when the diff supports it."""
    value = str(declared or "").strip().lower()
    if value in COMPONENTS and has_evidence(value, diff, domain_signals):
        return value
    return classify_diff(diff, domain_signals)


def novelty(edit_components: Iterable[str], accepted_counts: Mapping[str, int]) -> int:
    """Structural components the incumbent has never had an accepted edit on."""
    return sum(1 for component in set(edit_components)
               if component in STRUCTURAL_COMPONENTS
               and int(accepted_counts.get(component) or 0) == 0)


def edit_budget(t: int, T: int, b_min: int = 1, b_max: int = 3) -> int:
    """Annealed L0 budget: ``ceil(b_min + (b_max-b_min)/2 * (1+cos(pi t/T)))``."""
    b_min, b_max = int(b_min), int(b_max)
    if b_min < 1 or b_max < b_min:
        raise RrsiError("budget requires 1 <= b_min <= b_max")
    if T <= 0:
        return b_max
    t = max(0, min(int(t), int(T)))
    value = b_min + (b_max - b_min) * 0.5 * (1.0 + math.cos(math.pi * t / T))
    return int(math.ceil(round(value, 9)))


def budget_table(T: int, b_min: int = 1, b_max: int = 3) -> list[int]:
    return [edit_budget(t, T, b_min, b_max) for t in range(max(0, int(T)))]


def relative_cost_change(new_cost: float, incumbent_cost: float) -> float:
    """Relative mean-token change used by the cost rule."""
    new_cost = _number(new_cost, "new_cost")
    incumbent_cost = _number(incumbent_cost, "incumbent_cost")
    if incumbent_cost <= 0:
        return 0.0 if new_cost <= incumbent_cost else 1.0
    return (new_cost - incumbent_cost) / incumbent_cost


@dataclass
class SearchConfig:
    """Regularizer knobs; defaults follow the vendor coding instance."""

    delta: float = 0.017
    beta0: float = 0.0
    beta1: float = 0.5
    w_s: float = 1.0
    w_c: float = 0.25
    w_n: float = 0.1
    b_min: int = 1
    b_max: int = 3
    T: int = 10
    stall_window: int = 3
    recent_window: int = 3

    @classmethod
    def from_mapping(cls, value: Mapping[str, Any] | None) -> "SearchConfig":
        value = dict(value or {})
        allowed = set(cls.__dataclass_fields__)  # type: ignore[attr-defined]
        unknown = sorted(set(value) - allowed)
        if unknown:
            raise RrsiError(f"unknown RRSI config keys: {', '.join(unknown)}")
        config = cls(**value)
        config.validate()
        return config

    def validate(self) -> None:
        if self.delta < 0:
            raise RrsiError("delta must be >= 0")
        if self.beta0 < 0 or self.beta1 < 0:
            raise RrsiError("beta0 and beta1 must be >= 0")
        if self.w_s < 0 or self.w_c < 0 or self.w_n < 0:
            raise RrsiError("weights must be >= 0")
        if not 1 <= int(self.b_min) <= int(self.b_max):
            raise RrsiError("budget requires 1 <= b_min <= b_max")
        if int(self.T) < 0 or int(self.stall_window) < 1 or int(self.recent_window) < 1:
            raise RrsiError("T >= 0 and windows >= 1 are required")


@dataclass
class Candidate:
    variant: str
    edits: list[dict] = field(default_factory=list)
    score: float | None = None
    cost: float | None = None
    gate_failure: str | None = None
    commit: str | None = None
    worktree: str | None = None
    detail: str = ""

    @property
    def components(self) -> list[str]:
        return [str(edit.get("component")) for edit in self.edits if edit.get("component")]

    def validate(self) -> None:
        if not str(self.variant or "").strip():
            raise RrsiError("candidate variant is required")
        if not isinstance(self.edits, list) or not self.edits:
            raise RrsiError(f"candidate {self.variant}: at least one edit is required")
        for edit in self.edits:
            if not isinstance(edit, Mapping):
                raise RrsiError(f"candidate {self.variant}: edits must be objects")
            component = str(edit.get("component") or "").strip().lower()
            if not _COMPONENT_RE.match(component) or component not in COMPONENTS:
                raise RrsiError(f"candidate {self.variant}: unknown component {component!r}")
            if not str(edit.get("hypothesis") or "").strip():
                raise RrsiError(f"candidate {self.variant}: every edit needs a hypothesis")
        if self.score is not None:
            self.score = _number(self.score, "score")
        if self.cost is not None:
            self.cost = _number(self.cost, "cost")


@dataclass
class Decision:
    variant: str
    admissible: bool
    reason: str
    score: float | None = None
    cost: float | None = None
    delta_score: float | None = None
    delta_cost: float | None = None
    novelty: int = 0
    guards: list[str] = field(default_factory=list)

    def to_json(self) -> dict:
        return {
            "variant": self.variant,
            "admissible": self.admissible,
            "reason": self.reason,
            "score": self.score,
            "cost": self.cost,
            "delta_score": self.delta_score,
            "delta_cost": self.delta_cost,
            "novelty": self.novelty,
            "guards": list(self.guards),
        }


def cost_rule(delta_score: float, delta_cost: float, nov: int, delta: float,
              config: SearchConfig) -> tuple[bool, str]:
    """The cost / shaped-benefit rule from RRSI Algorithm 2 line 5."""
    if delta_score > delta:
        budget = config.beta0 + config.beta1 * delta_score
        ok = delta_cost <= budget
        return ok, (
            f"gain {delta_score:+.4f} > delta {delta:.4f}; cost change "
            f"{delta_cost:+.3f} {'<=' if ok else '>'} budget {budget:.3f} "
            f"(beta0 {config.beta0} + beta1 {config.beta1} * dS)")
    shaped = config.w_s * delta_score - config.w_c * delta_cost + config.w_n * nov
    ok = shaped > 0
    return ok, (
        f"gain {delta_score:+.4f} within delta {delta:.4f}; shaped "
        f"{config.w_s}*dS - {config.w_c}*dC + {config.w_n}*nu = {shaped:+.4f} "
        f"{'>' if ok else '<='} 0 (nu={nov})")


def judge(candidate: Candidate, *, incumbent_score: float, incumbent_cost: float,
          s_star: float, accepted_counts: Mapping[str, int],
          config: SearchConfig, guards: Iterable[str] | None = None) -> Decision:
    candidate.validate()
    if candidate.score is None:
        return Decision(candidate.variant, False,
                        candidate.gate_failure or "not evaluated")
    incumbent_score = _number(incumbent_score, "incumbent_score")
    incumbent_cost = _number(incumbent_cost, "incumbent_cost")
    s_star = _number(s_star, "s_star")
    delta_score = candidate.score - incumbent_score
    delta_cost = relative_cost_change(candidate.cost or 0.0, incumbent_cost)
    nov = novelty(candidate.components, accepted_counts)
    decision = Decision(candidate.variant, False, "", score=candidate.score,
                        cost=candidate.cost, delta_score=delta_score,
                        delta_cost=delta_cost, novelty=nov,
                        guards=list(guards or []))
    floor = s_star - config.delta
    if candidate.score < floor:
        decision.reason = (
            f"below noise-adjusted floor: S' {candidate.score:.4f} < S* {s_star:.4f} "
            f"- delta {config.delta:.4f}")
        return decision
    ok, why = cost_rule(delta_score, delta_cost, nov, config.delta, config)
    if not ok:
        decision.reason = f"cost rule failed: {why}"
        return decision
    if decision.guards:
        decision.reason = "domain guard violated: " + "; ".join(decision.guards)
        return decision
    decision.admissible = True
    decision.reason = f"admissible: {why}"
    return decision


def select_round(candidates: Iterable[Candidate], *, incumbent_score: float,
                 incumbent_cost: float, s_star: float,
                 accepted_counts: Mapping[str, int], config: SearchConfig,
                 guards: Mapping[str, Iterable[str]] | None = None) -> dict:
    """Judge every candidate; return the winner (or ``None``) and decisions."""
    guard_map = dict(guards or {})
    decisions = []
    candidates = list(candidates)
    for candidate in candidates:
        decisions.append(judge(
            candidate, incumbent_score=incumbent_score, incumbent_cost=incumbent_cost,
            s_star=s_star, accepted_counts=accepted_counts, config=config,
            guards=guard_map.get(candidate.variant)))
    admissible = [(candidate, decision) for candidate, decision in zip(candidates, decisions)
                  if decision.admissible]
    winner = max(admissible, key=lambda pair: pair[1].score)[0] if admissible else None
    return {
        "winner": winner.variant if winner else None,
        "commit": winner.commit if winner else None,
        "decisions": [decision.to_json() for decision in decisions],
        "config": {
            "delta": config.delta, "beta0": config.beta0, "beta1": config.beta1,
            "w_s": config.w_s, "w_c": config.w_c, "w_n": config.w_n,
        },
    }


@dataclass
class DomainSpec:
    """The small domain adapter ARK actually needs.

    The vendor's ``domains/<name>/adapter.py`` binds a benchmark, a runner and
    a harness checkout.  ARK keeps the contract and drops the benchmark: a
    domain names the evolvable harness path, its task splits and its metric so
    an agent can evaluate candidates without inheriting Docker or a benchmark
    client.
    """

    name: str
    harness_path: str
    evolve: list[str] = field(default_factory=list)
    heldout: list[str] = field(default_factory=list)
    smoke: list[str] = field(default_factory=list)
    metric: str = "task success rate"
    guards: list[str] = field(default_factory=list)
    component_signals: dict[str, list[str]] = field(default_factory=dict)

    def validate(self) -> None:
        self.name = str(self.name or "").strip()
        if not self.name:
            raise RrsiError("domain name is required")
        path = str(self.harness_path or "").strip().replace("\\", "/")
        if not path:
            raise RrsiError("harness_path is required")
        if path.startswith(("/", "\\")) or re.match(r"^[A-Za-z]:", path) or ".." in path.split("/"):
            raise RrsiError("harness_path must be repository-relative")
        self.harness_path = path
        for split_name in ("evolve", "heldout", "smoke"):
            values = [str(value).strip() for value in getattr(self, split_name)]
            if len(set(values)) != len(values):
                raise RrsiError(f"{split_name} contains duplicate task IDs")
            setattr(self, split_name, values)
        if not self.evolve:
            raise RrsiError("evolve must contain at least one task ID")
        # smoke commonly samples evolve (liveness checks); heldout must stay
        # unseen by both, otherwise the score is contaminated.
        overlap = (set(self.evolve) & set(self.heldout)) | (set(self.smoke) & set(self.heldout))
        if overlap:
            raise RrsiError("heldout must not overlap evolve or smoke: " + ", ".join(sorted(overlap)))
        self.metric = str(self.metric or "").strip() or "task success rate"
        for component in self.component_signals:
            if component not in COMPONENTS:
                raise RrsiError(f"unknown component signal: {component!r}")

    def to_json(self) -> dict:
        self.validate()
        return {
            "name": self.name,
            "harness_path": self.harness_path,
            "evolve": list(self.evolve),
            "heldout": list(self.heldout),
            "smoke": list(self.smoke),
            "metric": self.metric,
            "guards": list(self.guards),
            "component_signals": {key: list(value) for key, value in self.component_signals.items()},
        }


def validate_domain(payload: Mapping[str, Any]) -> dict:
    """Validate an ARK RRSI domain descriptor."""
    payload = dict(payload or {})
    signals = payload.get("component_signals") or {}
    if not isinstance(signals, Mapping):
        raise RrsiError("component_signals must be an object")
    spec = DomainSpec(
        name=payload.get("name") or "",
        harness_path=payload.get("harness_path") or "",
        evolve=[str(value) for value in _as_list(payload.get("evolve"))],
        heldout=[str(value) for value in _as_list(payload.get("heldout"))],
        smoke=[str(value) for value in _as_list(payload.get("smoke"))],
        metric=payload.get("metric") or "task success rate",
        guards=[str(value) for value in _as_list(payload.get("guards"))],
        component_signals={
            str(key): [str(item) for item in _as_list(value)]
            for key, value in signals.items()
        },
    )
    return {"ok": True, "domain": spec.to_json()}


def example_domain() -> dict:
    return {
        "name": "ark-coding",
        "harness_path": "arkstudio/skills",
        "evolve": ["T-0001", "T-0002", "T-0003"],
        "heldout": ["T-0004", "T-0005"],
        "smoke": ["T-0001"],
        "metric": "accepted task rate",
        "guards": ["unit tests must pass", "no new runtime dependency"],
        "component_signals": {
            "skill": ["arkstudio/skills/", r"SKILL\.md"],
            "client_tool": ["ToolRegistry", "register_tool"],
        },
    }


@dataclass
class EditRecord:
    """One measured edit.  Mirrors a JSONL line in the vendor edit history."""

    round: int
    component: str
    hypothesis: str
    verdict: str
    delta_score: float = 0.0
    delta_cost: float = 0.0
    variant: str = ""
    commit: str = ""
    evidence: str = ""

    def validate(self) -> None:
        if int(self.round) < 0:
            raise RrsiError("round must be >= 0")
        if self.component not in COMPONENTS:
            raise RrsiError(f"unknown component: {self.component!r}")
        if not str(self.hypothesis or "").strip():
            raise RrsiError("hypothesis is required")
        if self.verdict not in {"accepted", "rejected", "neutral", "invalid"}:
            raise RrsiError("verdict must be accepted, rejected, neutral or invalid")
        _number(self.delta_score, "delta_score")
        _number(self.delta_cost, "delta_cost")

    def to_json(self) -> dict:
        self.validate()
        return {
            "round": int(self.round), "component": self.component,
            "hypothesis": self.hypothesis, "verdict": self.verdict,
            "delta_score": float(self.delta_score),
            "delta_cost": float(self.delta_cost),
            "variant": self.variant, "commit": self.commit, "evidence": self.evidence,
        }


class History:
    """Edit history (``L_t``), tried set (``T_t``) and stall/exploration state."""

    def __init__(self, records: Iterable[Mapping[str, Any]] | None = None):
        self.records: list[dict] = []
        for record in records or []:
            self.add(record, validate=True)

    def add(self, record: EditRecord | Mapping[str, Any], *, validate: bool = True) -> dict:
        item = record.to_json() if isinstance(record, EditRecord) else dict(record)
        fields = {
            "round": item.get("round", 0),
            "component": str(item.get("component") or ""),
            "hypothesis": str(item.get("hypothesis") or ""),
            "verdict": str(item.get("verdict") or ""),
            "delta_score": item.get("delta_score", 0.0),
            "delta_cost": item.get("delta_cost", 0.0),
            "variant": str(item.get("variant") or ""),
            "commit": str(item.get("commit") or ""),
            "evidence": str(item.get("evidence") or ""),
        }
        try:
            edit = EditRecord(**fields)
        except TypeError as exc:
            raise RrsiError(f"invalid edit record: {exc}") from exc
        if validate:
            edit.validate()
        payload = edit.to_json()
        self.records.append(payload)
        return payload

    def accepted_counts(self) -> dict[str, int]:
        counts: dict[str, int] = {component: 0 for component in COMPONENTS}
        for record in self.records:
            if record["verdict"] == "accepted":
                counts[record["component"]] = counts.get(record["component"], 0) + 1
        return counts

    def tried_components(self) -> list[str]:
        return sorted({record["component"] for record in self.records
                       if record["verdict"] != "invalid"})

    def untried_components(self) -> list[str]:
        tried = set(self.tried_components())
        return [component for component in COMPONENTS if component not in tried]

    def stall_flag(self, delta: float, window: int | None = None) -> bool:
        """True when no accepted edit in the recent window moved beyond delta."""
        records = self.records[-int(window or 3):]
        if len(records) < int(window or 3):
            return False
        return not any(record["verdict"] == "accepted"
                       and float(record.get("delta_score") or 0) > float(delta)
                       for record in records)

    def exploration(self, config: SearchConfig) -> dict:
        """Reserved exploration slots when the run has stalled."""
        stalled = self.stall_flag(config.delta, config.stall_window)
        untried = self.untried_components()
        return {
            "stalled": stalled,
            "untried_components": untried,
            "reserved_component": untried[0] if (stalled and untried) else None,
        }

    def recent_yield(self, component: str, window: int | None = None) -> int:
        recent = self.records[-int(window or 3):]
        return sum(1 for record in recent
                   if record["component"] == component and record["verdict"] == "accepted"
                   and float(record.get("delta_score") or 0) > 0)

    def prune_set(self, window: int | None = None) -> list[str]:
        """Components exercised recently but producing no strictly improving edit."""
        recent = self.records[-int(window or 3):]
        exercised = {record["component"] for record in recent
                     if record["verdict"] != "invalid"}
        return sorted(component for component in exercised
                      if self.recent_yield(component, window) == 0)

    def to_json(self) -> dict:
        return {
            "records": list(self.records),
            "accepted_counts": self.accepted_counts(),
            "tried_components": self.tried_components(),
            "untried_components": self.untried_components(),
            "prune_set": self.prune_set(),
        }

    @classmethod
    def load_jsonl(cls, path: str | Path) -> "History":
        lines = Path(path).read_text(encoding="utf-8").splitlines()
        return cls(json.loads(line) for line in lines if line.strip())

    def append_jsonl(self, path: str | Path, record: EditRecord | Mapping[str, Any]) -> dict:
        payload = self.add(record)
        target = Path(path)
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(payload, ensure_ascii=False) + "\n")
        return payload


def validate_edit(payload: Mapping[str, Any], *,
                  domain_signals: Iterable[tuple[str, Iterable[str]]] = ()) -> dict:
    """Normalize one proposed edit and report whether its tag is evidenced."""
    payload = dict(payload or {})
    diff = str(payload.get("diff") or "")
    declared = str(payload.get("component") or "")
    component = normalize_component(declared, diff, domain_signals)
    return {
        "component": component,
        "declared_component": declared,
        "component_recovered": bool(declared and declared != component),
        "hypothesis": str(payload.get("hypothesis") or "").strip(),
        "diff_supplied": bool(diff),
        "evidence": has_evidence(component, diff, domain_signals) if diff else False,
    }


def scan_leakage(text: str, patterns: Iterable[str] | None = None) -> list[dict]:
    """Cheap leakage screen run before spending an evaluation."""
    findings = []
    for pattern in (list(patterns) if patterns is not None else list(DEFAULT_LEAKAGE_PATTERNS)):
        try:
            compiled = re.compile(pattern)
        except re.error as exc:
            raise RrsiError(f"invalid leakage pattern {pattern!r}: {exc}") from exc
        for line_number, line in enumerate(str(text or "").splitlines(), start=1):
            match = compiled.search(line)
            if match:
                findings.append({
                    "pattern": pattern,
                    "line": line_number,
                    "match": match.group(0),
                    "excerpt": line.strip()[:180],
                })
    return findings


def validate_round(payload: Mapping[str, Any]) -> dict:
    """Validate a JSON round request and apply RRSI selection."""
    payload = dict(payload or {})
    config = SearchConfig.from_mapping(payload.get("config"))
    incumbent = payload.get("incumbent") or {}
    incumbent_score = _number(incumbent.get("score"), "incumbent.score")
    incumbent_cost = _number(incumbent.get("cost", 0), "incumbent.cost")
    s_star = _number(payload.get("s_star", incumbent_score), "s_star")
    accepted_counts = payload.get("accepted_counts") or {}
    if not isinstance(accepted_counts, Mapping):
        raise RrsiError("accepted_counts must be an object")
    candidates = []
    for raw in _as_list(payload.get("candidates")):
        if not isinstance(raw, Mapping):
            raise RrsiError("candidates must be objects")
        candidates.append(Candidate(
            variant=str(raw.get("variant") or ""),
            edits=[dict(edit) for edit in _as_list(raw.get("edits"))],
            score=raw.get("score"),
            cost=raw.get("cost", 0),
            gate_failure=raw.get("gate_failure"),
            commit=raw.get("commit"),
            worktree=raw.get("worktree"),
            detail=str(raw.get("detail") or ""),
        ))
    if candidates:
        for candidate in candidates:
            candidate.validate()
            if len(candidate.edits) > edit_budget(
                    int(payload.get("round") or 0), config.T, config.b_min, config.b_max):
                raise RrsiError(
                    f"candidate {candidate.variant} exceeds the edit budget "
                    f"b_t={edit_budget(int(payload.get('round') or 0), config.T, config.b_min, config.b_max)}")
    return select_round(
        candidates, incumbent_score=incumbent_score, incumbent_cost=incumbent_cost,
        s_star=s_star, accepted_counts=accepted_counts, config=config,
        guards=payload.get("guards") if isinstance(payload.get("guards"), Mapping) else None)


def history_snapshot(payload: Mapping[str, Any]) -> dict:
    payload = dict(payload or {})
    history = History(payload.get("records") or [])
    config = SearchConfig.from_mapping(payload.get("config"))
    snapshot = history.to_json()
    snapshot["stall_flag"] = history.stall_flag(config.delta, config.stall_window)
    snapshot["exploration"] = history.exploration(config)
    snapshot["budget"] = budget_table(config.T, config.b_min, config.b_max)
    return snapshot


def build_briefs(context: Mapping[str, Any] | None = None) -> dict:
    """Render the proposer/analyst/critic role briefs for an actual round."""
    context = dict(context or {})
    defaults = {
        "task_title": "Improve the ARK coding-agent harness",
        "incumbent_score": "0.0000",
        "incumbent_cost": "0",
        "s_star": "0.0000",
        "delta": "0.017",
        "round_index": "0",
        "budget": "1",
        "components": ", ".join(COMPONENTS),
        "structural_components": ", ".join(STRUCTURAL_COMPONENTS),
        "prune_set": "_none_",
        "untried_components": ", ".join(COMPONENTS),
        "reserved_component": "_none_",
        "accepted_counts": "{}",
        "round_evidence": "_Round evidence has not been attached yet._",
        "diff": "",
    }
    defaults.update({key: value for key, value in context.items() if value is not None})
    return {
        "proposer": _read_asset("rrsi", "proposer.md", context=defaults),
        "analyst": _read_asset("rrsi", "analyst.md", context=defaults),
        "critic": _read_asset("rrsi", "critic.md", context=defaults),
    }


def example_round() -> dict:
    """A valid sample round used by the UI and docs."""
    return {
        "round": 0,
        "incumbent": {"score": 0.62, "cost": 120000},
        "s_star": 0.662,
        "accepted_counts": {"skill": 1},
        "config": {
            "delta": 0.017, "beta0": 0.0, "beta1": 0.5,
            "w_s": 1.0, "w_c": 0.25, "w_n": 0.1, "b_min": 1, "b_max": 3,
            "T": 10, "stall_window": 3, "recent_window": 3,
        },
        "candidates": [
            {
                "variant": "candidate-a",
                "commit": "abc1234",
                "score": 0.668,
                "cost": 122000,
                "edits": [{
                    "component": "prompt",
                    "hypothesis": "Inject the current ARK unit into the prompt instead of restating it.",
                    "diff": "+ prompt = f\"Unit: {unit['uid']}\\n{unit['body']}\"",
                }],
            },
            {
                "variant": "candidate-b",
                "score": 0.640,
                "cost": 110000,
                "edits": [{
                    "component": "skill",
                    "hypothesis": "Retrieve the design protocol on demand instead of keeping it resident.",
                    "diff": "+ from arkstudio.skills import get_skill\n+ skill = get_skill('design')",
                }],
            },
        ],
    }


def _read_asset(*parts: str, context: Mapping[str, Any] | None = None) -> str:
    context = context or {}
    path = Path(__file__).resolve().parent / "assets" / Path(*parts)
    try:
        template = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise RrsiError(f"missing skill asset: {Path(*parts).as_posix()}") from exc
    from . import protocol
    return protocol.substitute(template, context)


def to_jsonl(records: Iterable[Mapping[str, Any]]) -> str:
    """Serialise records for a durable ``edit_history.jsonl`` evidence file."""
    return "".join(json.dumps(dict(record), ensure_ascii=False) + "\n" for record in records)
