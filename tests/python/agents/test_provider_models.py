"""Offline tests for provider model discovery (arkstudio.providers)."""
import json
import tempfile
import unittest
from pathlib import Path

from arkstudio import providers
from arkstudio.providers import (
    ProviderCatalog,
    claude_config_models,
    codex_cli_models,
    codex_config_models,
    gemini_config_models,
    merge_models,
    openai_compatible_models,
    opencode_cli_models,
    opencode_config_models,
)

CODEX_CATALOG = {
    "models": [
        {"slug": "gpt-b", "display_name": "GPT B", "visibility": "list", "priority": 2,
         "default_reasoning_level": "medium",
         "supported_reasoning_levels": [{"effort": "low"}, {"effort": "medium"}, {"effort": "high"}],
         "base_instructions": "x" * 500},
        {"slug": "gpt-a", "display_name": "GPT A", "visibility": "list", "priority": 1,
         "default_reasoning_level": "low",
         "supported_reasoning_levels": [{"effort": "low"}, {"effort": "xhigh"}]},
        {"slug": "internal-review", "display_name": "Hidden", "visibility": "hide", "priority": 0},
        {"slug": "sk-THIS-IS-NOT-A-MODEL-abcdefghijklmnopqrstuvwxyz", "visibility": "list"},
        "garbage-row",
    ]
}


def ok(stdout):
    return {"returncode": 0, "stdout": stdout, "stderr": "", "error": ""}


class FakeRunner:
    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __call__(self, argv, timeout=None):
        self.calls.append((list(argv), timeout))
        key = " ".join(argv[1:])
        response = self.responses.get(key)
        if response is None:
            return {"returncode": 2, "stdout": "", "stderr": "unknown", "error": ""}
        return response


def preset(provider_id, command, **extra):
    base = {"id": provider_id, "label": provider_id, "description": "", "command": command,
            "prompt_mode": "arg", "followup": False, "models": [], "efforts": [],
            "access_modes": ["read_only", "full_access"], "demo": False}
    base.update(extra)
    return base


class CliListTests(unittest.TestCase):
    def test_codex_debug_models_orders_by_priority_and_keeps_efforts(self):
        run = FakeRunner({"debug models --bundled": ok(json.dumps(CODEX_CATALOG))})
        result = codex_cli_models("codex", run, bundled=True)
        self.assertEqual(result["error"], "")
        self.assertTrue(result["full_list"])
        ids = [item["id"] for item in result["models"]]
        self.assertEqual(ids, ["gpt-a", "gpt-b"])  # hidden and secret-looking rows dropped
        self.assertEqual(result["models"][0]["efforts"], ["low", "xhigh"])
        self.assertEqual(result["models"][1]["default_effort"], "medium")
        self.assertEqual(result["models"][0]["source"], "detected")
        self.assertEqual(run.calls[0][0], ["codex", "debug", "models", "--bundled"])
        self.assertEqual(run.calls[0][1], providers.MODEL_LIST_TIMEOUT_SECONDS)
        self.assertNotIn("base_instructions", json.dumps(result))

    def test_codex_timeout_is_reported_not_raised(self):
        run = FakeRunner({"debug models": {"returncode": None, "stdout": "", "stderr": "",
                                           "error": "timed out"}})
        result = codex_cli_models("codex", run, bundled=False)
        self.assertEqual(result["models"], [])
        self.assertIn("timed out", result["error"])

    def test_codex_malformed_output(self):
        run = FakeRunner({"debug models --bundled": ok("not json {")})
        result = codex_cli_models("codex", run, bundled=True)
        self.assertIn("malformed", result["error"])
        run = FakeRunner({"debug models --bundled": ok(json.dumps({"models": "nope"}))})
        self.assertIn("no model list", codex_cli_models("codex", run, bundled=True)["error"])

    def test_codex_nonzero_exit(self):
        run = FakeRunner({})
        self.assertIn("exited with 2", codex_cli_models("codex", run, bundled=True)["error"])

    def test_opencode_models_parses_ids_and_ignores_noise(self):
        run = FakeRunner({"models": ok(
            "\x1b[1manthropic/claude-x\x1b[0m\nopenai/gpt-y\nopenai/gpt-y\n"
            "Some banner line\n  \nopenrouter/sk-abcdefghijklmnopqrstuvwxyz0123456789\n")})
        result = opencode_cli_models("opencode", run)
        self.assertEqual([item["id"] for item in result["models"]],
                         ["anthropic/claude-x", "openai/gpt-y"])
        self.assertTrue(result["full_list"])

    def test_opencode_empty_output(self):
        run = FakeRunner({"models": ok("no models\n")})
        self.assertIn("no model ids", opencode_cli_models("opencode", run)["error"])


class ConfigReaderTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.home = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, rel, text):
        path = self.home / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def test_codex_config_reads_model_profiles_and_effort_but_never_secrets(self):
        self.write(".codex/config.toml", "\n".join([
            'model_provider = "OpenAI"',
            'model = "gpt-main"',
            'model_reasoning_effort = "high"',
            'api_key = "sk-SECRET-TOML-VALUE-1234567890abcdef"',
            '[model_providers.OpenAI]',
            'base_url = "https://example.test/v1"',
            'experimental_bearer_token = "SECRET-BEARER-abcdefghijklmnopqrstuvwxyz"',
            '[profiles.fast]',
            "model = 'gpt-fast'",
            '[profiles.bad]',
            'model = "sk-SECRET-LOOKING-MODEL-abcdefghijklmnopqrstuvwxyz"',
            '[projects."e:\\\\x"]',
            'model = "not-a-profile"',
        ]))
        result = codex_config_models(self.home, {})
        self.assertEqual(result["default"], "gpt-main")
        self.assertEqual(result["default_effort"], "high")
        self.assertEqual([item["id"] for item in result["models"]], ["gpt-main", "gpt-fast"])
        dumped = json.dumps(result)
        for secret in ("SECRET-TOML", "SECRET-BEARER", "SECRET-LOOKING", "example.test"):
            self.assertNotIn(secret, dumped)
        self.assertEqual(result["method"], "config:~/.codex/config.toml")

    def test_codex_config_missing_is_silent(self):
        result = codex_config_models(self.home, {})
        self.assertEqual(result["models"], [])
        self.assertEqual(result["error"], "")

    def test_claude_settings_with_bom_reads_model_env_but_not_token(self):
        path = self.home / ".claude" / "settings.json"
        path.parent.mkdir(parents=True)
        path.write_text(json.dumps({
            "effortLevel": "high",
            "env": {"ANTHROPIC_AUTH_TOKEN": "SECRET-CLAUDE-TOKEN-abcdefghijklmnop",
                    "ANTHROPIC_BASE_URL": "https://proxy.example.test",
                    "ANTHROPIC_MODEL": "claude-custom-1"},
        }), encoding="utf-8-sig")
        result = claude_config_models(self.home, {})
        self.assertEqual(result["default"], "claude-custom-1")
        self.assertEqual(result["default_effort"], "high")
        dumped = json.dumps(result)
        self.assertNotIn("SECRET-CLAUDE-TOKEN", dumped)
        self.assertNotIn("proxy.example.test", dumped)

    def test_claude_settings_secret_in_model_field_is_dropped(self):
        self.write(".claude/settings.json", json.dumps(
            {"model": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123"}))
        result = claude_config_models(self.home, {})
        self.assertEqual(result["models"], [])
        self.assertNotIn("sk-ant", json.dumps(result))

    def test_malformed_json_config_is_reported(self):
        self.write(".claude/settings.json", "{not json")
        self.assertIn("malformed", claude_config_models(self.home, {})["error"])
        self.write(".gemini/settings.json", "[1, 2")
        self.assertIn("malformed", gemini_config_models(self.home, {})["error"])

    def test_opencode_config_lists_models_and_provider_models(self):
        self.write(".config/opencode/opencode.json", "\n".join([
            "// comment line",
            json.dumps({
                "model": "anthropic/claude-x",
                "small_model": "openai/gpt-mini",
                "provider": {"local": {"options": {"apiKey": "SECRET-OPENCODE-KEY-abcdefghijklmnop"},
                                       "models": {"llama-3": {"name": "Llama 3"}}}},
            }),
        ]))
        result = opencode_config_models(self.home, {})
        self.assertEqual(result["default"], "anthropic/claude-x")
        self.assertEqual([item["id"] for item in result["models"]],
                         ["anthropic/claude-x", "openai/gpt-mini", "local/llama-3"])
        self.assertNotIn("SECRET-OPENCODE", json.dumps(result))

    def test_gemini_settings_model_object(self):
        self.write(".gemini/settings.json", json.dumps({"model": {"name": "gemini-x-pro"}}))
        self.assertEqual(gemini_config_models(self.home, {})["default"], "gemini-x-pro")


class HttpListTests(unittest.TestCase):
    def test_models_endpoint_parses_and_sends_key_only_in_header(self):
        seen = {}

        def http_get(url, headers, timeout):
            seen.update(url=url, headers=dict(headers), timeout=timeout)
            return {"status": 200, "error": "",
                    "body": json.dumps({"data": [{"id": "m-1"}, {"id": "m-2"}, {"id": "m-1"}, {}]})}

        result = openai_compatible_models("https://api.example.test/v1/", "sk-KEY-abcdefghijklmnop1234", http_get)
        self.assertEqual([item["id"] for item in result["models"]], ["m-1", "m-2"])
        self.assertEqual(seen["url"], "https://api.example.test/v1/models")
        self.assertEqual(seen["timeout"], providers.MODEL_HTTP_TIMEOUT_SECONDS)
        self.assertEqual(seen["headers"]["Authorization"], "Bearer sk-KEY-abcdefghijklmnop1234")
        self.assertNotIn("sk-KEY", json.dumps(result))

    def test_models_endpoint_failures_are_statuses(self):
        def timeout(url, headers, timeout):
            return {"status": None, "body": "", "error": "timed out"}

        def boom(url, headers, timeout):
            raise RuntimeError("sk-LEAK-abcdefghijklmnopqrstuvwxyz should not appear")

        def bad(url, headers, timeout):
            return {"status": 200, "body": "<html>", "error": ""}

        self.assertIn("timed out", openai_compatible_models("https://x.test", "", timeout)["error"])
        failed = openai_compatible_models("https://x.test", "", boom)
        self.assertIn("RuntimeError", failed["error"])
        self.assertNotIn("sk-LEAK", json.dumps(failed))
        self.assertIn("malformed", openai_compatible_models("https://x.test", "", bad)["error"])
        self.assertIn("no endpoint", openai_compatible_models("", "", bad)["error"])

    def test_default_http_rejects_non_http_scheme(self):
        result = providers._default_http_get("file:///etc/passwd", {}, 1)
        self.assertIn("unsupported", result["error"])


class MergeTests(unittest.TestCase):
    def test_configured_models_win_and_are_marked(self):
        merged = merge_models(
            preset("codex", ["codex"], models=["gpt-a", "mine"]),
            [codex_cli_models("codex", FakeRunner({"debug models --bundled": ok(json.dumps(CODEX_CATALOG))}),
                              bundled=True)],
            checked_at="2026-11-04T00:00:00+00:00")
        by_id = {item["id"]: item for item in merged["options"]}
        self.assertEqual(merged["source"], "configured")
        self.assertEqual(by_id["gpt-a"]["source"], "configured")
        self.assertEqual(by_id["gpt-a"]["efforts"], ["low", "xhigh"])  # detected efforts still attach
        self.assertEqual(by_id["mine"]["source"], "configured")
        self.assertEqual(by_id["gpt-b"]["source"], "detected")
        self.assertFalse(merged["builtin"])

    def test_builtin_fallback_is_labelled_with_reason(self):
        merged = merge_models(preset("claude", ["claude"]), [], checked_at=None)
        self.assertTrue(merged["builtin"])
        self.assertEqual(merged["source"], "builtin")
        self.assertEqual(merged["reason"], "no_list_command")
        self.assertTrue(all(item["source"] == "builtin" for item in merged["options"]))
        self.assertIn("efforts", merged["options"][0])
        missing = merge_models(preset("gemini", ["gemini"]), [], checked_at=None, installed=False)
        self.assertEqual(missing["reason"], "not_installed")

    def test_failed_list_falls_back_to_builtin(self):
        failed = codex_cli_models("codex", FakeRunner({}), bundled=True)
        merged = merge_models(preset("codex", ["codex"]), [failed], checked_at=None)
        self.assertEqual(merged["reason"], "list_failed")
        self.assertIn("exited with 2", merged["error"])
        self.assertTrue(merged["options"])

    def test_config_default_is_flagged(self):
        merged = merge_models(preset("claude", ["claude"]), [
            {"method": "config:~/.claude/settings.json", "models": [
                {"id": "claude-x", "label": "claude-x", "source": "detected", "via": "config"}],
             "default": "claude-x", "default_effort": "high", "error": "", "full_list": False}],
            checked_at=None)
        first = merged["options"][0]
        self.assertEqual(first["id"], "claude-x")
        self.assertTrue(first["default"])
        self.assertEqual(first["default_effort"], "high")
        self.assertEqual(merged["source"], "detected")


class CatalogDiscoveryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.home = Path(self.tmp.name)
        self.http_calls = []

    def tearDown(self):
        self.tmp.cleanup()

    def http_get(self, url, headers, timeout):
        self.http_calls.append(url)
        return {"status": 200, "error": "", "body": json.dumps({"data": [{"id": "ds-a"}, {"id": "ds-b"}]})}

    def catalog(self, list_run, presets=None, which=None):
        return ProviderCatalog(
            presets or [preset("codex", ["codex", "exec"]), preset("dsh", ["dsh", "headless"]),
                        preset("opencode", ["opencode", "run"]), preset("claude", ["claude", "-p"])],
            env={}, home=self.home, ttl=60,
            which=which or (lambda program: program if program in {"codex", "dsh", "claude"} else None),
            run=lambda program: ok("1.0"),
            list_run=list_run, http_get=self.http_get,
            assistant_key=lambda: (True, "ark_assistant"),
            assistant_model=lambda: "ds-a",
            assistant_endpoint=lambda: {"base_url": "https://tokenhub.example.test/v1",
                                        "api_key": "sk-ASSISTANT-SECRET-abcdefghijklmnop"},
        )

    def caps(self, payload):
        return {item["id"]: item["capabilities"] for item in payload["providers"]}

    def test_snapshot_uses_bundled_catalog_and_no_network(self):
        run = FakeRunner({"debug models --bundled": ok(json.dumps(CODEX_CATALOG)),
                          "debug models": ok(json.dumps({"models": [{"slug": "fresh", "priority": 0}]}))})
        catalog = self.catalog(run)
        caps = self.caps(catalog.snapshot())
        self.assertEqual(caps["codex"]["models"], ["gpt-a", "gpt-b"])
        self.assertEqual(caps["codex"]["models_source"], "detected")
        self.assertIn("cli:codex debug models --bundled", caps["codex"]["models_methods"])
        self.assertTrue(caps["codex"]["models_checked_at"])
        self.assertEqual(self.http_calls, [])
        # DSH without a refresh: the ARK assistant model, no HTTP request.
        self.assertIn("ds-a", caps["dsh"]["models"])
        # Missing CLI: never run, builtin fallback with a reason.
        self.assertEqual(caps["opencode"]["models_reason"], "not_installed")
        self.assertFalse(any(call[0][0] == "opencode" for call in run.calls))
        # Claude: nothing is run, builtin aliases plus CLI-documented efforts.
        self.assertFalse(any(call[0][0] == "claude" for call in run.calls))
        self.assertEqual(caps["claude"]["models_source"], "builtin")
        self.assertIn("max", caps["claude"]["efforts"])

    def test_refresh_runs_live_list_and_http_then_cache_keeps_them(self):
        run = FakeRunner({"debug models --bundled": ok(json.dumps(CODEX_CATALOG)),
                          "debug models": ok(json.dumps({"models": [{"slug": "fresh", "priority": 0}]}))})
        catalog = self.catalog(run)
        catalog.snapshot()
        refreshed = self.caps(catalog.refresh())
        self.assertEqual(refreshed["codex"]["models"], ["fresh"])
        self.assertEqual(self.http_calls, ["https://tokenhub.example.test/v1/models"])
        self.assertEqual(refreshed["dsh"]["models_source"], "detected")
        self.assertEqual(set(refreshed["dsh"]["models"]), {"ds-a", "ds-b"})
        self.assertTrue(next(o for o in refreshed["dsh"]["model_options"] if o["id"] == "ds-a")["default"])
        # A later TTL re-probe reuses the refresh result instead of hitting the network.
        catalog._cached_at -= 1000
        again = self.caps(catalog.snapshot())
        self.assertEqual(len(self.http_calls), 1)
        self.assertEqual(again["codex"]["models"], ["fresh"])
        self.assertNotIn("sk-ASSISTANT-SECRET", json.dumps(catalog.snapshot()))

    def test_refresh_failure_falls_back_to_bundled_and_reports_error(self):
        run = FakeRunner({"debug models --bundled": ok(json.dumps(CODEX_CATALOG)),
                          "debug models": {"returncode": None, "stdout": "", "stderr": "",
                                           "error": "timed out"}})
        catalog = self.catalog(run)
        caps = self.caps(catalog.refresh())
        self.assertEqual(caps["codex"]["models"], ["gpt-a", "gpt-b"])

    def test_list_command_timeout_falls_back_to_builtin(self):
        run = FakeRunner({"debug models --bundled": {"returncode": None, "stdout": "", "stderr": "",
                                                     "error": "timed out"}})
        caps = self.caps(self.catalog(run).snapshot())
        self.assertEqual(caps["codex"]["models_source"], "builtin")
        self.assertEqual(caps["codex"]["models_reason"], "list_failed")
        self.assertIn("timed out", caps["codex"]["models_error"])
        self.assertIn("gpt-5.5", caps["codex"]["models"])

    def test_version_fake_without_list_fake_never_spawns(self):
        catalog = ProviderCatalog(
            [preset("codex", ["codex", "exec"])], env={}, home=self.home,
            which=lambda program: program, run=lambda program: ok("1.0"),
            assistant_key=lambda: (False, ""), assistant_model=lambda: "")
        caps = self.caps(catalog.snapshot())
        self.assertEqual(caps["codex"]["models_source"], "builtin")
        self.assertIn("disabled", caps["codex"]["models_error"])

    def test_default_list_run_handles_missing_program(self):
        result = providers._default_list_run(["definitely-not-a-real-ark-binary-xyz", "models"], 2)
        self.assertIsNone(result["returncode"])
        self.assertTrue(result["error"])


class EffortChoiceTests(unittest.TestCase):
    def test_session_effort_choices_follow_selected_model(self):
        from arkstudio.sessions import SessionManager

        class Catalog:
            def metadata_for(self, provider_id):
                return {"capabilities": {
                    "efforts": ["low", "medium"],
                    "model_options": [
                        {"id": "a", "efforts": ["low", "xhigh"], "default": True},
                        {"id": "b", "efforts": ["medium"]},
                        {"id": "c"},
                    ]}}

        manager = SessionManager.__new__(SessionManager)
        manager.provider_catalog = Catalog()
        p = preset("codex", ["codex"])
        self.assertEqual(manager.effort_choices(p, "b"), ["medium"])
        self.assertEqual(manager.effort_choices(p, ""), ["low", "xhigh"])
        self.assertEqual(manager.effort_choices(p, "c"), ["low", "medium"])
        self.assertEqual(manager.effort_choices(dict(p, efforts=["x"]), "b"), ["x"])
        manager._check_effort(p, "a", "xhigh")
        with self.assertRaises(Exception):
            manager._check_effort(p, "b", "xhigh")


if __name__ == "__main__":
    unittest.main()
