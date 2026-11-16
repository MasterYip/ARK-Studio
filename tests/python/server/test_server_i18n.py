"""Server-originated text shown in the web UI has Chinese translations.

The server stays English; web/js/i18n_server.js holds EN templates plus ZH,
and the client maps server English to the UI language (i18n.js
localizeServer / serverErrorText).  These tests fail when a human-facing
server message or payload text has no EN/ZH entry there, so a new English
string cannot silently reach the Chinese UI.
"""
import ast
import importlib.util
import re
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
_spec = importlib.util.spec_from_file_location("i18n_audit", ROOT / "scripts" / "dev" / "i18n_audit.py")
audit_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(audit_mod)

from arkstudio import providers, skills  # noqa: E402
from arkstudio.skills import setup  # noqa: E402

# Payload fields the UI shows verbatim, per server module.
INFO_FIELDS = {
    "arkstudio/providers.py": {"detail"},
    "arkstudio/gpu_probe.py": {"hint", "note"},
    "arkstudio/workspace.py": {"reason", "hint", "message"},
    "arkstudio/mobile.py": {"warning", "note", "secure_context_note", "pwa_note",
                            "reachability_note"},
}


def _payload_texts(path: Path, fields: set[str]) -> list[tuple[int, str]]:
    """English templates assigned to ``fields`` in dict literals or ``x["field"] = ...``."""
    tree = ast.parse(path.read_text(encoding="utf-8"))
    out = []
    for node in ast.walk(tree):
        pairs = []
        if isinstance(node, ast.Dict):
            pairs = [(k, v) for k, v in zip(node.keys, node.values)
                     if isinstance(k, ast.Constant) and k.value in fields]
        elif isinstance(node, ast.Assign) and len(node.targets) == 1 \
                and isinstance(node.targets[0], ast.Subscript) \
                and isinstance(node.targets[0].slice, ast.Constant) \
                and node.targets[0].slice.value in fields:
            pairs = [(node.targets[0].slice, node.value)]
        for _, value in pairs:
            options = [value.body, value.orelse] if isinstance(value, ast.IfExp) else [value]
            for option in options:
                template = audit_mod._py_template(option)
                if template and len(re.findall(r"[A-Za-z]{2,}", template.replace("{v}", " "))) >= 2:
                    out.append((option.lineno, template))
    return out


