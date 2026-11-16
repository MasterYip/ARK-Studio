import json
import os
import tempfile
import unittest
from pathlib import Path

from arkstudio.session_transcript import (
    build_permission_response,
    build_transcript,
    events_from_line,
    index_workspace,
    language_for,
    permission_request_from_line,
)


def entry(line, at="2026-10-31T00:00:00+00:00", kind=""):
    item = {"t": at, "line": line}
    if kind:
        item["kind"] = kind
    return item


class TranscriptParserTests(unittest.TestCase):
    def test_prompt_text_and_input_are_ordered(self):
        data = build_transcript(
            session_id="s_1",
            status="running",
            prompt="Do the task",
            prompt_at="2026-10-31T00:00:00+00:00",
            stdout=[
                entry("first line", "2026-10-31T00:00:01+00:00"),
                entry("second line", "2026-10-31T00:00:02+00:00"),
                entry("› follow up", "2026-10-31T00:00:03+00:00", kind="input"),
            ],
        )
        self.assertEqual([item["type"] for item in data["items"]], ["message", "message", "message"])
        self.assertEqual(data["items"][0]["role"], "user")
        self.assertEqual(data["items"][0]["text"], "Do the task")
        self.assertEqual(data["items"][1]["role"], "assistant")
        self.assertEqual(data["items"][1]["text"], "first line\nsecond line")
        self.assertEqual(data["items"][2]["role"], "user")
        self.assertEqual(data["items"][2]["text"], "follow up")
        self.assertFalse(data["items"][1].get("streaming", False))
        self.assertEqual(data["stats"]["tools"], 0)

    def test_dsh_reasoning_stderr_is_a_streaming_notice(self):
        stderr = [
            entry("dsh: reasoning:", "2026-10-31T00:00:01+00:00"),
            entry("Need inspect the paper first.", "2026-10-31T00:00:02+00:00"),
            entry("Then download it.", "2026-10-31T00:00:03+00:00"),
        ]
        data = build_transcript(session_id="s_reason", status="running",
                                prompt="go", stderr=stderr)
        reasoning = [item for item in data["items"] if item.get("level") == "reasoning"]
        self.assertEqual(len(reasoning), 1)
        self.assertIn("Need inspect the paper first.", reasoning[0]["text"])
        self.assertIn("Then download it.", reasoning[0]["text"])
        self.assertTrue(reasoning[0].get("streaming"))
        stopped = build_transcript(session_id="s_reason", status="stopped",
                                   prompt="go", stderr=stderr)
        self.assertFalse([item for item in stopped["items"] if item.get("level") == "reasoning"][0].get("streaming", False))

    def test_last_assistant_message_is_marked_streaming_while_active(self):
        data = build_transcript(session_id="s_live", status="running", prompt="go",
                                stdout=[entry("still writing")])
        self.assertTrue(data["items"][-1]["streaming"])
        stopped = build_transcript(session_id="s_live", status="stopped", prompt="go",
                                   stdout=[entry("done")])
        self.assertFalse(stopped["items"][-1].get("streaming", False))

    def test_ark_tool_events_pair_and_render_error(self):
        stdout = [
            entry("[[ark-event]] " + json.dumps({
                "type": "tool_call", "id": "c1", "name": "search",
                "input": {"q": "scaling"},
            })),
            entry("[[ark-event]] " + json.dumps({
                "type": "tool_result", "id": "c1", "status": "ok",
                "output": {"hits": 3},
            })),
        ]
        stderr = [entry("boom", "2026-10-31T00:00:02+00:00")]
        data = build_transcript(session_id="s_2", status="stopped",
                                prompt="look", stdout=stdout, stderr=stderr)
        tool = next(item for item in data["items"] if item["type"] == "tool")
        self.assertEqual(tool["name"], "search")
        self.assertEqual(tool["input"], {"q": "scaling"})
        self.assertEqual(tool["output"], {"hits": 3})
        self.assertEqual(tool["status"], "ok")
        self.assertTrue(any(item["type"] == "error" and item["text"] == "boom"
                            for item in data["items"]))
        self.assertFalse(data["active"])

    def test_unpaired_tool_result_keeps_output(self):
        line = "[[ark-event]] " + json.dumps({
            "type": "tool_result", "id": "missing", "name": "write_file",
            "status": "error", "output": "denied",
        })
        data = build_transcript(session_id="s_3", status="errored", stdout=[entry(line)])
        tool = next(item for item in data["items"] if item["type"] == "tool")
        self.assertEqual(tool["status"], "error")
        self.assertEqual(tool["output"], "denied")

    def test_codex_jsonl_is_mapped_to_structured_events(self):
        lines = [
            json.dumps({"type": "item.completed", "item": {
                "id": "i1", "type": "agent_message", "text": "I will inspect it.",
            }}),
            json.dumps({"type": "item.completed", "item": {
                "id": "i2", "type": "command_execution", "command": "ls",
                "aggregated_output": "a.txt", "exit_code": 0,
            }}),
            json.dumps({"type": "item.completed", "item": {
                "id": "i3", "type": "command_execution", "command": "false",
                "aggregated_output": "", "exit_code": 1,
            }}),
        ]
        data = build_transcript(session_id="s_4", status="stopped",
                                stdout=[entry(line, f"2026-10-31T00:00:0{i}+00:00")
                                        for i, line in enumerate(lines)])
        tools = [item for item in data["items"] if item["type"] == "tool"]
        self.assertEqual(len(tools), 2)
        self.assertEqual(tools[0]["name"], "command")
        self.assertEqual(tools[0]["status"], "ok")
        self.assertEqual(tools[1]["status"], "error")
        self.assertTrue(any(item["type"] == "message" and "inspect" in item["text"]
                            for item in data["items"]))

    def test_codex_human_stderr_is_transcript_not_process_error(self):
        stderr = [
            entry("OpenAI Codex v0.146.0", "2026-10-31T00:00:01+00:00"),
            entry("--------", "2026-10-31T00:00:02+00:00"),
            entry("workdir: /tmp/project", "2026-10-31T00:00:03+00:00"),
            entry("model: gpt-6-astra", "2026-10-31T00:00:04+00:00"),
            entry("user", "2026-10-31T00:00:05+00:00"),
            entry("Do the task", "2026-10-31T00:00:06+00:00"),
            entry("codex", "2026-10-31T00:00:07+00:00"),
            entry("I will inspect the project.", "2026-10-31T00:00:08+00:00"),
            entry("It looks coherent.", "2026-10-31T00:00:09+00:00"),
            entry("tokens used", "2026-10-31T00:00:10+00:00"),
            entry("1,234", "2026-10-31T00:00:11+00:00"),
            entry("I will inspect the project.", "2026-10-31T00:00:12+00:00"),
            entry("It looks coherent.", "2026-10-31T00:00:13+00:00"),
        ]
        data = build_transcript(session_id="s_codex", status="stopped",
                                prompt="Do the task", stderr=stderr,
                                platform="codex")
        self.assertFalse([item for item in data["items"] if item["type"] == "error"])
        text = "\n".join(item["text"] for item in data["items"]
                         if item["type"] == "message" and item["role"] == "assistant")
        self.assertEqual(text.count("inspect the project"), 1)
        self.assertEqual(text.count("coherent"), 1)
        self.assertNotIn("OpenAI Codex", text)
        self.assertNotIn("1,234", text)

    def test_codex_real_stderr_error_stays_an_error(self):
        data = build_transcript(
            session_id="s_codex_err", status="errored", prompt="go",
            stderr=[entry("ERROR: unexpected status 401 Unauthorized")],
            platform="codex")
        errors = [item for item in data["items"] if item["type"] == "error"]
        self.assertEqual(len(errors), 1)
        self.assertIn("401", errors[0]["text"])

    def test_openai_and_claude_shapes(self):
        openai_line = json.dumps({"choices": [{"message": {
            "role": "assistant", "content": "Planning",
            "tool_calls": [{"id": "o1", "function": {"name": "get", "arguments": '{"x":1}'}}],
        }}]})
        claude_line = json.dumps({"type": "assistant", "message": {"content": [
            {"type": "text", "text": "Report ready"},
            {"type": "tool_use", "id": "cl1", "name": "write", "input": {"path": "a.md"}},
        ]}})
        result_line = json.dumps({"type": "result", "result": "All done."})
        data = build_transcript(session_id="s_5", status="stopped",
                                stdout=[entry(openai_line), entry(claude_line), entry(result_line)])
        tools = [item for item in data["items"] if item["type"] == "tool"]
        self.assertEqual([tool["id"] for tool in tools], ["o1", "cl1"])
        text = "\n".join(item["text"] for item in data["items"] if item["type"] == "message")
        self.assertIn("Planning", text)
        self.assertIn("Report ready", text)
        self.assertIn("All done.", text)

    def test_unknown_json_stays_visible_as_text(self):
        line = '{"type": "mystery", "payload": 1}'
        data = build_transcript(session_id="s_6", status="stopped", stdout=[entry(line)])
        self.assertIsNone(events_from_line(line))
        self.assertEqual(data["items"][-1]["type"], "message")
        self.assertIn("mystery", data["items"][-1]["text"])

    def test_ansi_is_stripped_and_empty_lines_ignored(self):
        data = build_transcript(session_id="s_7", status="stopped",
                                stdout=[entry("\x1b[31mred\x1b[0m"), entry("   ")])
        self.assertEqual(data["items"][-1]["text"], "red")

    def test_usage_events_are_aggregated_and_labeled(self):
        explicit = "[[ark-event]] " + json.dumps({
            "type": "usage", "usage": {"input_tokens": 120, "output_tokens": 30},
        })
        openai = json.dumps({"usage": {"prompt_tokens": 10, "completion_tokens": 5,
                                       "total_tokens": 15}})
        data = build_transcript(session_id="s_usage", status="stopped", prompt="hi",
                                stdout=[entry(explicit), entry(openai)])
        self.assertEqual(data["usage"]["source"], "reported")
        self.assertFalse(data["usage"]["estimated"])
        self.assertEqual(data["usage"]["reported_events"], 2)
        self.assertEqual(data["usage"]["input_tokens"], 130)
        self.assertEqual(data["usage"]["output_tokens"], 35)
        self.assertEqual(data["usage"]["total_tokens"], 165)
        self.assertEqual(data["stats"]["tokens"], 165)

        estimated = build_transcript(session_id="s_est", status="stopped", prompt="hello there",
                                     stdout=[entry("a reply with some words")])
        self.assertEqual(estimated["usage"]["source"], "estimated")
        self.assertTrue(estimated["usage"]["estimated"])
        self.assertGreater(estimated["usage"]["total_tokens"], 0)
        self.assertIn("estimated_usage", estimated["usage"])

    def test_rich_markdown_cjk_and_math_are_preserved_verbatim(self):
        message = (
            "# 标题 Heading\n\n"
            "| 列 | Value |\n|---|---|\n| 一 | `code` |\n\n"
            "```python\nprint('你好')\n```\n\n"
            "> quoted 引用\n\n"
            "Inline $E = mc^2$ and block:\n\n$$\n\\int_0^1 x^2 dx\n$$\n"
        )
        data = build_transcript(session_id="s_rich", status="stopped",
                                stdout=[entry(message, kind="")])
        text = data["items"][0]["text"]
        self.assertIn("标题 Heading", text)
        self.assertIn("```python", text)
        self.assertIn("print('你好')", text)
        self.assertIn("$E = mc^2$", text)
        self.assertIn("\\int_0^1 x^2 dx", text)
        self.assertIn("| 列 | Value |", text)

    def test_permission_requests_are_parsed_and_rendered(self):
        generic = "[[ark-event]] " + json.dumps({
            "type": "permission_request", "id": "r1", "kind": "command",
            "tool": "bash", "command": "pytest -q", "description": "run tests",
            "input": {"command": "pytest -q"},
        })
        jsonl = json.dumps({
            "type": "permission_request", "request_id": "r2",
            "tool": "apply_patch", "input": {"path": "artifacts/a.py"},
        })
        claude = json.dumps({
            "type": "control_request", "request_id": "c1",
            "request": {"subtype": "can_use_tool", "tool_name": "Bash",
                        "input": {"command": "ls"}},
        })
        resolved = "[[ark-event]] " + json.dumps({
            "type": "permission_resolved", "id": "r2",
        })
        data = build_transcript(
            session_id="s_perm", status="running",
            stdout=[entry(generic), entry(jsonl), entry(claude), entry(resolved)],
        )
        permissions = [item for item in data["items"] if item["type"] == "permission"]
        self.assertEqual(len(permissions), 3)
        first = permissions[0]
        self.assertEqual(first["request_id"], "r1")
        self.assertEqual(first["kind"], "command")
        self.assertEqual(first["tool"], "bash")
        self.assertEqual(first["command"], "pytest -q")
        self.assertEqual(first["description"], "run tests")
        self.assertEqual(first["status"], "pending")
        self.assertEqual(permissions[1]["kind"], "file_edit")
        self.assertEqual(permissions[1]["path"], "artifacts/a.py")
        self.assertEqual(permissions[1]["status"], "resolved")
        self.assertEqual(permissions[2]["protocol"], "claude-control")
        self.assertEqual(permissions[2]["command"], "ls")
        self.assertEqual(data["stats"]["permissions"], 3)
        self.assertEqual(data["stats"]["pending_permissions"], 2)

    def test_permission_request_line_recognition(self):
        request = permission_request_from_line(
            "[[ark-event]] " + json.dumps({"type": "permission_request",
                                           "id": "r9", "tool": "Bash",
                                           "command": "rm -rf build"}))
        self.assertEqual(request["request_id"], "r9")
        self.assertEqual(request["kind"], "command")
        self.assertEqual(request["protocol"], "ark-permission-v1")
        self.assertIsNone(permission_request_from_line("plain output"))
        self.assertIsNone(permission_request_from_line(
            json.dumps({"type": "message", "text": "hello"})))
        control = permission_request_from_line(json.dumps({
            "type": "control_request", "request_id": "c9",
            "request": {"subtype": "can_use_tool", "tool_name": "Write",
                        "input": {"file_path": "x.md"}},
        }))
        self.assertEqual(control["protocol"], "claude-control")
        self.assertEqual(control["kind"], "file_edit")
        self.assertEqual(control["path"], "x.md")

    def test_permission_responses_use_the_adapter_protocol(self):
        generic = build_permission_response(
            {"request_id": "r1", "protocol": "ark-permission-v1"}, "allow", "once")
        self.assertEqual(generic["type"], "permission_response")
        self.assertEqual(generic["decision"], "allow")
        self.assertEqual(generic["scope"], "once")
        self.assertEqual(generic["request_id"], "r1")
        denied = build_permission_response(
            {"request_id": "r2", "protocol": "ark-permission-v1"},
            "deny", "auto", "read-only mode denies mutating actions")
        self.assertEqual(denied["decision"], "deny")
        self.assertIn("read-only", denied["message"])
        claude = build_permission_response({
            "request_id": "c1", "native_id": "c1", "protocol": "claude-control",
            "input": {"command": "ls"},
        }, "allow", "once")
        self.assertEqual(claude["type"], "control_response")
        self.assertEqual(claude["response"]["request_id"], "c1")
        self.assertEqual(claude["response"]["response"]["behavior"], "allow")
        self.assertEqual(claude["response"]["response"]["updatedInput"], {"command": "ls"})


