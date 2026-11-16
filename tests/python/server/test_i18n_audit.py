"""Regression guard for UI localisation (scripts/dev/i18n_audit.py).

Fails when user-visible English literals bypass t(), when the EN and ZH
dictionaries drift apart, or when code calls t() with a key that either
dictionary lacks.
"""
import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
_spec = importlib.util.spec_from_file_location("i18n_audit", ROOT / "scripts" / "dev" / "i18n_audit.py")
audit_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(audit_mod)

# Unwrapped user-visible literals left after the i18n pass.  Lower it when you
# translate more; never raise it to make a new English string pass.
BASELINE_UNWRAPPED = 0


def scan_snippet(source: str) -> audit_mod.FileScan:
    fs = audit_mod.FileScan(ROOT / "web" / "js" / "snippet.js", source)
    audit_mod.scan_tokens(fs, audit_mod.tokenize(source)[0])
    return fs


class I18nAuditTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.result = audit_mod.audit()

    def test_no_new_unwrapped_literals(self):
        found = self.result["findings"]
        listing = "\n".join(f'{f["file"]}:{f["line"]} [{f["ctx"]}] {f["text"]!r}' for f in found[:40])
        self.assertLessEqual(
            len(found), BASELINE_UNWRAPPED,
            "User-visible English must go through t() (or carry // i18n-ignore with a reason). "
            "Run: python scripts/dev/i18n_audit.py --list\n" + listing)

    def test_en_and_zh_have_the_same_keys(self):
        self.assertEqual(self.result["missing_in_zh"], [])
        self.assertEqual(self.result["missing_in_en"], [])
        self.assertEqual(self.result["en_keys"], self.result["zh_keys"])

    def test_every_key_used_in_code_exists(self):
        where = self.result["where"]
        self.assertEqual({k: where[k] for k in self.result["used_missing_en"]}, {})
        self.assertEqual({k: where[k] for k in self.result["used_missing_zh"]}, {})

    def test_detector_still_flags_ui_literals(self):
        fs = scan_snippet(
            'h("button", { title: "Open details" }, "Copy subtree");\n'
            'items.push({ label: ok ? "Add child" : "Add sibling" });\n'
            'toast(`Copied ${n} nodes`);\n'
            'el.textContent = "Pairing failed.";\n'
            'const MSG = "Something went wrong here";\n')
        texts = sorted(f["text"] for f in fs.findings)
        self.assertEqual(texts, sorted([
            "Open details", "Copy subtree", "Add child", "Add sibling", "Copied {} nodes",
            "Pairing failed.", "Something went wrong here"]))

    def test_detector_ignores_keys_code_and_marked_literals(self):
        fs = scan_snippet(
            'h("button", { title: t("graph.menu.copy"), class: "icon-btn sm" }, t("action.copy"));\n'
            'console.warn("Could not reach the server");\n'
            'h("kbd", null, "Ctrl+K");\n'
            'h("span", null, "ARK Studio");\n'
            'if (mode === "Full access") go();\n'
            'h("span", null, "Referenced ARK nodes:"); // i18n-ignore: agent clipboard text\n')
        self.assertEqual(fs.findings, [])
        self.assertIn("graph.menu.copy", fs.keys)
        self.assertIn("action.copy", fs.keys)


if __name__ == "__main__":
    unittest.main()