class ServerI18nTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        dicts = audit_mod.load_dicts()
        cls.en, cls.zh = dicts["EN"], dicts["ZH"]
        cls.matcher = audit_mod.ServerMatcher(cls.en)
        cls.result = audit_mod.server_audit(cls.en, cls.zh)

    def assertTranslated(self, text: str, prefix: str = "server.", where: str = ""):
        key = self.matcher.match(text, prefix)
        self.assertIsNotNone(key, f"no {prefix}* template in web/js/i18n_server.js for "
                                  f"{text!r} {where}".rstrip())
        self.assertIn(key, self.zh, f"{key} has no ZH translation")

    # ----------------------------------------------------------------- errors
    def test_every_human_facing_server_error_is_translated(self):
        listing = "\n".join(f'{i["file"]}:{i["line"]} {i["template"]!r}'
                            for i in self.result["server_untranslated"][:40])
        self.assertEqual(self.result["server_untranslated"], [],
                         "Add an EN template + ZH entry under server.error.* in "
                         "web/js/i18n_server.js (python scripts/dev/i18n_audit.py lists them):\n" + listing)

    def test_api_errors_raised_in_server_py_are_mapped(self):
        raised = [item for item in audit_mod.server_error_messages()
                  if item["file"] == "arkstudio/server.py" and item["kind"] == "ApiError"]
        self.assertGreater(len(raised), 100)
        for item in raised:
            self.assertTranslated(item["sample"], "server.error.", f'({item["file"]}:{item["line"]})')

    def test_explicit_error_codes_render_by_code(self):
        # A specific code whose EN text is the server message renders without matching.
        for code in ("MOBILE_READ_ONLY", "PAIRING_USED", "SETUP_DIGEST", "WORKTREE_DIRTY",
                     "PROJECT_CONFIRM_REQUIRED", "AUTH_CSRF"):
            self.assertIn(f"server.error.{code}", self.en)
            self.assertIn(f"server.error.{code}", self.zh)

    def test_new_untranslated_server_message_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, "fresh.py").write_text(
                "class FreshError(ValueError):\n    pass\n\n"
                "def go(name):\n"
                "    raise FreshError(f'the frobnicator for {name} melted unexpectedly')\n"
                "    raise FreshError(f'unit not found: {name}')\n",
                encoding="utf-8")
            found = audit_mod.server_error_messages(Path(tmp))
        self.assertEqual(len(found), 2)
        self.assertIsNone(self.matcher.match(found[0]["sample"], "server.error."))
        self.assertIsNotNone(self.matcher.match(found[1]["sample"], "server.error."))

    def test_zh_placeholders_exist_in_en(self):
        self.assertEqual(self.result["server_placeholder_mismatch"], [])

    # ------------------------------------------------------------ payload text
    def test_payload_notes_and_hints_are_translated(self):
        for rel, fields in INFO_FIELDS.items():
            texts = _payload_texts(ROOT / rel, fields)
            self.assertTrue(texts, rel)
            for line, template in texts:
                self.assertTranslated(audit_mod._sample(template), "server.", f"({rel}:{line})")

    def test_provider_hints_are_translated(self):
        for text in list(providers.AUTH_DETAIL.values()) + list(providers.NO_LIST_COMMAND.values()):
            self.assertTranslated(text, "server.provider.")
        for text in ("codex was not found on PATH.",
                     "Codex is installed and credentials were detected. Version probe exited with 2.",
                     "Built-in list; the provider is not installed."):
            self.assertTranslated(text, "server.provider.")

    def test_gpu_vendor_messages_are_translated(self):
        tree = ast.parse((ROOT / "vendor" / "gpu_server_probe" / "ssh.py").read_text(encoding="utf-8"))
        hint = next(audit_mod._py_template(node.value) for node in ast.walk(tree)
                    if isinstance(node, ast.Assign) and getattr(node.targets[0], "id", "") == "MISSING_SSH_HINT")
        self.assertTranslated(hint, "server.gpu.")
        for text in ("SSH authentication failed; check your SSH agent or ~/.ssh/config",
                     "SSH connection failed", "remote command exited with status 3",
                     "SSH to gpu1 timed out after 25s", "line 4: duplicate key 'host'"):
            self.assertTranslated(text, "server.gpu.")

    def test_setup_summary_and_readiness_are_translated(self):
        profile, _, _ = setup.normalize_profile({
            "repositories": [{"id": "main", "path": "."}],
            "skipped": ["compute"],
        })
        record = {"profile": profile, "state": "draft", "digest": "abc"}
        for line in setup.render_summary(record).splitlines():
            if not line.strip() or line.startswith("|"):
                continue  # tables carry ids and values
            self.assertTranslated(line, "server.setup.", "(setup summary line)")
        for item in setup.readiness(profile, "draft")["blocked"]:
            self.assertTranslated(item["reason"], "server.", "(readiness reason)")
        for question in setup.questions({}):
            self.assertTranslated(question["prompt"], "server.setup.question.")
            self.assertTranslated(question["blocks"], "server.setup.blocks.")
        for _, reason in setup.SKIP_EFFECT.values():
            self.assertTranslated(reason, "server.setup.skip.")
        for note in setup.POLICY_NOTES.values():
            self.assertTranslated(note, "server.setup.policy.")

    def test_multi_user_setup_text_is_translated(self):
        from arkstudio.skills import setup_layers
        with tempfile.TemporaryDirectory() as tmp:
            project = Path(tmp) / "p.ark"
            viewer = setup_layers.Viewer(user="alice", host_id="elsewhere1")
            setup_layers.save_layer(project, {"resources": [
                {"id": "s", "kind": "storage", "path_or_ref": "/s"}]}, viewer=viewer,
                layer="user", actor="agent:alice")
            admin = setup_layers.Viewer(user="owner", admin=True, host_id="elsewhere1")
            setup_layers.save_layer(project, {"resources": [
                {"id": "code", "kind": "repository", "path_or_ref": "/c"}]}, viewer=admin,
                layer="project", actor="agent:owner")
            data = setup_layers.payload(project, setup_layers.Viewer(user="alice"))
        lines = data["summary_markdown"].splitlines() + data["user_summary_markdown"].splitlines()
        for line in lines:
            if line.strip() and not line.startswith("|"):
                self.assertTranslated(line, "server.setup.", "(multi-user summary line)")
        for item in data["readiness"]["blocked"]:
            self.assertTranslated(item["reason"], "server.", "(readiness reason)")

    def test_skill_catalogue_display_text_is_translated(self):
        for skill in skills.catalog_payload()["skills"]:
            prefix = f'server.skill.{skill["id"]}'
            self.assertEqual(self.en.get(f"{prefix}.name"), skill["name"])
            self.assertEqual(self.en.get(f"{prefix}.summary"), skill["summary"])
            self.assertIn(f"{prefix}.name", self.zh)
            self.assertIn(f"{prefix}.summary", self.zh)
            for template in skill.get("templates") or []:
                for field in ("title", "description"):
                    key = f'{prefix}.template.{template["id"]}.{field}'
                    self.assertEqual(self.en.get(key), template[field], key)
                    self.assertIn(key, self.zh)


if __name__ == "__main__":
    unittest.main()
