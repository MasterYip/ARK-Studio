import json
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio.providers import ProviderCatalog
from arkstudio.server import App, serve


def preset(provider_id, label, command):
    return {
        "id": provider_id,
        "label": label,
        "description": f"{label} test preset",
        "command": command,
        "prompt_mode": "arg",
        "followup": False,
        "models": [],
        "efforts": [],
        "access_modes": ["read_only", "full_access"],
        "demo": False,
    }


PRESETS = [
    preset("claude", "Claude Code", ["claude", "-p", "{prompt}"]),
    preset("codex", "Codex CLI", ["codex", "exec", "{prompt}"]),
    preset("dsh", "DeepSeek Harness", ["dsh", "headless", "-"]),
    preset("opencode", "OpenCode", ["opencode", "run", "{prompt}"]),
    preset("cursor", "Cursor Agent", ["cursor-agent", "-p", "{prompt}"]),
    preset("echo", "Echo", ["python", "-u", "demo_agent.py", "{prompt}"]),
    preset("shell", "Local shell", ["powershell", "-NoProfile"]),
]


class ProviderCatalogTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.home = Path(self.tmp.name) / "home"
        self.home.mkdir()
        self.which_calls = []
        self.run_calls = []

    def tearDown(self):
        self.tmp.cleanup()

    def which(self, program):
        self.which_calls.append(program)
        if program in {"claude", "codex", "dsh", "opencode", "python", "powershell"}:
            return program
        return None

    def runner(self, program):
        self.run_calls.append(program)
        versions = {
            "claude": "2.1.0 (Claude Code)",
            "codex": "codex-cli 0.99.0",
            "dsh": "dsh 1.2.3",
            "opencode": "opencode 0.8.0",
            "python": "Python 3.12.0",
        }
        return {"returncode": 0, "stdout": versions.get(program, ""), "stderr": "", "error": ""}

    def catalog(self, *, env=None, assistant_key=None, ttl=60):
        return ProviderCatalog(
            PRESETS,
            env=env if env is not None else {},
            home=self.home,
            ttl=ttl,
            which=self.which,
            run=self.runner,
            assistant_key=assistant_key or (lambda: (False, "ark_assistant")),
            assistant_model=lambda: "",
        )

    def write_credentials(self):
        claude = self.home / ".claude"
        claude.mkdir()
        (claude / ".credentials.json").write_text(json.dumps({
            "claudeAiOauth": {
                "accessToken": "sk-ant-CLAUDE-SECRET-VALUE-123456",
                "refreshToken": "refresh-SECRET-VALUE-abcdefghijklmnop",
                "expiresAt": 4102444800000,
            },
            "email": "person@example.test",
        }), encoding="utf-8")
        codex = self.home / ".codex"
        codex.mkdir()
        (codex / "auth.json").write_text(json.dumps({
            "tokens": {"access_token": "CODEX-SECRET-VALUE-abcdefghijklmnop"},
            "OPENAI_API_KEY": "sk-CODEX-SECRET-VALUE-1234567890",
        }), encoding="utf-8")
        opencode = self.home / ".local" / "share" / "opencode"
        opencode.mkdir(parents=True)
        (opencode / "auth.json").write_text("{}", encoding="utf-8")

    def by_id(self, payload):
        return {item["id"]: item for item in payload["providers"]}

    def test_statuses_are_honest_and_do_not_leak_secret_values(self):
        self.write_credentials()
        catalog = self.catalog()
        payload = catalog.snapshot()
        providers = self.by_id(payload)
        self.assertEqual(providers["claude"]["status"], "ready")
        self.assertEqual(providers["codex"]["status"], "ready")
        # DSH is installed but ARK cannot verify its own profile.
        self.assertEqual(providers["dsh"]["status"], "unknown")
        self.assertIsNone(providers["dsh"]["authenticated"])
        # Missing CLI wins over any credential question.
        self.assertEqual(providers["cursor"]["status"], "missing")
        self.assertEqual(providers["opencode"]["status"], "unauthenticated")
        self.assertEqual(providers["echo"]["status"], "ready")
        self.assertEqual(providers["shell"]["auth"]["state"], "not_required")
        self.assertEqual(providers["claude"]["version"], "2.1.0 (Claude Code)")
        self.assertEqual(payload["counts"]["missing"], 1)

        dumped = json.dumps(payload)
        for secret in (
            "sk-ant-CLAUDE-SECRET-VALUE-123456",
            "refresh-SECRET-VALUE-abcdefghijklmnop",
            "CODEX-SECRET-VALUE-abcdefghijklmnop",
            "sk-CODEX-SECRET-VALUE-1234567890",
        ):
            self.assertNotIn(secret, dumped)
        # Sources expose variable/file labels, not values.
        self.assertIn("file:~/.claude/.credentials.json", providers["claude"]["auth"]["sources"])
        self.assertIn("file:~/.codex/auth.json", providers["codex"]["auth"]["sources"])

    def test_environment_credentials_are_detected_by_name_only(self):
        catalog = self.catalog(env={"ANTHROPIC_API_KEY": "sk-ant-ENV-SECRET-1234567890"})
        payload = catalog.snapshot()
        claude = self.by_id(payload)["claude"]
        self.assertEqual(claude["status"], "ready")
        self.assertEqual(claude["auth"]["method"], "environment")
        self.assertIn("env:ANTHROPIC_API_KEY", claude["auth"]["sources"])
        self.assertNotIn("sk-ant-ENV-SECRET-1234567890", json.dumps(payload))

    def test_dsh_uses_ark_assistant_key_presence(self):
        catalog = self.catalog(assistant_key=lambda: (True, "ark_assistant"))
        dsh = self.by_id(catalog.snapshot())["dsh"]
        self.assertEqual(dsh["status"], "ready")
        self.assertEqual(dsh["auth"]["method"], "ark_assistant")

    def test_version_output_is_sanitized_and_bounded(self):
        def noisy_runner(program):
            return {"returncode": 0, "stdout": "claude 9.9.9 sk-abcdefghijklmnopqrstuvwxyz\nsecond",
                    "stderr": "", "error": ""}

        catalog = ProviderCatalog(
            [PRESETS[0]], env={}, home=self.home, which=self.which, run=noisy_runner,
            assistant_key=lambda: (False, "ark_assistant"), assistant_model=lambda: "",
        )
        claude = self.by_id(catalog.snapshot())["claude"]
        self.assertNotIn("sk-abcdefghijklmnopqrstuvwxyz", json.dumps(claude))
        self.assertIn("[redacted]", claude["version"])
        self.assertLessEqual(len(claude["version"]), 120)

    def test_version_probe_failure_does_not_claim_missing(self):
        def failing_runner(program):
            return {"returncode": 3, "stdout": "", "stderr": "nope", "error": ""}

        catalog = ProviderCatalog(
            [PRESETS[0]], env={"ANTHROPIC_API_KEY": "set"}, home=self.home, which=self.which,
            run=failing_runner, assistant_key=lambda: (False, "ark_assistant"),
            assistant_model=lambda: "",
        )
        claude = self.by_id(catalog.snapshot())["claude"]
        self.assertTrue(claude["installed"])
        self.assertEqual(claude["status"], "ready")
        self.assertIn("version probe exited with 3", claude["version_error"])

    def test_capabilities_expose_models_efforts_modes_and_no_approval_flow(self):
        presets = [dict(PRESETS[0], models=["sonnet"], efforts=["low", "high"])]
        catalog = ProviderCatalog(
            presets, env={}, home=self.home, which=self.which, run=self.runner,
            assistant_key=lambda: (False, "ark_assistant"), assistant_model=lambda: "",
        )
        claude = self.by_id(catalog.snapshot())["claude"]
        capabilities = claude["capabilities"]
        self.assertEqual(capabilities["models"], ["sonnet"])
        self.assertEqual(capabilities["efforts"], ["low", "high"])
        self.assertEqual(capabilities["permission_modes"], ["read_only", "full_access"])
        self.assertEqual(capabilities["streaming"], "line_buffered")
        self.assertFalse(capabilities["approval"]["supported"])

    def test_capabilities_reflect_merged_permission_and_approval_modes(self):
        from arkstudio.sessions import _normalize_preset

        normalized = _normalize_preset({
            "id": "claude",
            "label": "Claude Code",
            "command": ["claude", "-p", "{prompt}"],
            "prompt_mode": "arg",
            "followup": True,
            "supports_approval": True,
            "models": ["sonnet"],
        })
        catalog = ProviderCatalog(
            [normalized], env={}, home=self.home, which=self.which, run=self.runner,
            assistant_key=lambda: (False, "ark_assistant"), assistant_model=lambda: "",
        )
        capabilities = self.by_id(catalog.snapshot())["claude"]["capabilities"]
        self.assertIn("ask", capabilities["permission_modes"])
        self.assertIn("accept_edits", capabilities["permission_modes"])
        self.assertTrue(capabilities["approval"]["supported"])
        self.assertEqual(capabilities["approval"]["modes"], ["ask", "accept_edits"])
        self.assertEqual(capabilities["approval"]["protocol"], "ark-permission-v1")
        self.assertIn("stdin_approvals", capabilities["permission_enforcement"])

    def test_cache_and_manual_refresh(self):
        catalog = self.catalog(ttl=60)
        first = catalog.snapshot()
        second = catalog.snapshot()
        self.assertFalse(first["cached"])
        self.assertTrue(second["cached"])
        calls_before = len(self.run_calls)
        refreshed = catalog.refresh()
        self.assertFalse(refreshed["cached"])
        self.assertGreater(len(self.run_calls), calls_before)
        self.assertEqual(len(refreshed["providers"]), len(PRESETS))

    def test_refresh_forces_new_probe_even_within_ttl(self):
        catalog = self.catalog(ttl=600)
        catalog.snapshot()
        before = len(self.run_calls)
        catalog.refresh()
        self.assertGreater(len(self.run_calls), before)


class ProviderApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        root = Path(cls.tmp.name)
        home = root / "home"
        home.mkdir()
        presets = [preset("claude", "Claude Code", ["claude", "-p", "{prompt}"]),
                   preset("missing", "Missing", ["definitely-not-installed", "{prompt}"])]
        catalog = ProviderCatalog(
            presets, env={"ANTHROPIC_API_KEY": "sk-ant-API-TEST-SECRET"}, home=home, ttl=60,
            which=lambda program: "claude" if program == "claude" else None,
            run=lambda program: {"returncode": 0, "stdout": "claude 1.0.0", "stderr": "", "error": ""},
            assistant_key=lambda: (False, "ark_assistant"),
            assistant_model=lambda: "",
        )
        cls.app = App(root / "projects", seed_demo=False,
                      task_sessions_root=root / "sessions", task_presets=presets,
                      provider_catalog=catalog)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()

    def request(self, path, method="GET", body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read()

    def test_get_catalog_and_manual_refresh(self):
        status, raw = self.request("/api/providers")
        self.assertEqual(status, 200)
        payload = json.loads(raw)
        providers = {item["id"]: item for item in payload["providers"]}
        self.assertEqual(providers["claude"]["status"], "ready")
        self.assertEqual(providers["missing"]["status"], "missing")
        self.assertNotIn(b"sk-ant-API-TEST-SECRET", raw)
        self.assertFalse(payload["cached"])

        cached_status, cached_raw = self.request("/api/providers")
        self.assertEqual(cached_status, 200)
        self.assertTrue(json.loads(cached_raw)["cached"])

        refresh_status, refresh_raw = self.request("/api/providers/refresh", "POST", {})
        self.assertEqual(refresh_status, 200)
        refreshed = json.loads(refresh_raw)
        self.assertFalse(refreshed["cached"])
        self.assertTrue(refreshed["last_refresh"])
        self.assertNotIn(b"sk-ant-API-TEST-SECRET", refresh_raw)

    def test_presets_include_provider_state(self):
        status, raw = self.request("/api/agent-sessions/presets")
        self.assertEqual(status, 200)
        presets = {item["id"]: item for item in json.loads(raw)["presets"]}
        self.assertEqual(presets["claude"]["provider_status"], "ready")
        self.assertEqual(presets["missing"]["provider_status"], "missing")
        self.assertTrue(presets["claude"]["provider_auth"]["authenticated"])


if __name__ == "__main__":
    unittest.main()
