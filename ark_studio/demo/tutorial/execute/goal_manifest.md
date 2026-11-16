# Goal Manifest · T-0001

| Field | Value |
|---|---|
| Task ID | `T-0001` |
| Title | Run the 3-seed warm-up comparison |
| ARK project | ARK Studio Demo (`ark-demo`) |
| ARK unit | `T-0001` |
| Interface | v2.0 |
| Phase | `ready` |
| Status | `waiting` |
| Owner | agent:codex |
| Reviewer | human:you |
| Repository | `warmup-study` |
| Worktree | `.worktrees/T-0001` |
| Base revision | `main@4f2c1aa` |
| Priority | `p1` |
| Budget | one GPU (20 GB claim), 2 GPU-hours, no new dependency |
| Dependencies | _None declared._ |

## Objective

Train both arms for 3 seeds and report final loss and spikes per run.

## Background / hypothesis

Early loss spikes appear in the 60M model without warm-up (I-0001).

## Execution shape

- **Task structure**: `sequential`
- **Team**: - **mode**: solo
- **size**: 1
- **heterogeneous**: no
- **rationale**: one route, one GPU; a team would only add coordination cost
- **roles**: worker

## Context plan

- **budget tokens**: 120000
- **inline**: task manifest, current unit, owned paths
- **spill**: full logs, datasets, prior reports
- **artifacts**: assets/

## Model policy

- **routing**: tiered
- **planner**: inherit
- **worker**: inherit
- **verifier**: independent

## Deliverables

- results.csv with per-step mean loss
- results-chart.svg
- inspection report with commands and checksums

## Non-goals

- tuning other hyper-parameters
- changing the model code

## Owned paths and permissions

- configs/warmup/**
- reports/warmup/**

## Acceptance

1. `python train.py --config configs/warmup/smoke.yaml --steps 50` · threshold: exits 0, loss is finite · evidence: smoke log
2. `python analyse.py reports/warmup/ --out results.csv` · threshold: 3 seeds per arm present · evidence: results.csv

## Gates (cheapest first)

- **Scope** (`scope`) — declared files, ownership, clean diff, no secrets or bulk artifacts
- **Static** (`static`) — parse, format, lint, syntax, types
- **Unit** (`unit`) — focused deterministic tests

## Verification

- **independent**: yes
- **method**: command
- **evidence**: test log, diff
- **reviewer reproduces**: unit

## Evidence retention

**Retained in Git**
- goal manifest
- test log
- source mapping

**Kept in durable external storage with inventory + checksums**
- bulk logs and raw traces
- datasets and checkpoints
- media collections and caches
- PID files and transfer bundles
- credentials and secret values

## Failure taxonomy

- spec
- tool
- context
- model
- environment
- review
- integration

## Handoff

- **contract**: Reviewer receives results.csv, the chart, logs and residual risks.
- **inputs**: main@4f2c1aa
- **outputs**: results.csv, results-chart.svg

## Risks

- the shared GPU is busy and the job waits in the queue

## Stop and escalation conditions

- loss becomes NaN
- the GPU claim cannot be met for 2 hours

## Decision log

| Timestamp | Decision | Owner |
|---|---|---|
| 2026-11-02T09:00:00+00:00 | Manifest created from the ARK agent-team contract | agent:codex |

---

_Reviewer independence is required: the owner reports completion, the named
reviewer records acceptance._
