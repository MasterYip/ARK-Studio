# Critic Brief · Round $round_index

Frozen task: **$task_title**.

Screen the candidate **before** evaluation is spent. Reject any candidate whose
value depends on knowing the evaluation set.

## Candidate

- Components: $components.
- Evidence: $round_evidence.
- Diff:

```diff
$diff
```

## Automatic leakage patterns

- References to `tests/` or concrete `test_*.py` files.
- `expected_output`, `expected_result`, `expected_answer`.
- Assertions or verifier output used as a runtime signal.
- Benchmark/task names, concrete ARK task IDs (`T-\d{3,}`), or numeric answers.
- Special-casing an input that only appears in the current task set.

## Review questions

1. Would this help a competent human operator working on many unfamiliar tasks?
2. Is every changed mechanism justified by a general cause, not an answer?
3. Does the candidate alter the harness API, completion signal or verifier?
4. Does it bundle more independent edits than `b_t = $budget`?
5. Is the hypothesis falsifiable, and is the predicted flip stated?

## Verdict

Return `reject` with the exact offending lines, or `admit` with any residual
risk. A critic rejection is recorded in the edit history with verdict
`invalid`; the candidate is not evaluated.