class WorkspaceIndexTests(unittest.TestCase):
    def test_classifies_media_documents_and_skips_hidden(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "artifacts").mkdir()
            (root / "artifacts" / "shot.png").write_bytes(b"png")
            (root / "artifacts" / "clip.mp4").write_bytes(b"mp4")
            (root / "artifacts" / "voice.wav").write_bytes(b"wav")
            (root / "artifacts" / "notes.md").write_text("# notes")
            (root / "artifacts" / "report.pdf").write_bytes(b"%PDF")
            (root / ".hidden").write_text("secret")
            index = index_workspace(root, limit=100)
            self.assertEqual(index["count"], 5)
            self.assertEqual(sorted(item["kind"] for item in index["media"]),
                             ["audio", "image", "video"])
            self.assertIn("artifacts/notes.md",
                          [item["path"] for item in index["documents"]])
            self.assertIn("artifacts/report.pdf",
                          [item["path"] for item in index["documents"]])
            self.assertFalse(any(item["name"] == ".hidden"
                                 for item in index["documents"] + index["media"] + index["other"]))

    def test_limit_and_missing_root(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for i in range(6):
                (root / f"f{i}.png").write_bytes(b"x")
            index = index_workspace(root, limit=2)
            self.assertTrue(index["truncated"])
            self.assertEqual(index["count"], 2)
            self.assertEqual(index_workspace(root / "missing")["count"], 0)

    @unittest.skipIf(os.name == "nt", "symlink creation needs a Windows privilege")
    def test_symlinks_are_not_followed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            outside = root.parent / "outside.txt"
            outside.write_text("outside")
            try:
                (root / "link.png").symlink_to(outside)
            except OSError:
                self.skipTest("host does not allow symlinks")
            index = index_workspace(root)
            self.assertEqual(index["count"], 0)
            outside.unlink(missing_ok=True)


if __name__ == "__main__":
    unittest.main()
