"""Writes claude_stream_subagents.jsonl: a Claude Code stream-json run with the
event shapes seen in real sessions (sub-agents with nested messages, Bash
tasks in the foreground and background, task progress and notifications,
bookkeeping events, a line cut short by the log limit).  Run from this folder."""
import json

S = "4e74eeb4-sess"
n = [0]
L = []


def add(o, raw=None):
    n[0] += 1
    o.setdefault("session_id", S)
    o.setdefault("uuid", f"uuid-{n[0]}")
    L.append(raw if raw is not None else json.dumps(o, separators=(",", ":")))


def sysev(sub, **kw):
    add({"type": "system", "subtype": sub, **kw})


def asst(content, parent=None, usage=None):
    msg = {"model": "claude-opus-4-1", "content": content}
    if usage:
        msg["usage"] = usage
    add({"type": "assistant", "message": msg, "parent_tool_use_id": parent})


def user(content, parent=None, result=None):
    o = {"type": "user", "message": {"role": "user", "content": content}, "parent_tool_use_id": parent}
    if result is not None:
        o["tool_use_result"] = result
    add(o)


sysev("init", cwd="/w", model="claude-opus-4-1", tools=["Task", "Bash"])
sysev("thinking_tokens", estimated_tokens=50, estimated_tokens_delta=50)
asst([{"type": "thinking", "thinking": "Plan the work."}])
asst([{"type": "text", "text": "I'll split this into two sub-agents."},
      {"type": "tool_use", "id": "toolu_A", "name": "Task", "input": {"subagent_type": "Explore", "description": "Survey session code", "prompt": "Find the session modules"}},
      {"type": "tool_use", "id": "toolu_B", "name": "Agent", "input": {"subagent_type": "general-purpose", "description": "Write tests", "prompt": "Write tests", "model": "sonnet"}}])
sysev("task_started", task_id="a111", tool_use_id="toolu_A", description="Survey session code", task_type="local_agent", is_backgrounded=False)
sysev("task_started", task_id="a222", tool_use_id="toolu_B", description="Write tests", task_type="local_agent", is_backgrounded=False)
user([{"type": "text", "text": "Find the session modules"}], parent="toolu_A")
asst([{"type": "thinking", "thinking": "grep first"}], parent="toolu_A")
asst([{"type": "tool_use", "id": "toolu_A1", "name": "Grep", "input": {"pattern": "session"}}], parent="toolu_A", usage={"input_tokens": 1200, "output_tokens": 30})
sysev("permission_check_status", tool_use_id="toolu_A1", status="checking")
sysev("task_progress", task_id="a111", tool_use_id="toolu_A", description="Searching for session", subagent_type="Explore",
      usage={"total_tokens": 1230, "tool_uses": 1, "duration_ms": 900}, last_tool_name="Grep")
add({"type": "tool_progress", "tool_use_id": "toolu_A1", "parent_tool_use_id": "toolu_A", "elapsed_time_seconds": 1})
user([{"type": "tool_result", "tool_use_id": "toolu_A1", "content": "js/session-model.js"}], parent="toolu_A")
asst([{"type": "text", "text": "Found three modules.\nsession-model.js holds the helpers."}], parent="toolu_A", usage={"input_tokens": 1300, "output_tokens": 40})
user([{"type": "tool_result", "tool_use_id": "toolu_A", "content": [{"type": "text", "text": "Three modules: session-model.js, session-workspace.js, session-dock.js"}]}],
     result={"status": "completed", "agentId": "a111", "totalTokens": 2570, "totalDurationMs": 42000, "totalToolUseCount": 1})
sysev("task_notification", task_id="a111", tool_use_id="toolu_A", status="completed", summary="Survey session code")
# A foreground Bash command: Claude Code tracks it as a task too.
asst([{"type": "tool_use", "id": "toolu_G", "name": "Bash", "input": {"command": "cd /tmp/rua_a && for r in 1 2; do echo $r; done", "description": "Loop"}}])
sysev("task_started", task_id="b001", tool_use_id="toolu_G", description="Loop", task_type="local_bash", is_backgrounded=False)
user([{"type": "tool_result", "tool_use_id": "toolu_G", "content": "1\n2"}])
sysev("task_notification", task_id="b001", tool_use_id="toolu_G", status="completed", summary="Loop")
# Background commands.
asst([{"type": "tool_use", "id": "toolu_C", "name": "Bash", "input": {"command": "npm run dev", "description": "Start dev server", "run_in_background": True}}])
sysev("task_started", task_id="bash_1", tool_use_id="toolu_C", description="Start dev server", task_type="local_bash", is_backgrounded=True)
sysev("background_tasks_changed", tasks=[{"task_id": "bash_1", "task_type": "local_bash", "description": "Start dev server"}])
user([{"type": "tool_result", "tool_use_id": "toolu_C", "content": "Command running in background with ID: bash_1"}], result={"backgroundTaskId": "bash_1"})
asst([{"type": "tool_use", "id": "toolu_D", "name": "Bash", "input": {"command": "pytest -q", "description": "Run tests"}}])
sysev("task_started", task_id="bash_2", tool_use_id="toolu_D", description="Run tests", task_type="local_bash", is_backgrounded=False)
sysev("task_updated", task_id="bash_2", patch={"is_backgrounded": True})
user([{"type": "tool_result", "tool_use_id": "toolu_D", "content": "Command was moved to the background with ID: bash_2."}])
# A long tool result cut short by the log line limit.
long = json.dumps({"type": "user", "message": {"role": "user", "content": [{"tool_use_id": "toolu_H", "type": "tool_result", "content": "The reward is 1 if " + "x" * 300}]}, "session_id": S, "uuid": "u-long"}, separators=(",", ":"))
asst([{"type": "tool_use", "id": "toolu_H", "name": "Read", "input": {"file_path": "reward.md"}}])
add({}, raw=long[:200] + " …[truncated]")
sysev("task_notification", task_id="bash_2", tool_use_id="toolu_D", status="completed", summary='Background command "Run tests" completed (exit code 0)')
asst([{"type": "tool_use", "id": "toolu_E", "name": "KillShell", "input": {"shell_id": "bash_1"}}])
user([{"type": "tool_result", "tool_use_id": "toolu_E", "content": "Successfully killed shell: bash_1 (npm run dev)"}])
asst([{"type": "tool_use", "id": "toolu_F", "name": "Task", "input": {"subagent_type": "general-purpose", "description": "Long audit", "prompt": "Audit", "run_in_background": True}}])
user([{"type": "tool_result", "tool_use_id": "toolu_F", "content": "Async agent launched successfully.\nagentId: agent_77"}], result={"isAsync": True, "agentId": "agent_77", "status": "async_launched"})
add({"type": "rate_limit_event", "rate_limit_info": {"status": "allowed"}})
user('<task-notification>\n<task-id>agent_77</task-id>\n<tool-use-id>toolu_F</tool-use-id>\n<status>failed</status>\n<summary>Agent "Long audit" failed: timeout</summary>\n</task-notification>')
asst([{"type": "text", "text": "All done: tests pass."}])
add({"type": "result", "subtype": "success", "result": "All done: tests pass.", "usage": {"input_tokens": 5000, "output_tokens": 300}})
with open("claude_stream_subagents.jsonl", "w", encoding="utf-8", newline="\n") as f:
    f.write("\n".join(L) + "\n")
print(len(L))
