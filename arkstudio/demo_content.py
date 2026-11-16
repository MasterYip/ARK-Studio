"""Content of the built-in ARK Studio tutorial ("ARK Studio Demo").

This module is data only: ``CHAPTERS`` (one hub per workspace), ``EXAMPLES``
(the real ARK units each chapter teaches with) and ``LINKS``.  ``demo.py``
turns it into a project.  Keep the prose short and concrete:

- one paragraph "what this is for", 3-6 numbered "Try it" steps naming the
  exact UI labels in **bold**, then a short tips / common-mistakes list;
- every example is a real unit with a sensible status, linked to its hub.

Placeholders, resolved by ``demo.py`` after every unit exists:

- ``{{uid:key}}``  - the UID of the unit with that key (for example ``T-0001``);
- ``{{raw:path}}`` - a same-origin URL for ``assets/tutorial/<path>`` that the
  Markdown renderer can embed (``/api/workspace/raw?path=...``);
- ``{{html:path}}`` - the text of a tutorial asset (used for the ``html`` block).

Media paths are relative to ``demo-assets/tutorial`` and are copied to the
project's ``assets/tutorial/``.  The figures are built by
``scripts/dev/build_demo_assets.py``.
"""
from __future__ import annotations

# Bump when the tutorial content or its assets change.  demo.py rebuilds the
# cached template, and per-user copies refresh on their next open.
DEMO_VERSION = 3

# ---------------------------------------------------------------- chapters
# (key, seq, title, body, media)   media = [(path, kind, caption), ...]
CHAPTERS: list[dict] = []
EXAMPLES: list[dict] = []
# (from_key, to_key, relation, note)
LINKS: list[tuple[str, str, str, str]] = []
# topic key -> {"members": [(key, caption)], "design": extra topic settings}
TOPICS: dict[str, dict] = {}


def chapter(key: str, seq: int, title: str, body: str, *, media=(), importance="p1",
            status="active") -> None:
    CHAPTERS.append({"key": key, "seq": seq, "title": title, "body": body,
                     "media": list(media), "importance": importance, "status": status})


def example(chapter_key: str, key: str, title: str, kind: str, body: str, *,
            status: str = "open", importance: str = "p2", tags=(), media=(),
            start_at: str | None = None, end_at: str | None = None) -> None:
    EXAMPLES.append({"chapter": chapter_key, "key": key, "title": title, "type": kind,
                     "body": body, "status": status, "importance": importance,
                     "tags": list(tags), "media": list(media), "start_at": start_at,
                     "end_at": end_at})


def link(a: str, b: str, relation: str, note: str = "") -> None:
    LINKS.append((a, b, relation, note))


# =================================================================== START
chapter("start", 0, "Start here: ARK Studio in ten minutes", """\
Start here. ARK Studio keeps a whole research project in one graph; this tutorial walks through it in eight short chapters.

## What ARK is for

ARK Studio keeps one research project in one place. Ideas, questions, claims,
evidence, experiments, tasks, agent runs, figures and posts are all **units**
in one graph. You decide what matters. Coding agents do the work. ARK keeps
the record, so nothing lives only in a chat window.

![The ARK loop: Home, Research, Map, Execute, Output, with the Editor and Assistant alongside]({{raw:start/ark-loop.svg}})

```mermaid
flowchart LR
  H["Home<br/>setup + prompts"] --> R["Research<br/>idea and plan"]
  R --> M["Map<br/>the argument"]
  M --> X["Execute<br/>tasks and runs"]
  X --> O["Output<br/>figures and posts"]
  O -. "results feed the next idea" .-> R
  E["Editor<br/>any file or node"] --- X
  A["Assistant<br/>Ctrl+J"] --- M
```

## How to use this tutorial

There is one chapter per workspace. Each chapter is a node like this one.
Open it, follow the numbered **Try it** steps, then open its example nodes.

1. **Home** ({{uid:home}}): set up the environment, copy an agent prompt.
2. **Research** ({{uid:research}}): turn an idea into a question, claims and an experiment.
3. **Map** ({{uid:map}}): see the whole argument and find the gaps.
4. **Execute** ({{uid:execute}}): Board, Runs, GPU and Files.
5. **Output** ({{uid:output}}): figures, a colour board, a poster and planned posts.
6. **Editor** ({{uid:editor}}): open images, video, PDF, CSV and code in tabs.
7. **Agents & skills** ({{uid:agents}}): what the agent does, what you decide.
8. **Settings & mobile** ({{uid:settings}}): scopes, providers, GPU, your phone.

All chapters use one small, made-up study: *does a learning-rate warm-up
remove early loss spikes?* You will meet the same idea, claims, tasks and
figures in every workspace.

## Try it

1. Click **Research** in the top bar, then **Story**. The chapters are listed
   in order. Click one to open it in the inspector on the right.
2. Click **Map**. Every chapter and example is a node. Click a node to read it.
3. Press **Ctrl+J** to open the **Assistant**. Ask *What should I do next?*
4. Edit anything. This is a safe copy (see below).

## Good to know

- **This demo is disposable.** Your edits are real while you are here, but
  switching to another project and back gives you a fresh copy.
- Coding agents are switched off in the demo, so nobody's provider key is
  used. The Assistant still works.
- Colours on the Map mean unit types. The legend is in the **Map** chapter.

![ARK architecture: browsers and agents talk to one Python server that owns one folder per project]({{raw:start/architecture.svg}})
""", media=[
    ("start/ark-loop.svg", "figure", "The ARK loop: six workspaces and three human checkpoints"),
    ("start/architecture.svg", "figure", "Architecture at a glance: one server, one folder per project"),
], importance="p0")

