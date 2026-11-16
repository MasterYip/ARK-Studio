# Agent Brief · $task_id

Use this brief when a router hands one bounded unit of work to a worker. It is
lighter than a goal manifest and deliberately explicit about shape, budget and
hand-off, because most multi-agent failures start with an underspecified brief.

| Field | Value |
|---|---|
| Task ID | `$task_id` |
| Parent task | $parent_task |
| Owner | $owner |
| Reviewer | $reviewer |
| Task structure | `$task_structure` |
| Topology | `$team` |

## Objective

$objective

## Owned paths

$owned_paths

## Context plan

$context_plan

## Acceptance

$acceptance

## Verification

$verification

## Handoff

$handoff

## Stop conditions

$stop_conditions

## Failure taxonomy

$failure_taxonomy

---

_The worker reports evidence, not completion. Only the named reviewer accepts.
If the brief is ambiguous, stop and escalate; do not widen scope silently._
