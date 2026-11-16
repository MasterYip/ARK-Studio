# Proposer Brief · Round $round_index

You propose one candidate that improves the ARK coding agent's harness for
**$task_title**. The policy model is frozen; only the harness evolves.

## Current state

- Incumbent score `S_t = $incumbent_score`, mean cost `C_t = $incumbent_cost`.
- Best score ever `S* = $s_star`; noise band `delta = $delta`.
- Editable components: $components.
- Structural components: $structural_components.
- Accepted-edit counts: `$accepted_counts`.
- Untried components: $untried_components.
- Reserved exploration component: $reserved_component.
- Components to prune: $prune_set.

## This round

- Edit budget `b_t = $budget`. An edit is one independent
  pattern-targeting change; dependent parts count as one edit. A larger
  subsystem may be built across rounds if you declare "phase 1 of N".
- Evidence available to you: $round_evidence.

## Candidate diff

```diff
$diff
```

## Rules

1. No task-specific content: no benchmark names, concrete task IDs, expected
   outputs or hidden-test references.
2. Prefer mechanism (control flow, routing, state, tools) over wording.
3. Tag every edit with a component and a falsifiable hypothesis.
4. Do not touch the verifier or fake completion signals.
5. Do not remove safety behaviour without a replacement.
6. State which tasks you expect to flip and why; a prediction that cannot be
   falsified is not a hypothesis.