# ==================================================================== HOME
chapter("home", 1, "Home: set up once, then start with an agent", """\
Chapter 1 of 8. Home is where every session starts: check that setup is ready, then copy a prompt for your coding agent.

## What Home is for

Home is where every session starts. It shows whether this machine is set up
for agents, and it gives you two copy-ready prompts for your coding agent
(DSH, Codex or Claude Code). You paste a prompt into the agent; the agent
reads ARK's documentation through the agent API, does the work, records it
in ARK as it goes, and asks you before anything risky.

![Home, annotated: setup status, two starter prompts, Copy prompt and Review & confirm]({{raw:home/home-annotated.svg}})

| Prompt | When | What the agent does |
|---|---|---|
| **1. Set up this environment** | Once per machine or new repository | Asks about your code, servers and agents, proposes a setup, waits for you to confirm, then tells you what you can ask next |
| **2. Start / continue research team** | At the start of every new agent session | Loads the project context from ARK, summarises where things stand, offers what to do next and coordinates the work you choose |

Both prompts carry **standing rules**: before working the agent reads ARK;
after every meaningful step it records tasks, progress comments, evidence
and decisions in ARK without being reminded, and ends each work session
with a short summary comment.

## Try it

1. Click **Home** in the top bar. Find **Start with an agent** and read the
   badge at the top right, for example **Setup: not started**.
2. On **Set up this environment**, click **Preview**. Read what the agent will do.
3. Click **Copy prompt** and paste it into your coding agent.
4. Answer the agent's questions. Say *skip* for anything you don't need.
5. When the badge says **Setup: waiting for you**, click **Review & confirm**.
   One dialog walks through two pages: **1/2 Project setup** and
   **2/2 My resources**. Check each summary, click **Confirm setup** on each
   page (or **Next** / **Back**), then read the result. Only a person can do this.
6. In a new agent session, copy **Start / continue research team** and pick
   one of the options it offers, for example *"Plan this idea: ..."*.

## The setup states

```mermaid
stateDiagram-v2
  [*] --> unset
  unset --> draft: agent runs discover
  draft --> proposed: agent proposes the profile
  proposed --> confirmed: YOU click Confirm setup
  confirmed --> verified: agent runs verify
  verified --> stale: repos or servers change
  stale --> proposed: agent proposes again
```

| Badge | What it means | What you do |
|---|---|---|
| Setup: not started | No profile yet | Copy prompt 1 |
| Setup: draft | The agent is still asking | Answer or say *skip* |
| Setup: waiting for you | A profile is proposed | **Review & confirm** |
| Setup: confirmed | You approved it | Let the agent verify |
| Setup: ready | Checks passed | Use prompt 2 in each new session |
| Setup: needs refresh | Something changed | Run prompt 1 again |

## Tips and common mistakes

- **Run prompt 1 first** on a new machine or repository. Prompt 2 checks
  that setup is confirmed and sends you back to prompt 1 if it is not.
- Prompts contain no local paths or secrets. The agent asks you for them.
- Never paste the agent token into a unit, a file or a report.
- *"Setup: waiting for you"* does not go away on its own. It needs your click.
- Only a project admin can confirm page 1 (the project layer); other members
  see it read-only and confirm their own page 2.
""", media=[("home/home-annotated.svg", "figure", "Home, annotated: setup badge, two starter prompts and Review & confirm")])

example("home", "prompt_setup", "Starter prompt 1: Set up this environment", "resource", """\
This is the first prompt on **Home → Start with an agent**. Copy it from
there (it fills in your ARK address, project and token); this shortened copy
is for reading.

```text
Documentation: first GET /api/agent/v1/docs (same Authorization and
X-ARK-Project headers) and read the docs you need when you need them.

STANDING RULES: they hold for this whole session, without me reminding you.
R1. Before starting work, GET /api/agent/v1/context and read the units it concerns.
R2. After every meaningful step, write it to ARK straight away, without being asked.
R3-R6. Task units with current status, progress comments, evidence uploads,
       decisions and links to the plan.
R7. At the end of each work session, post a short summary comment.
R8. If ARK is unreachable, tell me and keep a local note to sync later.
R9. Secrets only as references (env:NAME); ask before risky actions.

FIRST: set up this ARK project with ARK's setup skill.
1. GET /api/agent/v1/setup: read resources, policies and readiness.
2. POST /api/agent/v1/setup/discover with the paths I gave, then ask only
   about missing details (scope, code, team, compute, commands, authority).
   I may answer, say "don't know" or say "skip".
3. POST /api/agent/v1/setup/draft with {resources: [...], propose: true}.
4. Show me the summaries and digests, then wait while I confirm them in ARK
   (Home -> Review & confirm: project layer, then my layer). Only a human
   confirms: never confirm yourself.
5. After I confirm, POST /api/agent/v1/setup/verify and tell me what failed.

NEXT: explain how to use ARK for this project and offer me options with
example requests ("Plan this idea: ...", "Start a research team on ...",
"Give me a status check", "Record what we just did", "Import my existing
task folder ...").
```

**Human step:** step 4 stops and waits for you. The agent gets a 403 if it
tries to confirm.
""", status="active", importance="p1", tags=["tutorial:prompt", "skill:setup"])

example("home", "prompts_more", "Starter prompt 2: Start / continue research team", "resource", """\
The second prompt on **Home**. Paste it into every **new** agent session once
setup is **ready**. It starts with the same documentation pointer and
standing rules as prompt 1, then:

```text
NOW: this is a new session on an existing ARK project. Load the current
research context before doing anything else:
1. GET /api/agent/v1/context: objective, open and blocked tasks, recent
   activity and decisions.
2. GET /api/agent/v1/setup: the confirmed setup profile. If setup is not
   confirmed, tell me what is missing; do not start task work.
3. Read the active, open and blocked tasks and their latest comments.
4. GET /api/agent/v1/skills/agent-team.
5. Summarise in under 15 lines where the project stands. Change nothing yet.

THEN: tell me what I can do now, each with an example request, and wait:
- "Start a research team on <objective>"
- "Continue <task ID>"
- "Plan this idea: <idea, paper or link>"
- "Give me a status check"
- "Review the results of <task ID>"
On my choice, act as the coordinator and follow the agent-team skill.
```

See {{uid:research}} for what *"Plan this idea"* produces and
{{uid:execute}} for how a research team runs tasks.
""", status="active", importance="p2", tags=["tutorial:prompt"])

