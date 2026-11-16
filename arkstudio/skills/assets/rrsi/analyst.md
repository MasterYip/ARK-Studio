# Analyst Brief · Round $round_index

Turn the round's trials and traces into component-level evidence for
**$task_title**. Do not propose edits; explain what happened.

## Round facts

- Incumbent `S_t = $incumbent_score`, `C_t = $incumbent_cost`.
- Best `S* = $s_star`, noise band `delta = $delta`.
- Accepted-edit counts: `$accepted_counts`.
- Components to prune: $prune_set.
- Evidence: $round_evidence.

## Required output

1. **Outcome:** measured score and cost, with trial counts and any missing or
   crashed trials (counted as failures, not silently dropped).
2. **Attribution:** which candidate edits plausibly caused the change, tagged by
   component. Distinguish mechanism from measurement noise.
3. **Regressions:** which tasks or behaviours got worse, and whether the cause is
   shared with the intended gain.
4. **Overfitting check:** does the gain depend on a task-specific trigger? Quote
   the evidence.
5. **Next evidence:** the cheapest experiment that would settle the remaining
   ambiguity.

Keep failures and exclusions in the record. A negative result is evidence; a
discarded run is not.
