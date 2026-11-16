"""Shared UI helpers that a web module calls must be imported or defined there.

Regression: app.js called openModal() without importing it, so the Export
dialog (command palette and mobile menu) threw ReferenceError since the
first commit.
"""
import re
import unittest
from pathlib import Path

WEB = Path(__file__).resolve().parents[3] / "web" / "js"
HELPERS = ("openModal", "toast", "copyText", "confirmDialog", "richText", "openImageLightbox")


class HelperImportTests(unittest.TestCase):
    def test_called_helpers_are_imported_or_defined(self):
        problems = []
        for path in sorted(WEB.rglob("*.js")):
            text = path.read_text(encoding="utf-8")
            # Ignore comments ("matching richText()." in a doc comment is not a call).
            # Block comments only when they start a line ("/**", " * ..."), so
            # "/*" inside a string or glob ("**/*.js") is left alone.
            text = re.sub(r"(?ms)^\s*/\*.*?\*/", "", text)
            text = re.sub(r"(?m)^\s*//.*$", "", text)
            imports = " ".join(re.findall(r"import\s*\{([^}]*)\}\s*from", text, re.S))
            imported = {name.strip().split(" as ")[-1] for name in imports.split(",")}
            for helper in HELPERS:
                if not re.search(rf"(?<![\w.]){helper}\(", text):
                    continue
                defined = re.search(rf"(function\s+{helper}\b|(const|let|var)\s+{helper}\b|export\s+(async\s+)?function\s+{helper}\b)", text)
                param = re.search(rf"[({{,]\s*{helper}\s*[,)}}=]", text)
                if helper not in imported and not defined and not param:
                    problems.append(f"{path.relative_to(WEB)} calls {helper}() without importing it")
        self.assertEqual([], problems)


if __name__ == "__main__":
    unittest.main()