example("home", "setup_decision", "Confirm the environment setup for warmup-study", "decision", """\
**Human-in-the-loop example.** An agent ran prompt 1 and proposed this
profile. It now waits for a person on **Home → Review & confirm** (page 1/2,
the project layer).

| Area | Proposed by the agent |
|---|---|
| Scope | Warm-up and loss-spike study on 60M-350M models |
| Code | `warmup-study` (primary, base `main`), `paper` (read-only) |
| Agents | Codex (worker), you (reviewer) |
| Compute | this machine; GPU server `lab-a100` (name only) |
| Commands | test `pytest -q`, smoke `python train.py --steps 50` |
| Authority | commit on task branches: yes. Push, merge, GPU jobs, deletion, posts: **ask first** |
| Secrets | `env:WANDB_API_KEY` (a reference, not the value) |

**What you check before you click Confirm setup**

- The repositories and base branch are right.
- The *ask first* list includes everything that costs money or is public.
- No secret values appear anywhere, only `env:NAME` references.

Status is **review** because a person has not confirmed it yet.
""", status="review", importance="p1", tags=["tutorial:human-step", "skill:setup"])

# ================================================================ RESEARCH
chapter("research", 2, "Research: from an idea to a plan you can test", """\
Chapter 2 of 8. Research turns a loose idea into a question, claims and an experiment you can actually run.

## What Research is for

Research is where an idea becomes something you can test. **Idea** is the
inbox for raw ideas and sources. **Plan** has four lenses on the same
project: **Story** (the argument), **Related** (prior work that matters),
**Method** (how you will do it) and **Experiment** (what you will run, and
what result would prove you wrong).

![The data model: one idea, a question, two claims, evidence, an experiment and a task, joined by typed links]({{raw:research/data-model.svg}})

## The example chain in this demo

```mermaid
flowchart LR
  I["Idea<br/>warm-up may stabilise training"] --> Q["Question<br/>does warm-up remove spikes?"]
  Q --> C1["Claim<br/>removes early spikes"]
  Q --> C2["Claim<br/>keeps final loss"]
  X1["Experiment<br/>3 seeds, 5k steps"] --> E1["Evidence<br/>0 vs 3 spikes per run"]
  E1 -- supports --> C1
  X2["Experiment<br/>LR sweep"] -- validates --> C2
```

Open the nodes in order: {{uid:idea}} → {{uid:question}} → {{uid:claim_spikes}}
and {{uid:claim_final}} → {{uid:evidence}} → {{uid:exp_main}} and {{uid:exp_sweep}}.

## Try it

1. Click **Research**, then **Idea**. Find {{uid:idea}}. Click **Capture idea**
   to add your own.
2. Click **Story**. The spine lists this tutorial's chapters in order: a
   story is just ordered nodes that carry an argument.
3. Click **Related**. Open {{uid:related}}; its BibTeX is attached.
4. Click **Method**, then **Experiment**. Open {{uid:exp_main}} and read
   its **Falsifier**: the result that would prove the claim wrong.
5. In the inspector, scroll to **Links**. Add a `supports` link from an
   evidence node to a claim.

## Tips and common mistakes

- **Write the falsifier before the run.** Afterwards it is too easy to
  explain any result.
- A claim with no evidence is fine; it shows up as a gap on **Home** and **Map**.
- Keep one status per node (`open`, `active`, `review`, `done`...). ARK
  refuses a second one.
- Prompt 2 on **Home** asks an agent to draft this whole chain for you, and
  waits for your OK before writing.
""", media=[("research/data-model.svg", "figure", "The data model: units with one type and status, joined by typed links")])

example("research", "idea", "Learning-rate warm-up might stabilise small-model training", "idea", """\
Small models (about 60M parameters) sometimes show sharp loss spikes in the
first few thousand steps. Large-batch training uses a warm-up to avoid this.
**Maybe a short warm-up helps small models too.**

Where it came from: the tutorial note {{uid:source}} and a run log that
spiked at step 750.

Next: turn it into a question ({{uid:question}}).
""", status="active", importance="p1", tags=["topic:optimisation"])

example("research", "source", "Tutorial note: warm-up and early loss spikes (PDF)", "source", """\
A two-page note with the question, setup, result and limits. The numbers
are **fictional**; they exist to show the workflow.

- `warmup-note.pdf` - the note as a PDF (opens in the **Editor**)
- `warmup-note.md` - the same note as Markdown
- `references.bib` - two BibTeX entries

Open any attachment with **Open in Editor** in the attributes panel. The PDF
opens in the browser's PDF viewer.
""", status="done", importance="p2", tags=["topic:optimisation", "kind:note"], media=[
    ("research/warmup-note.pdf", "pdf", "Tutorial note as a PDF (fictional data)"),
    ("research/warmup-note.md", "document", "Tutorial note as Markdown"),
    ("research/references.bib", "document", "BibTeX for the two related works"),
])

example("research", "question", "Does a 500-step warm-up remove early loss spikes?", "question", """\
**Question:** in a 60M-parameter model, does a 500-step linear warm-up of
the learning rate remove the loss spikes we see before step 3,000?

**Why it matters:** each spike costs a restart or a worse final model.

**What would settle it:** 3 seeds per arm, spikes counted with one fixed
rule (see {{uid:exp_main}}).
""", status="active", importance="p1", tags=["topic:optimisation"])

example("research", "claim_spikes", "Warm-up removes early loss spikes", "claim", """\
**Claim:** with a 500-step warm-up, runs show no loss spikes in the first
3,000 steps.

**Evidence so far:** {{uid:evidence}} - 0 spikes per run with warm-up, 3
without, in 3 of 3 seeds.

**Status: review.** The evidence is in; a reviewer still has to accept the
task that produced it ({{uid:task_main}}).
""", status="review", importance="p1", tags=["topic:optimisation"])

example("research", "claim_final", "Warm-up does not hurt the final loss", "claim", """\
**Claim:** warm-up does not make the final loss worse.

The main run hints at this (2.08 vs 2.14), but one learning rate is not
enough. The sweep {{uid:exp_sweep}} will decide.

**Status: open.** No evidence is linked yet, so **Map** and **Home** flag
it as a gap. That is the point: gaps should be visible.
""", status="open", importance="p1", tags=["topic:optimisation"])

example("research", "evidence", "0 spikes per run with warm-up, 3 without (3 seeds)", "evidence", """\
With warm-up no run spiked; without it every run spiked three times.

| Arm | Final loss (mean) | Spikes per run |
|---|---|---|
| no warm-up | 2.14 | 3.0 |
| 500-step warm-up | 2.08 | 0.0 |

![Mean training loss with and without warm-up; the no-warm-up curve spikes at steps 750, 1,500 and 2,250]({{raw:research/results-chart.svg}})

Source data: `results.csv` (attached). Produced by {{uid:exp_main}}.
""", status="review", importance="p1", tags=["topic:optimisation"], media=[
    ("research/results-chart.svg", "figure", "Mean training loss, warm-up vs none, 3 seeds per arm"),
    ("research/results.csv", "data", "Per-step mean loss for both arms (source of the chart)"),
])

