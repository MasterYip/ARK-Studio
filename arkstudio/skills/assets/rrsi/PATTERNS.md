# RRSI Patterns for ARK Harnesses

A pattern is a reusable mechanism, not a task-specific patch. Use these as
starting points, then name the component, the hypothesis and the expected
flip in the edit record.

## Prompt

- Inject computed project context (current unit, task manifest, acceptance
  checks) instead of restating facts the model must remember.
- Make the output contract explicit: format, required fields, refusal path.
- Do not add motivational wording; if the edit is only prose, it must still
  implement a mechanism.

## Control flow

- Turn a long linear loop into plan → act → verify with a bounded revision
  path.
- Add a cheap pre-check before an expensive tool call; skip the call when the
  pre-check already settles the question.
- Cap retries and route exhausted retries to an explicit failure state.

## Config

- Make an implicit constant explicit and validate it at startup.
- Default to the conservative behaviour when a value is missing.

## Output plumbing

- Normalize tool output into a stable schema before the model sees it.
- Preserve the raw artifact separately from the summary the model reads.

## Context management

- Retrieve a skill/protocol on demand instead of keeping it resident.
- Summarize a completed phase once and keep the summary, not the transcript.

## Client tools

- Add one tool for a repeated need; register a schema the model can satisfy.
- Return structured errors the model can act on.

## Skill and subagent

- Move a repeated procedure into a small, testable skill file.
- Give a subagent one bounded objective and require a structured report.

## Memory

- Store durable project facts, not raw transcripts.
- Include a write policy so stale or conflicting facts are detectable.

## Anti-patterns

- Benchmark names, concrete task IDs, expected outputs or hidden-test paths.
- A prompt rewrite that changes tone but not information routing.
- Bundling independent fixes into one candidate beyond the current budget.
- Removing truncation, summarization or parse recovery without a replacement.
