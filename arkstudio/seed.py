"""Starter content.

The demo is intentionally dense: it exercises every view (story outline, logic
graph, task board, gantt, production palette, outreach calendar) and gives the
user something to delete.  It is not evidence for any paper claim.
"""
from __future__ import annotations

import shutil
from pathlib import Path

from .store import Store


def _copy_demo_assets(store_dir: Path) -> list[str]:
    src = Path(__file__).resolve().parent.parent / "demo-assets"
    assets = store_dir / "assets"
    assets.mkdir(exist_ok=True)
    copied = []
    for name in ("workbench-overview.png", "mind-graph.png"):
        source = src / name
        if source.is_file():
            target = assets / name
            if not target.exists():
                shutil.copy2(source, target)
            copied.append(f"assets/{name}")
    return copied


def seed_blank(store: Store) -> None:
    store.create_unit(
        {
            "type": "note",
            "title": "Start here",
            "body": (
                "This project is a single node graph. Capture an idea, ask a question, "
                "register a claim, plan an experiment, or add a task — then connect it.\n\n"
                "Open Ideas to capture, Plan to shape the story, Map to test the logic, "
                "and Tasks to hand work to agents."
            ),
            "tags": ["type:note", "status:open", "importance:p2", "stage:capture", "pin:true"],
        },
        actor="system",
    )


def seed_paper(store: Store) -> None:
    seed_blank(store)
    root = store.create_unit(
        {
            "type": "story",
            "title": "Paper story spine",
            "body": "The narrative is edited here, then wired to claims, methods and evidence.",
            "tags": ["stage:story", "status:active", "importance:p1"],
        },
        actor="system",
    )
    for i, (title, body) in enumerate(
        [
            ("Motivation", "Why should anyone care about this problem?"),
            ("Gap", "What do existing approaches fail to do?"),
            ("Insight", "What is the one idea that closes the gap?"),
            ("Contribution", "What artifacts, results and analysis do we contribute?"),
            ("Evidence", "Which experiments can falsify the claim?"),
            ("Limits", "What does this work deliberately not claim?"),
        ],
        start=1,
    ):
        child = store.create_unit(
            {
                "type": "story",
                "title": title,
                "body": body,
                "tags": ["stage:story", "status:open", "importance:p2", f"seq:{i:02d}"],
            },
            actor="system",
        )
        store.create_link(child["id"], root["id"], "part_of", "", actor="system")
    store.create_unit(
        {
            "type": "task",
            "title": "Draft the one-paragraph argument",
            "body": "Turn the story spine into a single paragraph that a reviewer can repeat back.",
            "tags": ["stage:task", "status:open", "importance:p1", "topic:story"],
            "start_at": "2026-10-29",
            "end_at": "2026-11-02",
        },
        actor="system",
    )