example("research", "exp_main", "Warm-up vs no warm-up: 3 seeds, 5,000 steps", "experiment", """\
**Protocol:** 60M decoder, AdamW, peak learning rate 6e-4, cosine decay.
Two arms (no warm-up, 500-step warm-up), 3 seeds each, 5,000 steps.

**Spike rule** (fixed before the run): step $t$ is a spike when its loss is
more than 1.5 times the running mean of the previous 50 steps:

$$ \\text{spike}(t) \\iff L_t > 1.5 \\cdot \\frac{1}{50}\\sum_{k=t-50}^{t-1} L_k $$

**Falsifier:** if the warm-up arm shows one or more spikes per run on
average, claim {{uid:claim_spikes}} is false.

**Budget:** one GPU with a 20 GB memory claim, about 2 GPU-hours.

The work is task {{uid:task_main}} on the **Execute → Board**.
""", status="review", importance="p1", tags=["topic:optimisation", "falsifier:spikes>=1"])

example("research", "exp_sweep", "Learning-rate sweep: 6 rates × 3 seeds", "experiment", """\
**Question:** at which peak learning rate does warm-up stop helping, and
does the final loss suffer?

**Falsifier for {{uid:claim_final}}:** if the best warm-up run is worse than
the best no-warm-up run by more than 0.02, the claim is false.

Partial results so far. The chart below is plain HTML. Click **Run
visualisation** to start it; it runs in a sandbox with no network access.
Hover a bar for the exact value, or switch the metric.

```html
{{html:editor/lr-sweep.html}}
```

Data: `lr-sweep.csv`. The same chart is a file in the **Editor** chapter
({{uid:sweep_html}}).
""", status="active", importance="p2", tags=["topic:optimisation"], media=[
    ("editor/lr-sweep.csv", "data", "Learning-rate sweep results (source of the chart)"),
])

example("research", "method", "Linear warm-up, then cosine decay", "method", """\
The learning rate rises linearly for $W$ steps, then follows a cosine
decay to zero at step $T$:

$$ \\eta(t) = \\begin{cases} \\eta_{\\max} \\, t / W & t < W \\\\ \\tfrac{1}{2}\\,\\eta_{\\max}\\left(1 + \\cos\\frac{\\pi (t - W)}{T - W}\\right) & t \\ge W \\end{cases} $$

In this study $W = 500$, $T = 5000$ and $\\eta_{\\max} = 6 \\times 10^{-4}$.
Used by {{uid:exp_main}} and {{uid:exp_sweep}}.
""", status="done", importance="p2", tags=["topic:optimisation"])

example("research", "related", "Goyal et al. 2017: warm-up for large-batch SGD", "related_work", """\
**Why it matters here:** the original argument for a gradual warm-up. It
studies large batches, not small models, so it motivates our question but
does not answer it.

**Venue:** arXiv 1706.02677. The BibTeX is attached (`references.bib`).

Cited by {{uid:claim_spikes}}.
""", status="done", importance="p2", tags=["venue:arXiv", "topic:optimisation"], media=[
    ("research/references.bib", "document", "BibTeX: Goyal et al. 2017 and Liu et al. 2020"),
])

example("research", "topic_story", "Topic: the warm-up argument, step by step", "topic", """\
A **topic** is a node that holds its own small graph. This one tells the
warm-up study as one storyline: the question, the method, the two
experiments, what they showed and the two claims.

Open it from **Research → Topics**, or double-click it on the Map. The
numbers on the cards are the reading order; the captions belong to the
topic, not to the nodes. *Choose links* hides links that do not serve the
story without deleting them, and *Rule* pulls in every node that matches a
filter, including nodes added later.
""", status="active", importance="p1", tags=["topic:optimisation"])

TOPICS["topic_story"] = {
    "members": [
        ("idea", "Where it started"),
        ("question", "The testable question"),
        ("method", "What we change"),
        ("exp_main", "Main comparison"),
        ("evidence", "What it showed"),
        ("claim_spikes", "Claim 1"),
        ("exp_sweep", "Robustness check"),
        ("claim_final", "Claim 2"),
        ("figure", "The figure readers see"),
    ],
}

# ===================================================================== MAP
chapter("map", 3, "Map: see the whole argument at once", """\
Chapter 3 of 8. Map shows every claim, its evidence and the gaps in one picture.

## What Map is for

Map draws every question, claim, evidence, experiment and task as one
graph. It is the fastest way to see what supports what, and what is still
missing. The graph is a **view** of the data: moving nodes or zooming never
changes the project.

![Map legend: every unit type with its colour and ID prefix]({{raw:map/map-legend.svg}})

## Try it

1. Click **Map**. Click **Fit** if the graph is off-screen.
2. Click {{uid:question}}. The inspector opens on the right with its links.
3. Turn on **Show linked context** to see neighbours of what you filtered.
4. In the filter rail, pick **Types → Claim**. Only claims stay; clear it after.
5. Select a claim and click **Trace** to follow its incoming and outgoing logic.
6. Switch **Layout** between **Force**, **Tree** and **Mind**.

## How to spot gaps

- A **claim with no incoming `supports`** has no evidence yet ({{uid:claim_final}}).
- A **question with no claim** is not testable yet ({{uid:open_question}}).
- A **decision** records why you chose a path ({{uid:decision_sweep}}).

## Tips and common mistakes

- Dragging a node only moves it in this view. Edit links in the inspector.
- Filters combine: OR inside a category, AND across categories.
- The little count in the toolbar is what is *visible*, not the total.
- On a phone: one finger pans, two fingers zoom.
""", media=[("map/map-legend.svg", "figure", "Map legend: type colours and ID prefixes")])

example("map", "open_question", "Does the effect hold for a 350M-parameter model?", "question", """\
An open question with **no claims yet**. On the Map it sits alone next to
{{uid:question}}: a visible gap.

To make it testable, add a claim and an experiment, or ask an agent with
prompt 2 on **Home** (*"Plan this idea: ..."*).
""", status="open", importance="p2", tags=["topic:scaling"])

