# Context: inline, spill, reference

Context is the budget that actually runs out. A long-horizon task rarely fails
because the model is too weak; it fails because the hot set got buried, the
history got replayed, or an artefact was pasted ten times instead of linked.

## The three tiers

| Tier | What goes there | Where it lives |
|---|---|---|
| **Inline** | task manifest, current unit, owned paths, last verified diff | the prompt |
| **Spill** | full logs, datasets, transcripts, prior reports, raw traces | files / external storage |
| **Reference** | figures, tables, media, reports | project-relative path + unit link |

Default budget: **120,000 tokens**. Above **500,000** is a warning: split the
task or spill more.

## Rules

1. **Inline the decision, not the evidence.** Put the constraint and the current
   state in the prompt; put the 40 MB log on disk and cite it.
2. **Reference artefacts by path.** `assets/validation-summary.svg`, not a
   base64 blob in the unit body.
3. **One source of truth per fact.** If it lives in a unit, link the unit; do
   not restate it in the manifest.
4. **Compact deliberately.** When approaching the budget, write a state summary:
   verified facts, open questions, next action, evidence pointers. Restart from
   that summary rather than replaying raw history.
5. **Spill before you shrink.** Dropping detail silently is worse than writing
   it to a file and citing it.
6. **Never inline secrets.** Tokens, credentials and personal data stay out of
   prompts, units, artefacts and logs.

## The context block

```json
{
  "budget_tokens": 120000,
  "inline": ["task manifest", "current unit", "owned paths"],
  "spill": ["full logs", "datasets", "prior reports"],
  "artifacts": ["assets/", "reports/"]
}
```

- `budget_tokens` — 8k to 2M; the validator rejects anything outside.
- `inline` — the hot set the agent must see at turn zero.
- `spill` — what the agent may read on demand; must be non-empty.
- `artifacts` — project-relative paths only; absolute paths and `..` are
  rejected.

## Hand-off without the telephone game

When a worker finishes, it should not paste its whole transcript back to the
router. It should:

1. write the artefact to `assets/` or `reports/`;
2. attach it to the task unit (or reference it in the hand-off block);
3. return a short summary: what changed, what was verified, what remains risky.

The router reads the summary and the artefact path, not the transcript. This is
the same reason production multi-agent systems push subagent output to a
filesystem instead of piping it through the coordinator.

## Failure modes

| Symptom | Likely cause | Fix |
|---|---|---|
| Agent forgets the constraint | the constraint was in a spilled file | inline it |
| Agent re-does finished work | no verified-state summary | write one before continuing |
| Token bill explodes | artefacts pasted repeatedly | reference paths |
| Agent "remembers" a wrong fact | stale summary | re-read the source unit |
| Context overflows mid-task | no compaction point | checkpoint + summary at 70% |