def seed_demo(store: Store, project_path: Path) -> None:
    assets = _copy_demo_assets(project_path)
    overview = assets[0] if assets else ""
    mind = assets[1] if len(assets) > 1 else ""

    units: dict[str, dict] = {}

    def add(key: str, **data) -> dict:
        unit = store.create_unit(data, actor="system")
        units[key] = unit
        return unit

    def link(a: str, b: str, relation: str, note: str = "") -> None:
        if a in units and b in units:
            try:
                store.create_link(units[a]["id"], units[b]["id"], relation, note, actor="system")
            except Exception:
                pass

    # ---------------------------------------------------------------- story
    add(
        "story.root", type="story", title="PredActor — the paper as a graph",
        body=(
            "PredActor asks whether behavior-level control can be both predictable and deployable. "
            "The story is one chain: motivation → gap → insight → contribution → method → evidence → limits. "
            "Every node in this chain points at the claim or experiment that carries it."
        ),
        tags=["stage:story", "status:active", "importance:p0", "topic:control"],
        start_at="2026-10-01", end_at="2026-11-25",
        media=[{"url": mind, "kind": "figure", "caption": "PredActor mind graph"}] if mind else [],
    )
    story = [
        ("story.motivation", "Motivation — control is a behavior problem",
         "Robot learning has strong trackers and strong semantic priors, but the layer that decides what behavior to request, when, and why is still hand-written. That layer is the missing object of study.",
         ["seq:01", "topic:control"]),
        ("story.gap", "Gap — predictable policies are not deployable policies",
         "Diffusion policies are remarkably stable at short horizons, yet they fail when the observation stream shifts, when the command changes mid-episode, or when a tracker consumes the action chunk. Predictability is measured; deployability is asserted.",
         ["seq:02", "topic:deployability"]),
        ("story.insight", "Insight — separate semantic intent from physical tracking",
         "The paper's bet is that a behavior-level controller should output a semantic action request, while a tracker owns physical execution. This makes recovery and replanning measurable at the boundary rather than hidden inside one network.",
         ["seq:03", "topic:control"]),
        ("story.contribution", "Contribution — PRDP, RLOBS-DP and a closed-loop harness",
         "Contribution 1: a predictable diffusion policy that consumes behavior-level intent. Contribution 2: observation substitution as a recovery mechanism. Contribution 3: an evaluation harness that reports capability-matched comparisons instead of a single leaderboard.",
         ["seq:04", "topic:contribution"]),
        ("story.method", "Method — intent, substitution, tracking",
         "The controller is trained on episode-level behavior labels. At test time a supervisor may substitute observations, request a recovery, or hand the same intent to a different tracker. The method is evaluated as a closed loop, not as an isolated policy.",
         ["seq:05", "topic:method"]),
        ("story.evidence", "Evidence — Q1–Q4 answer different questions",
         "Q1 tests fair comparison under matched budgets. Q2 tests the DAgger negative result. Q3 measures sampler efficiency. Q4 tests whether whole-body guidance changes the boundary. None of these collapses into a global rank.",
         ["seq:06", "topic:evidence"]),
        ("story.limits", "Limits — what this paper does not claim",
         "We do not claim cross-embodiment transfer, long-horizon autonomy, or safety. The Q2 result is a negative result for the tested checkpoint, not a statement about DAgger in general.",
         ["seq:07", "topic:limits"]),
    ]
    for key, title, body, extra in story:
        add(
            key, type="story", title=title, body=body,
            tags=["stage:story", "status:open", "importance:p2", *extra],
            start_at="2026-10-05", end_at="2026-11-20",
        )
        link(key, "story.root", "part_of")

    # ---------------------------------------------------------------- ideas
    add(
        "idea.predictable", type="idea", title="Predictability without deployability is not control",
        body=(
            "A policy can have low action error and still be useless when the tracker drifts. "
            "The interesting unit is not a smoother trajectory; it is a closed loop that can notice drift and ask for a different behavior."
        ),
        tags=["status:open", "importance:p1", "topic:deployability", "origin:infohub"],
        start_at="2026-10-02", end_at="2026-10-06",
        source_title="InfoHub · control reading group",
    )
    add(
        "idea.separate", type="idea", title="Separate semantic intent from physical tracking",
        body=(
            "If the policy emits an intent such as 'turn left while keeping the tray level', the tracker can reject, retry or hand back a failure. "
            "This is the architectural move that makes recovery a first-class signal."
        ),
        tags=["status:open", "importance:p0", "topic:architecture", "origin:brainstorm"],
        start_at="2026-10-06", end_at="2026-10-09",
    )
    add(
        "idea.kimodo", type="idea", title="Kimodo labels are a scalable semantic prior",
        body=(
            "Human teleoperation does not scale. Behavior labels from a generative motion model can cover more intents, "
            "but they must be validated against the tracker's physical limits before they count as supervision."
        ),
        tags=["status:open", "importance:p1", "topic:data", "origin:brainstorm"],
    )
    add(
        "idea.replan", type="idea", title="Event-triggered replanning beats fixed chunking",
        body=(
            "Fixed action chunks spend compute on every step. A supervisor could replan only when the tracker reports a divergence or the command changes. "
            "This idea is still unconnected: find the experiment that would falsify it."
        ),
        tags=["status:inbox", "importance:p2", "topic:efficiency", "origin:scratch"],
    )
    add(
        "idea.gates", type="idea", title="Agent teams need evidence gates, not more chat",
        body=(
            "A team of agents does not become trustworthy by adding messages. It becomes trustworthy when each handoff names the claim it serves, "
            "the artifact it produces, and the gate that can reject it."
        ),
        tags=["status:open", "importance:p1", "topic:agents", "origin:ark"],
    )
    add(
        "idea.info", type="idea", title="InfoHub is the intake membrane",
        body=(
            "Chats, papers and notes arrive through InfoHub. ARK should import their structure, keep a pointer to the source, "
            "and let a human decide which fragments become research ideas."
        ),
        tags=["status:open", "importance:p1", "topic:workflow", "origin:infohub"],
        source_title="InfoHub · paper feed",
    )
    add(
        "idea.scale", type="idea", title="DAgger scaling needs matched checkpoints",
        body=(
            "The Q2 negative result may be a checkpoint mismatch before it is a statement about DAgger. "
            "Match the starting policy, the data budget and the evaluation seeds before drawing a conclusion."
        ),
        tags=["status:open", "importance:p0", "topic:protocol", "origin:experiment"],
    )

    # -------------------------------------------------------------- questions
    add(
        "q.control", type="question", title="Can one policy be both predictable and deployable?",
        body="A useful answer must separate short-horizon action quality from closed-loop recovery. Define both before running the comparison.",
        tags=["status:open", "importance:p0", "topic:control"],
    )
    add(
        "q.shift", type="question", title="Does observation substitution explain push failures?",
        body="If the same intent survives a substituted observation but the action does not, the failure is in the tracker boundary, not in the semantic prior.",
        tags=["status:open", "importance:p1", "topic:deployability"],
    )
    add(
        "q.wbg", type="question", title="What does whole-body guidance actually buy?",
        body="Separate tracking stability from semantic success. A guidance term that improves one while hurting the other is a trade-off, not a win.",
        tags=["status:open", "importance:p1", "topic:tracking"],
    )
    add(
        "q.graph", type="question", title="Does a graph-first workbench reduce lost context?",
        body="Measure whether a decision can be traced to its evidence and its task six weeks later. Count broken links and orphaned ideas as failure modes.",
        tags=["status:open", "importance:p2", "topic:workflow"],
    )

    # ----------------------------------------------------------------- claims
    add(
        "claim.prdp", type="claim", title="PRDP improves text-conditioned push survival at matched budget",
        body=(
            "Falsifier: under a frozen Q1 protocol, PRDP's push survival is not better than the matched DP baseline, or the difference disappears when the compute budget is matched. "
            "Current status is descriptive; no Holm-corrected significance claim."
        ),
        tags=["status:open", "importance:p0", "topic:claim", "falsifiable:true"],
    )
    add(
        "claim.rlobs", type="claim", title="RLOBS-DP recovers from observation noise without tracker retraining",
        body=(
            "Falsifier: recovery success is not higher than the no-substitution arm on the same seeds, or the tracker has to be retrained for the recovery to work."
        ),
        tags=["status:open", "importance:p1", "topic:claim", "falsifiable:true"],
    )
    add(
        "claim.long", type="claim", title="Behavior-level control reduces compounding error at long horizons",
        body=(
            "Falsifier: action error grows at the same rate as a flat policy after the first replan, or the improvement is explained entirely by more inference compute."
        ),
        tags=["status:open", "importance:p1", "topic:claim", "falsifiable:true"],
    )
    add(
        "claim.dagger", type="claim", title="The Q2 DAgger failure is a protocol artifact",
        body=(
            "This is a contested claim. It is only admissible if matched checkpoints and matched data budgets reproduce the failure. "
            "Until then it is a hypothesis, not a finding."
        ),
        tags=["status:blocked", "importance:p0", "topic:claim", "falsifiable:true", "contested:true"],
    )
    add(
        "claim.wbg", type="claim", title="WBG guidance helps only when the tracker is already stable",
        body="Falsifier: a clearly unstable tracker improves its semantic success under WBG without a matching stability gain.",
        tags=["status:active", "importance:p1", "topic:claim", "falsifiable:true"],
    )
    add(
        "claim.graph", type="claim", title="Graph-first planning keeps decisions traceable",
        body=(
            "Falsifier: after six weeks, a reader cannot recover why an experiment exists from the graph, or the graph requires reading every node body to answer."
        ),
        tags=["status:active", "importance:p1", "topic:claim", "falsifiable:true"],
    )

    # --------------------------------------------------------------- evidence
    add(
        "ev.q1", type="evidence", title="Q1 fair benchmark — 390/390 cells",
        body=(
            "Three policies × five paired seeds × four scenario families. All 390 terminal cells completed. "
            "No comparison survives Holm correction at .05; the minimum adjusted p is .1875. Treat the result as descriptive."
        ),
        tags=["status:review", "importance:p0", "topic:evidence", "quality:accepted"],
        start_at="2026-08-25", end_at="2026-09-05",
    )
    add(
        "ev.q2", type="evidence", title="Q2 DAgger — 2/80 vs 80/80 survival",
        body=(
            "The corrected epoch-70 comparison completed with zero failed cells. DAgger survival was 2/80 versus 80/80 for BC; "
            "paired delta -0.975, seed-cluster 95% CI [-1.000, -0.938]. Negative result for the tested checkpoint."
        ),
        tags=["status:review", "importance:p0", "topic:evidence", "quality:negative"],
        start_at="2026-09-06", end_at="2026-09-12",
    )
    add(
        "ev.q3", type="evidence", title="Q3 sampler — 4.10× p99 speedup",
        body=(
            "DDIM-10/5/3 gave p99 speedups of 4.10× / 7.67× / 11.36× with survival 25/30, 24/30 and 25/30. "
            "The rolling variants failed 30/30. This is an efficiency result, not a capability result."
        ),
        tags=["status:review", "importance:p1", "topic:evidence", "quality:descriptive"],
        start_at="2026-09-05", end_at="2026-09-10",
    )
    add(
        "ev.q4", type="evidence", title="Q4 WBG pilot — partial",
        body=(
            "Two of four ablation arms are complete. The unstable-tracker arm is showing a semantic gain without a stability gain. "
            "Do not promote claim.wbg until the matched arms finish."
        ),
        tags=["status:active", "importance:p1", "topic:evidence", "quality:provisional"],
        start_at="2026-10-20", end_at="2026-11-01",
    )

    # ----------------------------------------------------------- experiments
    add(
        "exp.q1", type="experiment", title="Q1 · capability-matched fair benchmark",
        body="Protocol: frozen suite v1.2, 390 cells, five paired seeds, no model gets extra inference compute. Primary endpoint: push survival; secondary: text retrieval, WBG success, smoothness.",
        tags=["status:done", "importance:p0", "topic:experiment", "protocol:frozen", "outcome:mixed"],
        start_at="2026-08-25", end_at="2026-09-05",
    )
    add(
        "exp.q2", type="experiment", title="Q2 · DAgger checkpoint-matched rerun",
        body="Protocol: epoch-70 checkpoint, matched BC/DAgger data budgets, 100 reset-matched pairs. Primary endpoint: push survival.",
        tags=["status:done", "importance:p0", "topic:experiment", "protocol:frozen", "outcome:negative"],
        start_at="2026-09-06", end_at="2026-09-12",
    )
    add(
        "exp.q3", type="experiment", title="Q3 · sampler and schedule benchmark",
        body="Seven DDPM/DDIM arms, 30 command-seed strata, 210 provenance-v2 rollouts. Primary endpoint: p99 speedup; guardrail: survival.",
        tags=["status:done", "importance:p1", "topic:experiment", "protocol:frozen", "outcome:speedup"],
        start_at="2026-09-05", end_at="2026-09-10",
    )
    add(
        "exp.q4", type="experiment", title="Q4 · WBG guidance ablation",
        body="Four arms: stable/unstable tracker × guidance on/off. Primary endpoint: semantic success; secondary: tracking stability. Active on linux node gpu-server-1.",
        tags=["status:active", "importance:p0", "topic:experiment", "protocol:preregistered", "outcome:pending"],
        start_at="2026-10-01", end_at="2026-11-01",
    )
    add(
        "exp.agent", type="experiment", title="Agent-team pilot · evidence-gated handoffs",
        body="Compare a chat-only handoff with a graph handoff that names claim, artifact and gate. Measure lost context after six weeks. Active.",
        tags=["status:active", "importance:p2", "topic:agents", "outcome:pending"],
        start_at="2026-10-20", end_at="2026-11-10",
    )
    add(
        "exp.ark", type="experiment", title="ARK Studio · graph-first workbench pilot",
        body="Use ARK on PredActor for two weeks: capture, plan, map, task, produce. Measure orphan ideas, broken links and time-to-context. Active.",
        tags=["status:active", "importance:p1", "topic:workflow", "outcome:pending"],
        start_at="2026-10-28", end_at="2026-11-15",
    )

    # --------------------------------------------------------------- methods
    add(
        "method.prdp", type="method", title="PRDP · predictable diffusion policy",
        body="A diffusion policy over behavior-level intent, trained with episode-level semantic labels. Outputs a request that the tracker can accept, reject or recover from.",
        tags=["status:active", "importance:p0", "topic:method", "component:policy"],
    )
    add(
        "method.rlobs", type="method", title="RLOBS-DP · observation substitution",
        body="At inference, substitute the observation stream with a learned or retrieved stream when the tracker reports divergence. The policy is not retrained; the boundary is.",
        tags=["status:active", "importance:p1", "topic:method", "component:recovery"],
    )
    add(
        "method.kimodo", type="method", title="Kimodo → tracker behavior labels",
        body="Generate semantic behavior labels from a motion model, validate them against the tracker's physical envelope, then use the accepted labels as supervision.",
        tags=["status:active", "importance:p1", "topic:method", "component:data"],
    )
    add(
        "method.wbg", type="method", title="Whole-body guidance at inference",
        body="A guidance term that trades semantic success for stability. Must be reported as a two-axis trade-off, never as a single scalar win.",
        tags=["status:active", "importance:p1", "topic:method", "component:tracking"],
    )
    add(
        "method.ark", type="method", title="ARK · evidence-gated research graph",
        body="A single node graph per project; type/status/importance are reserved tags; every task names the claim it serves and the artifact it produces.",
        tags=["status:active", "importance:p1", "topic:method", "component:workflow"],
    )

    # ---------------------------------------------------------- related work
    related = [
        ("rw.dp", "Diffusion Policy", "Chi et al. · RSS 2023", "The reference point for action diffusion. Strong short-horizon stability; no explicit recovery boundary.", "venue:RSS", "year:2023"),
        ("rw.octo", "Octo · open generalist policy", "Team Octo · 2024", "A transformer policy trained across embodiments. Useful baseline for data scaling, not for tracker-boundary recovery.", "venue:arXiv", "year:2024"),
        ("rw.openvla", "OpenVLA · open vision-language-action", "Kim et al. · 2024", "Semantic generalization is strong; physical execution is delegated. The boundary is the relevant comparison.", "venue:arXiv", "year:2024"),
        ("rw.pi0", "π0 · flow matching for general robot control", "Physical Intelligence · 2024", "Flow-based action generation at scale. Raises the bar for data efficiency comparisons.", "venue:arXiv", "year:2024"),
        ("rw.humanoidbench", "HumanoidBench", "Sferra et al. · 2024", "A simulated humanoid benchmark. Useful for task coverage; not a substitute for closed-loop tracker evaluation.", "venue:arXiv", "year:2024"),
        ("rw.beyondmimic", "BeyondMimic", "2025", "Whole-body guidance and tracker-level stability. Motivates the Q4 trade-off study.", "venue:arXiv", "year:2025"),
        ("rw.labweft", "LabWeft research map", "Example lab · 2026", "A projection of research state. ARK borrows the map idea but keeps editing and task state in one store.", "venue:internal", "year:2026"),
    ]
    for key, title, who, body, venue, year in related:
        add(key, type="related_work", title=title, body=f"{who}\n\n{body}",
            tags=["status:open", "importance:p2", "topic:related", venue, year])

    # ---------------------------------------------------------------- sources
    add(
        "src.info", type="source", title="InfoHub · control reading group",
        body="Imported topic summary. 18 linked messages across 3 channels. The import kept the source key so the idea can be traced back.",
        tags=["status:open", "importance:p2", "system:infohub", "topic:intake"],
        origin="infohub", source_ref="infohub:topic/control-gap", source_title="InfoHub · control reading group",
    )
    add(
        "src.paper", type="source", title="InfoHub · paper feed",
        body="Imported paper feed item. Two related works were routed into the Related Work subtab.",
        tags=["status:open", "importance:p3", "system:infohub", "topic:intake"],
        origin="infohub", source_ref="infohub:topic/reading-flow", source_title="InfoHub · paper feed",
    )

    # ------------------------------------------------------------------ tasks
    tasks = [
        ("task.freeze", "Freeze the Q1 evaluation suite", "done", "p0", "2026-08-20", "2026-08-24",
         "Protocol v1.2 is frozen; 390 cells and five paired seeds are registered.", ["task:protocol"]),
        ("task.q1run", "Run the 390 Q1 terminal cells", "done", "p0", "2026-08-25", "2026-09-05",
         "All cells complete; analysis and figure contract accepted.", ["task:experiment"]),
        ("task.q2review", "Review the Q2 negative-result memo", "done", "p1", "2026-09-10", "2026-09-12",
         "Negative result accepted for the tested checkpoint; no general DAgger claim.", ["task:review"]),
        ("task.dagger", "Rerun DAgger with matched checkpoints", "blocked", "p0", "2026-10-20", "2026-10-27",
         "Blocked: the epoch-70 checkpoint hash does not match the frozen source gate. Need a clean re-export.", ["task:experiment", "risk:overdue"]),
        ("task.q4", "Run the Q4 WBG ablation on gpu-server-1", "active", "p0", "2026-10-01", "2026-11-02",
         "Two of four arms complete. Do not promote claim.wbg before the unstable arm finishes.", ["task:experiment", "gpu:4x4090"]),
        ("task.intro", "Draft the PredActor introduction", "review", "p1", "2026-10-26", "2026-10-30",
         "One page, three paragraphs: motivation, gap, contribution. Needs a clearer falsifier sentence.", ["task:writing"]),
        ("task.figs", "Build the paper figure suite v3", "active", "p1", "2026-10-28", "2026-11-08",
         "Figure 1 is the mind graph; Figure 2 is the fairness table; Figure 3 is the Q2 negative result.", ["task:figure"]),
        ("task.teaser", "Render the 76.5s teaser", "done", "p2", "2026-10-01", "2026-10-06",
         "76.5s, 1080p30, music-only preview. User review pending before publication.", ["task:video"]),
        ("task.ark", "Implement the ARK Studio workbench", "active", "p0", "2026-10-28", "2026-11-05",
         "Node/tag store, tabs+subtabs, list/graph/gantt, import, agent API. This is the active build.", ["task:build"]),
        ("task.import", "Import InfoHub topics into ARK", "review", "p1", "2026-10-28", "2026-10-31",
         "Two topic groups imported with source keys preserved. Need to decide the default kind mapping.", ["task:data"]),
        ("task.contract", "Define the agent run contract", "active", "p0", "2026-10-29", "2026-11-03",
         "Every dispatch must carry: task uid, claim, gate, host, budget, artifact path, stop condition.", ["task:agents"]),
        ("task.release", "Anonymous release audit", "inbox", "p1", "2026-11-03", "2026-11-05",
         "Check LFS paths, anonymous access, checksums and attribution before any upload.", ["task:release", "risk:external"]),
        ("task.camera", "Camera-ready checklist", "inbox", "p2", "2026-11-18", "2026-11-20",
         "Author order, acknowledgements, data statement, compute statement, licence.", ["task:release"]),
        ("task.story", "Shape the story spine", "done", "p0", "2026-10-01", "2026-10-05",
         "Seven sections, each linked to the claim or experiment it carries.", ["task:writing"]),
    ]
    for key, title, status, imp, start, end, body, tags in tasks:
        add(key, type="task", title=title, body=body,
            tags=["status:" + status, "importance:" + imp, "stage:task", *tags],
            start_at=start, end_at=end)

    # ----------------------------------------------------------------- agents
    add("agent.atlas", type="agent", title="Atlas · architect",
        body="Owns the question, the claim graph and the task contract. Refuses to dispatch a task without a falsifier and an artifact path.",
        tags=["status:active", "importance:p0", "role:manager", "model:reasoner", "host:workstation"])
    add("agent.probe", type="agent", title="Probe · experiment runner",
        body="Owns benchmark execution and provenance. Runs only frozen protocols; reports failures instead of retrying silently.",
        tags=["status:active", "importance:p0", "role:runner", "model:coder", "host:gpu-server-1"])
    add("agent.quill", type="agent", title="Quill · paper and visuals",
        body="Owns the story spine, figures and release copy. Every figure must name the claim it supports.",
        tags=["status:open", "importance:p1", "role:writer", "model:writer", "host:workstation"])
    add("agent.scout", type="agent", title="Scout · literature",
        body="Owns related work and prior-art checks. Adds citations with venue, year and the claim they touch.",
        tags=["status:inbox", "importance:p2", "role:scout", "model:reasoner", "host:workstation"])

    add("host.workstation", type="host", title="Lab workstation",
        body="Windows 11 · RTX 4090 24 GB · 64 GB RAM. Interactive work, figure rendering and the ARK server.",
        tags=["status:active", "importance:p1", "os:windows", "gpu:rtx-4090", "device:workstation"])
    add("host.gpu-server-1", type="host", title="Linux node gpu-server-1",
        body="Ubuntu 22.04 · 4× RTX 4090 · 256 GB RAM. Benchmark execution. SSH details are not stored in this demo.",
        tags=["status:active", "importance:p0", "os:linux", "gpu:4x4090", "device:gpu-node"])

    # ------------------------------------------------------------- production
    add("prod.system", type="production", title="PredActor visual system",
        body="Dense scientific layout. Blue is structure, amber is a pending decision, green is accepted evidence. No decoration without a data job.",
        tags=["status:active", "importance:p1", "role:palette", "color:#2563eb", "color:#f59e0b",
              "color:#0f172a", "color:#34d399", "font:inter", "font:source-serif-4"])
    add("prod.figs", type="production", title="Figure suite v3",
        body="Figure 1 mind graph, Figure 2 fairness matrix, Figure 3 Q2 negative result, Figure 4 Q3 speedup. Each figure links to its evidence node.",
        tags=["status:active", "importance:p0", "role:figure", "version:v3"])
    add("prod.teaser", type="production", title="Teaser · 76.5 s",
        body="1080p30, music-only review cut. No publication before the release audit.",
        tags=["status:review", "importance:p2", "role:video", "version:v015"])
    add("prod.page", type="production", title="Project page",
        body="Bilingual landing page: one-sentence claim, three figures, two demos, paper link, no press-release language.",
        tags=["status:review", "importance:p2", "role:web", "audience:public"])
    add("prod.deck", type="production", title="Lab talk deck",
        body="12 slides. The negative result gets its own slide; do not hide it in backup.",
        tags=["status:inbox", "importance:p2", "role:slides", "audience:lab"])

    # --------------------------------------------------------------- outreach
    add("out.x", type="outreach", title="X thread · predictable vs deployable",
        body="Five posts: claim, gap, one figure, negative result, link. Do not claim a global winner.",
        tags=["status:open", "importance:p2", "channel:x", "audience:research"],
        start_at="2026-11-12", end_at="2026-11-12")
    add("out.wechat", type="outreach", title="WeChat long-form summary",
        body="Chinese summary for the robotics community. Explain the boundary, not the leaderboard.",
        tags=["status:inbox", "importance:p2", "channel:wechat", "audience:zh"],
        start_at="2026-11-14", end_at="2026-11-14")
    add("out.talk", type="outreach", title="Shenlan lab talk",
        body="30 minutes + 10 minutes questions. Include the Q2 negative result and the ARK workflow.",
        tags=["status:open", "importance:p2", "channel:talk", "audience:lab"],
        start_at="2026-11-20", end_at="2026-11-20")
    add("out.arxiv", type="outreach", title="arXiv + project page launch",
        body="Blocked on camera-ready, release audit and figure freeze. No upload without explicit approval.",
        tags=["status:inbox", "importance:p0", "channel:web", "audience:public", "risk:external"],
        start_at="2026-11-25", end_at="2026-11-25")

    # -------------------------------------------------------------- milestones
    add("ms.q4", type="milestone", title="Q4 results frozen",
        body="All four arms complete, analysis sealed, claim status updated by evidence.",
        tags=["status:open", "importance:p0", "stage:milestone"],
        start_at="2026-11-01", end_at="2026-11-01")
    add("ms.draft", type="milestone", title="Full draft complete",
        body="Story spine, all figures, related work and limitations are in one document.",
        tags=["status:open", "importance:p0", "stage:milestone"],
        start_at="2026-11-15", end_at="2026-11-15")
    add("ms.submit", type="milestone", title="Submission",
        body="Camera-ready package passes the release audit. Submission is an external action and needs approval.",
        tags=["status:open", "importance:p0", "stage:milestone"],
        start_at="2026-11-25", end_at="2026-11-25")

    # ------------------------------------------------------- artifacts/resources
    add("art.mind", type="artifact", title="Mind graph · workbench view",
        body="The Xmind-style left-to-right logic tree, rendered from this project's hierarchy. This screenshot doubles as the demo figure.",
        tags=["status:done", "importance:p1", "kind:figure", "format:png"],
        media=[{"url": mind, "kind": "figure", "caption": "ARK Studio mind graph"}] if mind else [])
    add("art.team", type="artifact", title="Workbench overview · demo",
        body="The overview dashboard: pipeline, health, next actions, activity and graph preview. This screenshot doubles as the demo overview asset.",
        tags=["status:done", "importance:p2", "kind:figure", "format:png"],
        media=[{"url": overview, "kind": "figure", "caption": "ARK Studio overview"}] if overview else [])
    add("art.q2", type="artifact", title="Q2 negative-result memo",
        body="Terminal report with 200 cells, 100 reset-matched pairs, zero failed cells. Durable path: outputs/Q2-DAGGER/final_report.md.",
        tags=["status:done", "importance:p0", "kind:report", "quality:accepted"])
    add("res.infohub", type="resource", title="InfoHub dataset",
        body="SQLite hub.db on the lab workstation. Read-only import source; never write back.",
        tags=["status:active", "importance:p1", "system:infohub", "access:read-only"])
    add("res.labweft", type="resource", title="LabWeft research map",
        body="A projection of research state. ARK can export to it; it does not own task state.",
        tags=["status:open", "importance:p2", "system:labweft", "access:read-only"])
    add("res.workstation", type="resource", title="Lab long-term store",
        body="Final materials, resources and notes live in the lab store. ARK stores identities and hyperlinks, not the bytes.",
        tags=["status:active", "importance:p1", "system:workstation", "access:read-only"])

    add("dec.q1", type="decision", title="Freeze Q1 protocol v1.2",
        body="Decision: no protocol changes after the first terminal cell. Approved by the user; owner is the manager.",
        tags=["status:done", "importance:p0", "gate:protocol"],
        start_at="2026-08-24", end_at="2026-08-24")

    # ------------------------------------------------------------------ links
    for key, _, _, _ in [
        ("story.motivation", "", "", ""), ("story.gap", "", "", ""), ("story.insight", "", "", ""),
        ("story.contribution", "", "", ""), ("story.method", "", "", ""),
        ("story.evidence", "", "", ""), ("story.limits", "", "", ""),
    ]:
        link(key, "story.root", "part_of")

    link("story.motivation", "q.control", "supports")
    link("story.gap", "idea.predictable", "derived_from")
    link("story.insight", "idea.separate", "derived_from")
    link("story.contribution", "claim.prdp", "targets")
    link("story.method", "method.prdp", "uses")
    link("story.method", "method.rlobs", "uses")
    link("story.evidence", "ev.q1", "supports")
    link("story.limits", "claim.dagger", "relates_to")

    link("idea.predictable", "src.info", "derived_from")
    link("idea.predictable", "q.control", "inspires")
    link("idea.predictable", "idea.separate", "inspires")
    link("idea.separate", "idea.predictable", "derived_from")
    link("idea.separate", "claim.prdp", "inspires")
    link("idea.separate", "method.prdp", "inspires")
    link("idea.kimodo", "method.kimodo", "inspires")
    link("idea.gates", "method.ark", "inspires")
    link("idea.gates", "claim.graph", "supports")
    link("idea.info", "src.info", "derived_from")
    link("idea.info", "q.graph", "supports")
    link("idea.scale", "claim.dagger", "supports")
    link("idea.scale", "task.dagger", "inspires")

    link("claim.prdp", "q.control", "targets")
    link("claim.rlobs", "q.shift", "targets")
    link("claim.long", "q.control", "targets")
    link("claim.dagger", "q.shift", "targets")
    link("claim.wbg", "q.wbg", "targets")
    link("claim.graph", "q.graph", "targets")

    link("ev.q1", "claim.prdp", "supports")
    link("ev.q2", "claim.dagger", "supports")
    link("ev.q2", "claim.long", "contradicts")
    link("ev.q3", "claim.long", "supports")
    link("ev.q4", "claim.wbg", "supports")
    link("exp.q1", "claim.prdp", "validates")
    link("exp.q2", "claim.dagger", "validates")
    link("exp.q3", "claim.long", "validates")
    link("exp.q4", "claim.wbg", "validates")
    link("exp.agent", "claim.graph", "validates")
    link("exp.ark", "claim.graph", "validates")

    link("exp.q1", "method.prdp", "uses")
    link("exp.q2", "method.rlobs", "uses")
    link("exp.q3", "method.prdp", "uses")
    link("exp.q4", "method.wbg", "uses")
    link("exp.agent", "method.ark", "uses")
    link("exp.ark", "method.ark", "uses")
    link("method.prdp", "idea.separate", "derived_from")
    link("method.rlobs", "idea.separate", "derived_from")
    link("method.kimodo", "idea.kimodo", "derived_from")
    link("method.ark", "idea.gates", "derived_from")

    link("story.gap", "rw.dp", "cites")
    link("story.gap", "rw.octo", "cites")
    link("story.gap", "rw.openvla", "cites")
    link("story.gap", "rw.pi0", "cites")
    link("story.gap", "rw.humanoidbench", "cites")
    link("story.contribution", "rw.beyondmimic", "cites")
    link("method.ark", "rw.labweft", "derived_from")
    link("rw.dp", "method.prdp", "inspires")
    link("rw.octo", "method.rlobs", "inspires")
    link("rw.beyondmimic", "method.wbg", "inspires")

    for task_key, exp_key in [
        ("task.freeze", "exp.q1"), ("task.q1run", "exp.q1"), ("task.dagger", "exp.q2"),
        ("task.q4", "exp.q4"), ("task.ark", "exp.ark"), ("task.contract", "exp.agent"),
        ("task.story", "story.root"),
    ]:
        link(task_key, exp_key, "part_of")

    for task_key, agent_key in [
        ("task.freeze", "agent.atlas"), ("task.q1run", "agent.probe"), ("task.dagger", "agent.probe"),
        ("task.q4", "agent.probe"), ("task.intro", "agent.quill"), ("task.figs", "agent.quill"),
        ("task.teaser", "agent.quill"), ("task.ark", "agent.atlas"), ("task.import", "agent.atlas"),
        ("task.contract", "agent.atlas"), ("task.release", "agent.probe"), ("task.camera", "agent.quill"),
        ("task.story", "agent.atlas"),
    ]:
        link(task_key, agent_key, "assigned_to")

    for task_key, host_key in [
        ("task.q4", "host.gpu-server-1"), ("task.dagger", "host.gpu-server-1"), ("task.q1run", "host.gpu-server-1"),
        ("task.figs", "host.workstation"), ("task.ark", "host.workstation"), ("task.contract", "host.gpu-server-1"),
    ]:
        link(task_key, host_key, "runs_on")

    link("agent.atlas", "host.workstation", "runs_on")
    link("agent.probe", "host.gpu-server-1", "runs_on")
    link("agent.quill", "host.workstation", "runs_on")
    link("agent.scout", "host.workstation", "runs_on")

    link("task.dagger", "claim.dagger", "targets")
    link("task.q4", "claim.wbg", "targets")
    link("task.ark", "claim.graph", "targets")
    link("task.q1run", "ev.q1", "produces")
    link("task.q2review", "art.q2", "produces")
    link("task.dagger", "task.freeze", "depends_on")
    link("task.release", "task.camera", "depends_on")
    link("task.camera", "ms.draft", "depends_on")
    link("task.figs", "prod.figs", "produces")
    link("task.teaser", "prod.teaser", "produces")
    link("task.story", "story.root", "produces")
    link("task.ark", "method.ark", "produces")
    link("prod.figs", "ev.q1", "supports")
    link("prod.figs", "ev.q2", "supports")
    link("prod.page", "claim.prdp", "supports")
    link("prod.teaser", "story.root", "supports")
    link("prod.system", "prod.figs", "part_of")
    link("prod.system", "prod.page", "part_of")
    link("prod.deck", "story.root", "derived_from")

    link("out.x", "prod.page", "depends_on")
    link("out.wechat", "prod.page", "depends_on")
    link("out.talk", "prod.deck", "depends_on")
    link("out.arxiv", "ms.submit", "depends_on")
    link("out.arxiv", "task.release", "depends_on")
    link("out.x", "claim.prdp", "targets")
    link("out.wechat", "story.root", "targets")

    link("ms.q4", "exp.q4", "depends_on")
    link("ms.draft", "story.root", "depends_on")
    link("ms.submit", "ms.draft", "depends_on")
    link("ms.q4", "task.q4", "depends_on")
    link("dec.q1", "exp.q1", "supports")
    link("exp.q1", "res.workstation", "uses")
    link("exp.q2", "res.workstation", "uses")
    link("exp.ark", "res.labweft", "uses")
    link("src.info", "idea.predictable", "inspires")
    link("src.paper", "idea.info", "inspires")
    link("art.mind", "story.root", "supports")
    link("art.team", "method.ark", "supports")

    # A deliberate orphan for the assistant's "unlinked idea" insight.
    # (No links target or leave idea.replan.)
