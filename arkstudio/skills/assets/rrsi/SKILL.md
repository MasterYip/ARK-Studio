# RRSI Harness Search · ARK adaptation

Adapted from `rrsi` (Apache-2.0, google-research/rrsi @
`be50316e1db05914068a973f322770ef08ed7ba1`). ARK exposes the reusable search
regularizers, not the benchmark harnesses or model clients.

## Why it belongs in ARK

An agent improving its own prompts, skills, tools, memory or subagents is
changing a *harness*. Optimising against the tasks currently in view overfits:
gains disappear on the next task. RRSI regularizes the search instead of
restricting what the harness may contain.

The contract here is deliberately small and usable without a benchmark runner:

- **Open edit space.** `prompt`, `control_flow`, `config`, `output_plumbing`,
  `context_mgmt`, `client_tool`, `skill`, `memory`, `subagent`.
- **Structural set.** `client_tool`, `skill`, `memory`, `subagent` add
  machinery rather than text; they earn a novelty tie-break.
- **Tag evidence.** A declared component is kept only when the diff supports it;
  otherwise the component is recovered from the diff (`normalize_component`).
- **Annealed edit budget.** `b_t = ceil(b_min + (b_max-b_min)/2 *
  (1 + cos(pi t / T)))`; early rounds may bundle coordinated edits, late rounds
  are sparse and attributable.
- **Cost rule.** If `S' - S_t > delta`, the relative token-cost increase must
  stay within `beta0 + beta1 * (S' - S_t)`. Inside the noise band, keep the
  candidate only when `w_s*dS - w_c*dC + w_n*nu > 0`.
- **Noise-adjusted floor.** `S' >= S* - delta`; a candidate may never walk
  downhill through regressions small enough to look like noise.
- **History, exploration, pruning.** Log every measured edit. A stalled run
  reserves a slot for an untried structural component; components exercised
  recently without a strictly improving edit are candidates for removal.
- **Leakage screen.** Reject benchmark task names, hidden-test references,
  expected outputs and concrete ARK task IDs before spending an evaluation.

## ARK commands

```text
GET  /api/skills/rrsi
POST /api/skills/rrsi/render      {template: proposer|analyst|critic, context}
POST /api/skills/rrsi/validate    {kind: round|history|edit|leakage, ...}
POST /api/skills/rrsi/round       {incumbent, s_star, candidates, ...}
POST /api/skills/rrsi/history     {records, config}
```

Python:

```python
from arkstudio.skills import rrsi

budget = rrsi.edit_budget(t=0, T=10, b_min=1, b_max=3)   # 3
result = rrsi.validate_round({...})
history = rrsi.History()
history.append_jsonl("evidence/edit_history.jsonl", rrsi.EditRecord(...))
```

## Domain descriptor

A domain names the evolvable harness path, the `evolve`, `heldout` and `smoke`
task splits, the metric and non-compensatory guards. It is the
dependency-light part of the vendor's `domains/<name>/adapter.py`; benchmark
runners stay outside ARK.

```json
{
  "name": "ark-coding",
  "harness_path": "arkstudio/skills",
  "evolve": ["T-0001", "T-0002"],
  "heldout": ["T-0004"],
  "smoke": ["T-0001"],
  "metric": "accepted task rate",
  "guards": ["unit tests must pass"]
}
```

Render it with `POST /api/skills/rrsi/render` (`template: "domain.json"`) and
validate it with `POST /api/skills/rrsi/validate` (`kind: "domain"`).

## Evidence

Keep the edit history as JSONL in Git: round, component, hypothesis, verdict,
delta score, delta cost, candidate variant, commit and evidence link. Raw model
traces, trial corpora and candidate worktrees stay outside Git with a durable
path and checksum. A candidate that survived the critic but failed evaluation is
still recorded.

## Hard rules

1. No task-specific content: no benchmark names, task IDs, expected outputs or
   file names that only make sense for one task.
2. Never touch or detect the verifier.
3. Prefer mechanism over wording; prompt edits must implement a mechanism.
4. Do not break the harness API contract.
5. Do not remove safety behaviour without a replacement.
6. One independent pattern-targeting change per edit; dependent parts are one
   edit.
