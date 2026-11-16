"""Card and list excerpts are plain text, not raw Markdown."""
import unittest

from arkstudio.store import excerpt

NL = chr(10)


class ExcerptTests(unittest.TestCase):
    def test_strips_markdown_syntax_and_blocks(self):
        body = NL.join([
            "Start here.", "", "## What ARK is for", "", "ARK keeps **one** `project`.",
            "", "```mermaid", "flowchart LR", "A-->B", "```", "",
            "- see [the docs](docs/x.md)", "1. first step", "> a quote", "![Figure](a.png)",
        ])
        text = excerpt(body)
        self.assertEqual(text, "Start here. What ARK is for ARK keeps one project. "
                               "see the docs first step a quote Figure")
        for marker in ("##", "**", "`", "](", "flowchart", "A-->B"):
            self.assertNotIn(marker, text)

    def test_truncates_with_ellipsis_and_handles_empty(self):
        self.assertEqual(excerpt(""), "")
        long = excerpt("word " * 100, limit=20)
        self.assertTrue(long.endswith("…"))
        self.assertLessEqual(len(long), 21)

    def test_html_fence_is_dropped(self):
        self.assertEqual(excerpt(NL.join(["Chart below.", "```html", "<script>x()</script>", "```"])),
                         "Chart below.")


if __name__ == "__main__":
    unittest.main()
