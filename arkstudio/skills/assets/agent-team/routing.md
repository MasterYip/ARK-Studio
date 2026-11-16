# Routing: choose the execution shape

Read this before you spawn anything. The most expensive mistake in ARK is not a
weak model; it is a team applied to a task that needed one agent, or one agent
applied to a task that needed independent evidence.

## 1. Classify the task

| Question | If yes | If no |
|---|---|---|
| Does each step need the previous artefact? | `sequential` | continue |
| Are there several independent directions to explore? | `breadth` | continue |
| Is the dependency graph sparse but the horizon long? | `long-horizon` | continue |
| Must a claim survive an active attempt to refute it? | `adversarial` | `sequential` |

## 2. Map structure to topology

- `sequential → solo` (default) or `pipeline` when a distinct stage has a
  different tool or model.
- `breadth → router-workers`; a `pipeline` is enough when the directions are
  really stages.
- `long-horizon → router-workers`; the win comes from durable checkpoints and a
  shared task list, not from raw agent count.
- `adversarial → peer-team`; only here is direct peer challenge worth the
  coordination cost.

## 3. Size the team

| Size | When |
|---|---|
| 1 | default; sequential work, one writer, one context |
| 2–3 | a pipeline stage or two independent evidence routes |
| 4–5 | breadth-first exploration with a router and an independent reviewer |
| > 5 | almost never; requires `heterogeneous: true` and a written rationale |

Hard rules:

- `size > 1` needs a rationale. "It is a big task" is not one.
- `size > 5` requires heterogeneity. Homogeneous agents converge; the
  Ringelmann ceiling is real.
- `peer-team` requires heterogeneity and a reviewer/critic/verifier role.
- No nested teams. A worker may run a bounded read-only errand; it may not
  spawn another team.

## 4. Allocate roles without inventing a bureaucracy

Minimum viable roles:

- **router / owner** — holds the plan and the task list.
- **worker** — executes one bounded unit, reports evidence.
- **verifier / reviewer** — independent; reproduces the mandatory gates.

Add a role only when it owns a distinct tool or a distinct failure mode. A role
whose only difference is a paragraph of persona is a skill, not an agent.

## 5. Pick a model policy

| Routing | Use when | Risk |
|---|---|---|
| `fixed` | one model is good enough | overpaying on easy steps |
| `tiered` | clear plan/execute split | planner and worker drift |
| `auction` | heterogeneous workers with measurable cost/value | coordination overhead |

The verifier must not inherit the planner's context. Same family is fine;
same conversation is not.

## 6. Anti-patterns

- **Team by default.** Five agents on a sequential refactor: more tokens,
  more conflicts, no speed-up.
- **Debate without heterogeneity.** Five samples of the same model agreeing
  with each other is self-consistency with extra steps.
- **Router that also implements.** The router loses the global view and the
  task list goes stale.
- **Nested teams.** Coordination cost grows faster than the work.
- **Unbounded fan-out.** "Spawn as many as you need" is how a $5 task becomes
  a $500 task.
- **A verifier chosen by the owner.** Independence is structural, not a promise.

## 7. Worked examples

**Sequential, tightly coupled** — migrate a module across 12 files:
`solo`, one worktree, gates `scope → static → unit`.

**Breadth, independent** — survey 8 candidate methods for a research plan:
`router-workers`, size 3, one shared task list, each worker returns a table +
one figure; router synthesises.

**Long-horizon, sparse** — reproduce a paper's main experiment across three
datasets: `router-workers`, size 3, durable checkpoints, context budget 120k,
artefacts in `assets/`, independent verifier.

**Adversarial** — decide whether a claimed counterexample is valid:
`peer-team`, size 3, heterogeneous (two model families + one human reviewer),
one critic role.