example("map", "decision_sweep", "Run the learning-rate sweep before claiming 'no harm'", "decision", """\
**Decision:** we do not accept {{uid:claim_final}} on one learning rate.
We run the sweep {{uid:exp_sweep}} first.

**Why:** one rate can hide a harm that shows up at higher rates.

**Who:** you (reviewer), 2026-11-02. **Status: done.**
""", status="done", importance="p2", tags=["topic:optimisation"])

# ================================================================= EXECUTE
chapter("execute", 4, "Execute: run tasks with agents, safely", """\
Chapter 4 of 8. Execute is where tasks move across the board, agents run, and GPU jobs wait in a fair queue.

## What Execute is for

Execute is where work happens. **Board** shows tasks by status. **Runs**
shows agent sessions as a live thread, with a model picker that lists the
models your provider has. **GPU** shows servers and the job queue.
**Files** browses the project folder.

## A task's life

```mermaid
stateDiagram-v2
  [*] --> inbox: captured
  inbox --> open: triaged
  open --> active: agent starts in a worktree
  active --> blocked: waiting on something
  blocked --> active
  active --> review: inspection report attached
  review --> done: reviewer accepts
  review --> active: reviewer returns it
  done --> [*]
```

## Try it

1. Click **Execute**, then **Board**. Find one task in each column.
2. Open {{uid:task_main}}. Its body is a **goal manifest** made by the
   agent-team skill; the **inspection report** is attached.
3. Drag a task to another column to change its status.
4. Click **Runs**. In a real project, **Launch agent** opens a session; pick
   a provider and a model from the picker. (Agents are off in this demo.)
5. Click **GPU**. Then read {{uid:gpu_request}} for how a job picks a GPU.
6. Click **Files** and open `assets/tutorial/execute/run-log-excerpt.log`.

## Tips and common mistakes

- **One task, one worktree, one branch.** The agent stays in its owned paths.
- **Review is a person's job.** The agent moves a task to `review`; only the
  reviewer moves it to `done`.
- Push, GPU jobs, deletion and posts wait for your approval, even when the
  agent is sure.
- A task without a measurable outcome cannot be reviewed. Write one.
""", media=[
    ("execute/run-log-excerpt.log", "log", "Run log excerpt for T-0001, with the approval step"),
])

# The goal manifest body is filled in by demo.py from the agent-team templates.
example("execute", "task_main", "Run the 3-seed warm-up comparison", "task",
        "{{manifest}}", status="review", importance="p1",
        tags=["topic:optimisation", "agent:codex"], media=[
            ("execute/goal_manifest.md", "document", "Goal manifest (agent-team skill template)"),
            ("execute/inspection_report.md", "document", "Inspection report, waiting for the reviewer"),
            ("execute/run-log-excerpt.log", "log", "Run log excerpt"),
        ])

example("execute", "task_sweep", "Run the learning-rate sweep (6 rates × 3 seeds)", "task", """\
**Outcome:** final loss and spikes per run for 6 learning rates, 3 seeds each.

**Status: active.** 9 of 18 runs are done. The rest wait in the GPU queue;
see {{uid:gpu_request}} for the request each run sends.

**Human step:** the agent asked before the first GPU job and you approved
"one GPU at a time" ({{uid:approve_gpu}}).
""", status="active", importance="p1", tags=["topic:optimisation", "agent:codex"],
        start_at="2026-11-02", end_at="2026-11-06")

example("execute", "task_smoke", "Write the 50-step smoke config for the 350M model", "task", """\
**Outcome:** `configs/350m/smoke.yaml` trains for 50 steps without NaNs.

**Status: open.** Nobody has started. A good first task for an agent: small,
cheap to check, one owned path.
""", status="open", importance="p2", tags=["topic:scaling"])

example("execute", "task_blocked", "Fetch the 350M checkpoint from shared storage", "task", """\
**Status: blocked.** Waiting for read access to the shared storage bucket.

When something blocks a task, say **what** and **who** can unblock it, so the
status check (*"Give me a status check"* after prompt 2 on **Home**) can report it.
""", status="blocked", importance="p2", tags=["topic:scaling", "blocked-by:storage-access"])

example("execute", "task_inbox", "Draft the related-work paragraph", "task", """\
**Status: inbox.** Captured but not triaged. Move it to **open** when it has
an owner and a measurable outcome.
""", status="inbox", importance="p3", tags=["topic:writing"])

example("execute", "task_done", "Set up the warmup-study repository", "task", """\
**Status: done.** Repository created, `pytest -q` passes, smoke config runs.
Accepted by you on 2026-11-01.
""", status="done", importance="p2", tags=["topic:infra"])

example("execute", "agent_codex", "Codex (example coding agent)", "agent", """\
An **agent** unit stands for a coding agent that takes tasks. Tasks link to
it with `assigned_to`, and **Execute → Board** shows the load.

In a real project, the provider and model come from **Settings → Providers**;
the **Runs** model picker lists the models ARK detected for that provider.
""", status="active", importance="p3", tags=["provider:codex"])

