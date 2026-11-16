# Verification: evidence, independence, failure classes

The owner reports; the reviewer accepts. Everything here exists to make that
sentence true in practice.

## 1. Gates, cheapest first

`scope → static → unit → component → integration → experiment → publication`

| Gate | Checks | Minimum evidence |
|---|---|---|
| `scope` | changed files inside `owned_paths`, clean diff, no secrets | file list + diff |
| `static` | parse, lint, types | command + result |
| `unit` | focused deterministic tests | command + result + count |
| `component` | bounded smoke, imports | command + result + artefact |
| `integration` | controlled checkout/environment | command + revision |
| `experiment` | config, data, seed, metrics | config + data + seed + metrics + marker |
| `publication` | claim traceability or authorized checks | traceability map |

A skipped gate is recorded as skipped, never silently omitted.

## 2. Independence

- The reviewer must differ from the owner unless `acceptance_exception` is
  recorded on the manifest.
- The reviewer is an agent by default. Its acceptance closes the ARK unit
  (`done`). Escalate to the human user (ARK `review` status) only for the
  cases in SKILL.md, *Status and human attention*.
- The verifier must not inherit the planner's conversation. Same model family
  is acceptable; same context is not.
- A `peer-team` must contain a critic/reviewer role and heterogeneity; a
  homogeneous panel is self-consistency with extra steps.

## 3. Methods

| `verification.method` | Use when | Record |
|---|---|---|
| `command` | a deterministic check exists | exact command + threshold + output |
| `rubric` | output is free-form (reports, designs) | rubric + judge + score |
| `human` | judgement or domain expertise needed | who + what they inspected |
| `mixed` | deterministic first, judgement second | both records |

## 4. Evidence format

Every acceptance item should answer five questions:

1. **What** was run (exact command).
2. **Where** (revision, worktree, environment).
3. **With what** (data, seed, config).
4. **Expected** (threshold, baseline).
5. **Observed** (result, artefact path).

An inspection report without these is an opinion.

## 5. Failure taxonomy

| Class | Meaning | Default response |
|---|---|---|
| `spec` | brief/acceptance ambiguous | rewrite the brief; do not retry |
| `tool` | missing/misdescribed tool, bad output | fix tool, retry once |
| `context` | information missing, stale or compacted away | re-read the source |
| `model` | cannot produce a correct step | raise effort or switch model |
| `environment` | runtime, dependency, permission, resource | fix environment, retry |
| `review` | reviewer rejected evidence/reasoning | revise, same task ID |
| `integration` | conflict with concurrent/upstream work | rebase, same task ID |

## 6. Retry policy

| Failure | Retry in place? | Why |
|---|---|---|
| `tool` | yes, once | the fix is local |
| `environment` | yes, once | the fix is local |
| `spec` | **no** | the same brief produces the same miss |
| `context` | **no** | the polluted context keeps biasing the run |
| `model` | maybe, with higher effort or a different model | capability, not information |
| `review` | yes, after addressing the finding (and the user's `sent_back` comment, if any) | same task, new evidence |
| `integration` | yes, after rebase | same task, new base |

For `spec` and `context`, restart from a clean brief and a fresh context. For
everything else, preserve the artefacts and add to the same task record.

## 7. What the reviewer writes

- Decision: `accepted` / `returned for revision`.
- Gates reproduced (not "all gates" — the actual list).
- Evidence observed (paths, hashes, commands).
- Residual risks and rollback guidance.
- Failure classes encountered and how they were resolved.

## 8. Anti-patterns

- **Self-acceptance by default.** An owner who accepts their own work has not
  verified anything.
- **"Tests pass"** with no command, revision or count.
- **Reviewer reads the summary, not the diff.** The summary is the owner's
  claim; the diff is the evidence.
- **Retrying a spec failure unchanged.** More attempts do not fix ambiguity.
- **Green gate, wrong artefact.** Verify the file that will actually ship.
