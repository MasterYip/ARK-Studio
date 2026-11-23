"""Writes the Codex `exec --json` fixtures of transcript_codex_test.go.

codex_exec_read.jsonl and codex_exec_write.jsonl are real runs of codex-cli
0.169 on Windows (2026-11-23; user paths anonymised, one final message
shortened).  Run from this folder with the raw captures as arguments to
refresh them:

    python gen_codex_fixtures.py [read.jsonl write.jsonl stderr.txt]

Without arguments only the synthetic fixtures are rewritten:
codex_exec_synthetic.jsonl (escape sequences, redrawn progress lines,
reasoning, a plan, an MCP call, streamed message updates, a reconnect and a
failed turn) and claude_stream_equivalent.jsonl (the Claude Code stream-json
of the same kind of work, for the block comparison).
"""
import json
import re
import sys

ESC = "\x1b"
BEL = "\x07"


def anon(text):
    """User folders become C:\\Users\\me\\ and the capture folder D:\\work (any escaping)."""
    text = re.sub(r"(C:(?:\\)+Users(?:\\)+)[^\\\"/]+", lambda m: m.group(1) + "me", text)
    return re.sub(r"[A-Z]:((?:\\)+)[^\"]*?codexcap\d*", lambda m: "D:" + m.group(1) + "work", text)


def dump(name, rows):
    with open(name, "w", encoding="utf-8", newline="\n") as fh:
        for row in rows:
            fh.write((row if isinstance(row, str) else json.dumps(row, ensure_ascii=False, separators=(",", ":"))) + "\n")


def real(read, write, stderr):
    rows = []
    for line in open(read, encoding="utf-8"):
        obj = json.loads(anon(line))
        if obj.get("item", {}).get("id") == "item_4":
            obj["item"]["text"] = "文件里写的是“你好，世界 ARK”。\n\n- 内容包含问候语“你好，世界”\n- 末尾是“ARK”"
        rows.append(obj)
    dump("codex_exec_read.jsonl", rows)
    dump("codex_exec_write.jsonl", [json.loads(anon(line)) for line in open(write, encoding="utf-8")])
    dump("codex_exec_stderr.txt", [anon(line.rstrip("\n")) for line in open(stderr, encoding="utf-8")][:4])


PYTEST = ESC + "]0;pytest" + BEL + "collecting ...\r" + ESC + "[2K" + "进度 10%\r进度 100%\n"
SYNTHETIC = [
    {"type": "thread.started", "thread_id": "01a2-0000-synthetic"},
    {"type": "turn.started"},
    {"type": "item.started", "item": {"id": "item_0", "type": "reasoning", "text": "**检查** 测试目录"}},
    {"type": "item.completed", "item": {"id": "item_0", "type": "reasoning", "text": "**检查** 测试目录和失败用例"}},
    {"type": "item.started", "item": {"id": "item_1", "type": "todo_list", "items": [
        {"text": "运行测试", "completed": False}, {"text": "修复失败", "completed": False}]}},
    {"type": "item.started", "item": {"id": "item_2", "type": "command_execution", "command": "/bin/bash -lc 'pytest -q'",
                                      "aggregated_output": "", "exit_code": None, "status": "in_progress"}},
    {"type": "item.updated", "item": {"id": "item_2", "type": "command_execution", "command": "/bin/bash -lc 'pytest -q'",
                                      "aggregated_output": PYTEST, "exit_code": None, "status": "in_progress"}},
    {"type": "item.completed", "item": {"id": "item_2", "type": "command_execution", "command": "/bin/bash -lc 'pytest -q'",
                                        "aggregated_output": PYTEST + ESC + "[31mFAILED" + ESC + "[0m tests/test_中文.py::test_x - 断言失败\r\n"
                                        + ESC + "[1m1 failed" + ESC + "[0m in 0.12s\n", "exit_code": 1, "status": "failed"}},
    {"type": "item.updated", "item": {"id": "item_1", "type": "todo_list", "items": [
        {"text": "运行测试", "completed": True}, {"text": "修复失败", "completed": False}]}},
    {"type": "item.started", "item": {"id": "item_3", "type": "file_change", "changes": [
        {"path": "tests/test_中文.py", "kind": "update"}, {"path": "src/new.py", "kind": "add"}], "status": "in_progress"}},
    {"type": "item.completed", "item": {"id": "item_3", "type": "file_change", "changes": [
        {"path": "tests/test_中文.py", "kind": "update"}, {"path": "src/new.py", "kind": "add"}], "status": "completed"}},
    {"type": "item.started", "item": {"id": "item_4", "type": "mcp_tool_call", "server": "ark", "tool": "get_unit",
                                      "arguments": {"uid": "T-0001"}, "status": "in_progress"}},
    {"type": "item.completed", "item": {"id": "item_4", "type": "mcp_tool_call", "server": "ark", "tool": "get_unit",
                                        "arguments": {"uid": "T-0001"},
                                        "result": {"content": [{"type": "text", "text": "任务：整理日志"}]}, "status": "completed"}},
    {"type": "item.started", "item": {"id": "item_5", "type": "agent_message", "text": "已修复"}},
    {"type": "item.updated", "item": {"id": "item_5", "type": "agent_message", "text": "已修复 `test_x`："}},
    {"type": "item.completed", "item": {"id": "item_5", "type": "agent_message",
                                        "text": "已修复 `test_x`：\n\n- 断言改为比较 **中文** 字符串\n- 新增 `src/new.py`"}},
    {"type": "error", "message": "Reconnecting... 1/5"},
    {"type": "turn.completed", "usage": {"input_tokens": 1200, "cached_input_tokens": 800, "output_tokens": 90,
                                         "reasoning_output_tokens": 30}},
    {"type": "turn.failed", "error": {"message": "stream disconnected before completion: 连接被重置"}},
]

CLAUDE = [
    {"type": "system", "subtype": "init", "session_id": "c-1", "uuid": "u0"},
    {"type": "assistant", "session_id": "c-1", "uuid": "u1", "message": {"content": [{"type": "text", "text": "我先读取 `hello.txt`。"}]}},
    {"type": "assistant", "session_id": "c-1", "uuid": "u2", "message": {"content": [
        {"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": {"command": "cat hello.txt"}}]}},
    {"type": "user", "session_id": "c-1", "uuid": "u3", "message": {"content": [
        {"type": "tool_result", "tool_use_id": "toolu_1", "content": "你好，世界 ARK\n"}]}},
    {"type": "assistant", "session_id": "c-1", "uuid": "u4", "message": {"content": [
        {"type": "tool_use", "id": "toolu_2", "name": "Write", "input": {"file_path": "notes.md", "content": "# 会话测试 中文\n"}}]}},
    {"type": "user", "session_id": "c-1", "uuid": "u5", "message": {"content": [
        {"type": "tool_result", "tool_use_id": "toolu_2", "content": "File created"}]}},
    {"type": "assistant", "session_id": "c-1", "uuid": "u6", "message": {"content": [
        {"type": "text", "text": "文件里写的是“你好，世界 ARK”。\n\n- 已创建 `notes.md`"}]}},
]

if __name__ == "__main__":
    if len(sys.argv) == 4:
        real(*sys.argv[1:])
    dump("codex_exec_synthetic.jsonl", SYNTHETIC)
    dump("claude_stream_equivalent.jsonl", CLAUDE)