example("execute", "gpu_request", "GPU queue: how a job picks a GPU (example, not submitted)", "resource", """\
An example GPU job request and the rule ARK uses to place it. Nothing is submitted.

**What this is:** an example of the request an agent sends to the GPU
queue (`POST /api/gpu/jobs`). The tutorial never submits it.

```json
{
  "name": "warmup-b-s0",
  "task": "T-0001",
  "command": "python train.py --config configs/warmup/arm_b.yaml --seed 0",
  "work_dir": "/data/projects/warmup-study",
  "gpus": 1,
  "gpu_memory_mb": 20000,
  "timeout_minutes": 120
}
```

**The dispatch rule.** For each queued job, oldest first:

1. **Filter.** Keep GPUs whose available memory fits the claim.
2. **Rank.** Least utilised first. Ties: more memory, then lower index.
3. **Dispatch.** Reserve the claim before looking at the next job.

$$ g^{*} = \\arg\\min_{g \\,:\\, F_g - R_g \\,\\ge\\, c} \\; U_g $$

where $F_g$ is free memory from `nvidia-smi`, $R_g$ is memory already
claimed by ARK jobs that are still launching, $c$ is the job's
`gpu_memory_mb` and $U_g$ is utilisation.

```mermaid
flowchart TD
  J["Job claims 20 GB"] --> F{"available = free - claimed >= 20 GB?"}
  F -- no --> S["GPU skipped"]
  F -- yes --> R["Rank by utilisation"]
  R --> P["Pick the least utilised"]
  P --> V["Reserve 20 GB on it"]
  V --> N["Next job in the queue"]
```

![GPU dispatch example: GPU 1 and GPU 3 are too small, GPU 2 is less busy than GPU 0 and wins]({{raw:execute/gpu-dispatch.svg}})

**Human step:** GPU jobs are on the *ask first* list. The agent stops and
asks you before it submits ({{uid:approve_gpu}}).
""", status="active", importance="p2", tags=["topic:gpu"], media=[
    ("execute/gpu-dispatch.svg", "figure", "GPU dispatch: filter by memory claim, then least utilised first"),
    ("execute/gpu-queue-request.json", "data", "Example queue request with a gpu_memory_mb claim (not submitted)"),
])

# ================================================================== OUTPUT
chapter("output", 5, "Output: turn results into figures, posters and posts", """\
Chapter 5 of 8. Output turns results into checked figures, a poster and planned posts, all in one visual style.

## What Output is for

Output is where results become things other people see. **Assets** collects
figures, posters, videos and the visual system (palette and fonts).
**Distribution** plans posts and talks by channel. ARK never posts for you:
every public item needs your explicit approval.

![Colour board: the academic-clean theme from the design skill]({{raw:output/colour-board.svg}})

## Try it

1. Click **Output**, then **Assets**. The **Palette** comes from `color:`
   tags on {{uid:style}}; the fonts come from `font:` tags.
2. Open {{uid:style}} and its attached `colour-board.svg` and `theme.json`.
3. Open {{uid:figure}}. Its body lists the checks the design skill ran on it.
4. Open {{uid:poster}}: a poster built from the same theme.
5. Click **Distribution**. Posts sit in lanes by channel (X, WeChat, web, talk).
6. Open {{uid:post_x}}. It waits for approval and is never sent by ARK.

## Tips and common mistakes

- **Name colours by role** (`data_primary`), not by look (`blue_box`).
- Run the design check before you share a figure: it must be valid,
  self-contained and inside the project folder.
- Keep versions: add a new production node or attachment, don't overwrite.
- A post marked `risk:external` always waits for a person.
""", media=[
    ("output/colour-board.svg", "figure", "Colour board: roles, ramps and type scale of the academic-clean theme"),
    ("output/theme.json", "data", "The design skill's example theme (academic-clean)"),
])

example("output", "style", "Visual system: academic-clean theme", "production", """\
The design skill's example theme. Every figure, the poster and the HTML
chart in this demo use it.

| Role | Colour | Used for |
|---|---|---|
| `surface` | `#F7F5EF` | page and figure background |
| `text_primary` | `#111827` | headings, axes, body text |
| `data_primary` | `#1F5A7A` | the main thing you measure |
| `data_secondary` | `#C2683F` | the comparison or baseline |
| `success` | `#2F7D5B` | validated state |
| `failure` | `#B23A48` | failure or contradiction |

The `color:` and `font:` tags on this node fill the **Palette** and **Type**
panels in **Output → Assets**. The full theme is the attached `theme.json`.

![Colour board for the academic-clean theme]({{raw:output/colour-board.svg}})
""", status="active", importance="p1", tags=[
    "role:style", "color:#1F5A7A", "color:#C2683F", "color:#2F7D5B", "color:#7C5C9E",
    "color:#B7791F", "color:#B23A48", "color:#F7F5EF", "color:#111827", "font:Inter",
    "font:Source-Serif-4", "font:JetBrains-Mono"], media=[
    ("output/colour-board.svg", "figure", "Colour board (SVG): 12 roles, 3 ramps, type scale"),
    ("output/theme.json", "data", "theme.json from the design skill"),
])

example("output", "figure", "Figure 1: warm-up removes early spikes (checked)", "artifact", "{{figure_check}}",
        status="done", importance="p1", tags=["role:figure"], media=[
            ("output/fig-main-result.svg", "figure", "Figure 1: mean training loss, 3 seeds per arm"),
        ])

example("output", "poster", "Poster: warm-up removes early loss spikes", "production", """\
A one-page poster in the academic-clean theme: headline, main chart, three
key numbers and the limits.

![Poster cover: headline, main chart and three key numbers]({{raw:output/poster-cover.svg}})

**Status: review.** Check the numbers against {{uid:evidence}} before it
goes anywhere.
""", status="review", importance="p2", tags=["role:poster", "audience:lab"], media=[
    ("output/poster-cover.svg", "figure", "Poster cover (SVG)"),
])

example("output", "post_x", "X thread: three runs, zero spikes", "outreach", """\
**Draft:**

> Small models spike early in training. A 500-step warm-up removed every
> early spike in our runs (3 seeds, 60M params). Final loss: 2.08 vs 2.14.
> Caveat: one model size. Sweep in progress.

**Human step:** this is `risk:external`. It waits for your approval, and
ARK never posts it; you publish it yourself.
""", status="open", importance="p2", tags=["channel:x", "risk:external"],
        start_at="2026-11-10")

example("output", "post_talk", "Lab meeting: 10-minute update on warm-up", "outreach", """\
Slides: the poster ({{uid:poster}}) plus the sweep chart. Internal, so no
external-risk tag.
""", status="active", importance="p2", tags=["channel:talk"], start_at="2026-11-07")

example("output", "post_web", "Project page: add the warm-up result", "outreach", """\
Add Figure 1 ({{uid:figure}}) and a two-line summary to the project web
page after the reviewer accepts {{uid:task_main}}.
""", status="inbox", importance="p3", tags=["channel:web"])

example("output", "post_wechat", "WeChat post: summary for the group", "outreach", """\
**Status: blocked.** Waiting for the sweep ({{uid:exp_sweep}}), so the post
does not over-claim.
""", status="blocked", importance="p3", tags=["channel:wechat", "risk:external"])

