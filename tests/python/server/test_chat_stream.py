import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from arkstudio import chat
from arkstudio.server import App


class ChatStreamTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.app = App(Path(self.tmp.name) / "projects", seed_demo=True)
        self.config = {"api_key": "fixture", "base_url": "http://unused.test",
                       "model": "fixture", "reasoning_effort": "high", "timeout": 1}
        self.patches = [mock.patch.object(self.app, "effective_assistant", return_value=self.config),
                        mock.patch.dict("os.environ", {"ARK_CHAT_MODE": "deepseek"})]
        for patch in self.patches:
            patch.start()

    def tearDown(self):
        for patch in reversed(self.patches):
            patch.stop()
        self.app.project.close()
        self.tmp.cleanup()

    def test_real_deltas_arrive_before_provider_finishes_and_are_persisted(self):
        release = []

        def provider(messages, tools, config):
            yield {"reasoning_content": "Inspect "}
            self.assertEqual(release, ["reasoning"])
            yield {"reasoning_content": "the evidence."}
            yield {"content": "Answer "}
            self.assertEqual(release, ["reasoning", "content"])
            yield {"content": "C-0017."}

        with mock.patch.object(chat, "_deepseek_stream", side_effect=provider):
            stream = self.app.chat_stream({"message": "Summarize the evidence."})
            events = []
            for event in stream:
                events.append(event)
                if event["type"] in ("reasoning", "content") and event["type"] not in release:
                    release.append(event["type"])
        final = events[-1]
        self.assertEqual(final["type"], "done")
        self.assertEqual(final["assistant"]["content"], "Answer C-0017.")
        self.assertEqual(final["assistant"]["meta"]["reasoning"], "Inspect the evidence.")
        self.assertEqual(final["harness"]["mode"], "deepseek")

    def test_fragmented_tool_call_reports_running_and_result_before_next_round(self):
        round_number = 0

        def provider(messages, tools, config):
            nonlocal round_number
            round_number += 1
            if round_number == 1:
                yield {"reasoning_content": "Read the project first."}
                yield {"tool_calls": [{"index": 0, "id": "call-1", "function": {"name": "get_", "arguments": "{"}}]}
                yield {"tool_calls": [{"index": 0, "function": {"name": "context", "arguments": "}"}}]}
            else:
                self.assertEqual(messages[-1]["role"], "tool")
                self.assertEqual(messages[-2]["reasoning_content"], "Read the project first.")
                yield {"content": "The project is ready."}

        with mock.patch.object(chat, "_deepseek_stream", side_effect=provider):
            events = list(self.app.chat_stream({"message": "Read project state."}))
        tools = [event for event in events if event["type"] == "tool"]
        self.assertEqual([event["state"] for event in tools], ["running", "done"])
        self.assertEqual(tools[0]["name"], "get_context")
        self.assertTrue(tools[1]["ok"])
        self.assertEqual(len(events[-1]["assistant"]["tools"]), 1)

    def test_legacy_protocol_split_across_deltas_is_hidden(self):
        rounds = iter([
            [{"content": "<"}, {"content": "||DS"}, {"content": "ML||calls><||DSML||invoke name=\"get_context\"><||DSML||invoke></||DSML||calls>"}],
            [{"content": "A readable answer."}],
        ])
        with mock.patch.object(chat, "_deepseek_stream", side_effect=lambda *args: iter(next(rounds))):
            events = list(self.app.chat_stream({"message": "Read project."}))
        self.assertEqual("".join(event.get("text", "") for event in events if event["type"] == "content"), "A readable answer.")
        self.assertEqual(events[-1]["type"], "done")

    def test_provider_failure_is_error_and_does_not_store_false_completion(self):
        def provider(*args):
            yield {"content": "Partial answer."}
            raise chat.DeepSeekError("connection interrupted")

        with mock.patch.object(chat, "_deepseek_stream", side_effect=provider):
            events = list(self.app.chat_stream({"message": "Answer me."}))
        self.assertEqual(events[-1]["type"], "error")
        self.assertFalse(any(event["type"] == "done" for event in events))
        self.assertEqual(self.app.project.store.chat_history()[-1]["role"], "user")

    def test_sse_network_request_and_fragment_decode(self):
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def __iter__(self):
                return iter([b": keep-alive\n", b"data: " + json.dumps({"choices": [{"delta": {"content": "live"}}]}).encode() + b"\n",
                             b"data: [DONE]\n"])
        with mock.patch.object(chat.urllib.request, "urlopen", return_value=Response()) as request:
            self.assertEqual(list(chat._deepseek_stream([], None, self.config)), [{"content": "live"}])
        self.assertTrue(json.loads(request.call_args.args[0].data)["stream"])


if __name__ == "__main__":
    unittest.main()