# ================================================================== EDITOR
chapter("editor", 6, "Editor: open any file or node in tabs", """\
Chapter 6 of 8. The Editor opens any project file, attachment or node in a tab: images, video, PDF, tables, code and whole nodes.

## What the Editor is for

The Editor opens project files, node attachments and whole nodes in VS
Code-style tabs. It previews images, video, audio, PDF, Markdown, CSV, JSON
and code, and edits project text files. A node tab is a wide editor for one
node, with **Source**, **Split** and **Preview** views of its body. Use the
Editor to look at results and write long notes without leaving ARK.

This 5-second video is a project file, embedded in the node body:

![Animated training curves: both loss curves drawn over 5,000 steps]({{raw:editor/warmup-curves.mp4}})

## Try it

1. Right-click this node on the **Map** (or press **Alt+Enter**) and choose
   **Open in Editor**. Switch between **Source**, **Split** and **Preview**.
2. Click **Editor** in the top bar, open the explorer and go to
   `assets/tutorial/editor/`. Click `warmup-curves.mp4`; it plays in a tab.
3. Click `lr-sweep.csv`. It opens as a table; switch to the source view.
4. Open {{uid:exp_sweep}} in the Editor. Its chart is an `html` block: click
   **Run visualisation** to start it in a sandbox.
5. Double-click a file to **pin** its tab. A single click opens an *italic*
   preview tab that the next click replaces.
6. Press **Ctrl+S** to save and **Ctrl+W** to close a tab (in the installed
   app window; a browser tab keeps Ctrl+W for itself).

## Tips and common mistakes

- Attachments are **read-only**; project files and node tabs are editable.
- Visualisations never run until you click **Run visualisation**. They run
  in a sandbox with no network, no cookies and no access to ARK.
- Files over 1 MiB open as an info card with **Download**.
- If someone else saved the node while you typed, a banner offers **Reload**
  or **Overwrite**.
""", media=[
    ("editor/warmup-curves.mp4", "video", "Animated training curves (5 s, MP4)"),
    ("editor/warmup-curves-poster.png", "image", "Final frame of the training-curve video (PNG)"),
])

example("editor", "video", "Video: animated training curves (5 s)", "artifact", """\
A 5-second MP4 that draws both loss curves step by step. It was generated
by `scripts/dev/build_demo_assets.py` with ffmpeg, from the same data as
`results.csv`.

![Animated training curves, warm-up vs none]({{raw:editor/warmup-curves.mp4}})

The final frame is also attached as a PNG, for slides and documents:

![Final frame: both training curves after 5,000 steps]({{raw:editor/warmup-curves-poster.png}})

Tip: open the attachment with **Open in Editor** to watch it in its own tab.
""", status="done", importance="p2", tags=["role:video"], media=[
    ("editor/warmup-curves.mp4", "video", "Animated training curves, warm-up vs none (MP4, 5 s)"),
    ("editor/warmup-curves-poster.png", "image", "Final frame of the video"),
])

example("editor", "sweep_html", "Interactive chart: learning-rate sweep (HTML)", "artifact", """\
A self-contained HTML chart (no network, no ARK API calls) plus its data.

- `lr-sweep.html` - bars for final loss or spikes per run, with hover
  tooltips, a metric toggle and a data table; follows light/dark mode.
- `lr-sweep.csv` - the numbers behind it.

The same chart is embedded as a sandboxed `html` block in {{uid:exp_sweep}}.
Opening `lr-sweep.html` in the **Editor** shows it in the same sandbox, with a
source toggle.
""", status="done", importance="p2", tags=["role:figure"], media=[
    ("editor/lr-sweep.html", "document", "Interactive learning-rate sweep chart (self-contained HTML)"),
    ("editor/lr-sweep.csv", "data", "Learning-rate sweep data (CSV)"),
])

# ========================================================== AGENTS & SKILLS
chapter("agents", 7, "Agents & skills: what the agent does, what you decide", """\
Chapter 7 of 8. Your coding agent does the work through ARK's agent API; you keep three decisions for yourself.

## What this is for

ARK does not run hidden AI. Your coding agent (DSH, Codex or Claude Code)
does the work and talks to ARK through the **agent API**. **Skills** are
written instructions the agent reads first. You stay in charge of three
decisions: confirming setup, approving risky actions, and accepting work.

![Where the agent stops and asks you: setup, risky steps and review]({{raw:agents/human-in-the-loop.svg}})

```mermaid
sequenceDiagram
  participant You
  participant Agent
  participant ARK
  Agent->>ARK: GET /setup, /context, /skills/agent-team
  Agent->>ARK: propose setup profile
  You->>ARK: Review & confirm
  Agent->>ARK: validate goal manifest, start work
  Agent->>You: "GPU job next. OK?"
  You->>Agent: approve
  Agent->>ARK: inspection report + evidence (status: review)
  You->>ARK: accept (status: done)
```

## Try it

1. Click **Assistant** (or press **Ctrl+J**). Type `summary`, then
   `what should I do next`. The built-in harness works without an API key.
2. In the Assistant, open **Agent API** and find **Reveal token**. This is
   the token your coding agent sends as `Authorization: Bearer <token>`.
3. Open **Settings → Skills**. Read the four skills: **setup**,
   **agent-team**, **rrsi** and **design**.
4. Open {{uid:approve_gpu}} and {{uid:accept_main}}: the two human steps
   waiting in this demo.

## Tips and common mistakes

- **Never put the token in a unit, a file or a commit.**
- The agent cannot confirm setup or accept its own work. That is by design.
- If an agent skips the *ask first* list, stop it and fix the setup profile.
- The Assistant writes through the same store as the UI, so its changes
  show up in the activity log.
""", media=[("agents/human-in-the-loop.svg", "figure", "Human-in-the-loop: the three decisions that stay with a person")])

example("agents", "skills", "The four built-in skills", "resource", """\
Skills are written instructions your agent reads before it starts. ARK ships four.

| Skill | The agent uses it to... | Human step |
|---|---|---|
| **setup** | discover repos, servers and agents, then propose a profile | you confirm on **Home** |
| **agent-team** | write a goal manifest, pick solo or team, run gates, report | reviewer accepts |
| **rrsi** | run propose → critique → analyse rounds on a hard question | you pick the direction |
| **design** | apply a theme and check figures, sites and video | you approve publication |

Agents read them at `GET /api/agent/v1/skills/<name>`. People read them in
**Settings → Skills**.
""", status="active", importance="p2", tags=["topic:skills"])

example("agents", "approve_gpu", "Approve GPU jobs for the learning-rate sweep", "decision", """\
**Human-in-the-loop example.** The agent working on {{uid:task_sweep}}
stopped before its first GPU job, because GPU jobs are on the *ask first*
list of the confirmed setup.

**Agent asked:** "Submit 18 jobs, 20 GB each, one GPU at a time?"

**You answered:** "Yes, one GPU at a time. Stop if a run goes NaN."

**Status: done.** Approval recorded here, so the next check-in can see it.
""", status="done", importance="p1", tags=["tutorial:human-step"])

example("agents", "accept_main", "Accept or return T-0001 (reviewer)", "decision", """\
**Human-in-the-loop example.** {{uid:task_main}} is in **review**. The
agent attached `inspection_report.md` with commands, results and risks.

**What the reviewer checks**

1. The commands in the report ran and passed.
2. The numbers in {{uid:evidence}} match `results.csv`.
3. Residual risks are honest (3 seeds, one model size).

Then move {{uid:task_main}} to **done**, or back to **active** with a note.

**Status: open** until you decide.
""", status="open", importance="p1", tags=["tutorial:human-step"], media=[
    ("execute/inspection_report.md", "document", "Inspection report for T-0001"),
])

# ======================================================== SETTINGS & MOBILE
chapter("settings", 8, "Settings & mobile: scopes, providers, GPU and your phone", """\
Chapter 8 of 8. Settings separates shared project options from your personal ones, and pairs your phone.

## What Settings is for

Settings has three scopes. **Project** settings are shared with everyone on
the project. **Personal** settings (your model key, your agent commands,
your password) are yours alone. **Admin** is for organisation owners. A
personal value always wins over a project value.

![Settings at a glance: Project, Personal and Admin tabs and the precedence rule]({{raw:settings/settings-map.svg}})

## Try it

1. Click the **Settings** icon (top right). Look at the left list: tabs are
   grouped by scope.
2. Open **Providers**. ARK lists the coding-agent CLIs it found and the
   models each one offers; the **Runs** model picker uses this list.
3. Open **Skills** to read the agent instructions ({{uid:agents}}).
4. Open **GPU**. Add servers by name in the private server list, then
   **Probe**. Probing is read-only.
5. Open **Mobile** and generate a pairing QR. Scan it with your phone.
6. To end a phone's access, **Revoke** it in the same panel.

## Tips and common mistakes

- **Phone pairing gives full access by default and lasts until you revoke
  it.** Choose read-only if the phone only needs to watch.
- The QR code never contains a password or SSH key.
- Project settings hold env-var *names*; your secret values stay personal.
- The demo cannot be deleted, and coding agents are off here.
""", media=[("settings/settings-map.svg", "figure", "Settings at a glance: scopes and precedence")])

example("settings", "providers", "Providers and the model picker", "resource", """\
**Settings → Providers** shows each coding-agent CLI (Codex, Claude Code,
DSH...), whether it is installed and signed in, and its models.

Models come from three places, shown in this order in the **Runs** picker:

1. **Configured** - models you listed yourself;
2. **Detected** - models ARK found in the provider's own config;
3. **Built-in** - a safe default list.

Detection is best-effort: it never proves a login works. If a model is
missing, add it under **Configured**.
""", status="active", importance="p2", tags=["topic:providers"])

example("settings", "mobile", "Pair a phone in one minute", "resource", """\
1. On the desktop: **Settings → Mobile → Generate pairing QR**.
2. Choose **Full access** (default) or **Read-only**.
3. Scan the code with the phone's camera. ARK opens, signed in to this
   project only.
4. Optional: **Add to Home Screen** to install it as an app (needs HTTPS).
5. Lost the phone? **Revoke** it in **Settings → Mobile**. Access ends at once.

The code is single-use and expires quickly. It is pinned to the project
that made it.
""", status="active", importance="p2", tags=["topic:mobile"])

example("settings", "security", "ARK keeps secrets and access explicit", "claim", """\
- Loopback is the default; a public address needs an account.
- Every project-scoped API checks access on the server, not just in the UI.
- Credentials are personal and sealed; project settings hold `env:NAME`
  references only.
- The setup profile is confirmed by a person; agents get a 403 if they try.
- Full-access agent runs and full-access phone pairings are explicit choices.
""", status="active", importance="p2", tags=["topic:security"])

# =================================================================== LINKS
# Research chain
link("question", "idea", "derived_from", "The idea became a testable question.")
link("idea", "source", "cites")
link("claim_spikes", "question", "targets")
link("claim_final", "question", "targets")
link("evidence", "claim_spikes", "supports", "0 vs 3 spikes per run, 3 of 3 seeds.")
link("exp_main", "evidence", "produces")
link("exp_main", "claim_spikes", "validates")
link("exp_sweep", "claim_final", "validates")
link("exp_main", "method", "uses")
link("exp_sweep", "method", "uses")
link("claim_spikes", "related", "cites")
# Map
link("open_question", "question", "relates_to", "Same question at a larger scale.")
link("decision_sweep", "claim_final", "targets")
# Execute
link("task_main", "exp_main", "part_of")
link("task_sweep", "exp_sweep", "part_of")
link("task_smoke", "open_question", "targets")
link("task_blocked", "task_smoke", "blocks")
link("task_main", "agent_codex", "assigned_to")
link("task_sweep", "agent_codex", "assigned_to")
link("task_sweep", "gpu_request", "uses")
link("task_done", "task_main", "blocks", "The repository had to exist first.")
# Home / agents
link("setup_decision", "prompt_setup", "derived_from")
link("approve_gpu", "task_sweep", "targets")
link("accept_main", "task_main", "targets")
# Output
link("figure", "evidence", "derived_from")
link("poster", "figure", "uses")
link("poster", "style", "uses")
link("figure", "style", "uses")
link("post_x", "figure", "uses")
link("post_talk", "poster", "uses")
link("post_web", "figure", "uses")
link("post_wechat", "exp_sweep", "depends_on")
link("task_main", "figure", "produces")
# Editor
link("exp_sweep", "sweep_html", "produces")
link("exp_main", "video", "produces")
