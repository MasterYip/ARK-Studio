import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from arkstudio import assistant_config


class AssistantConfigTests(unittest.TestCase):
    def test_private_config_round_trip_and_redaction(self):
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {"ARK_PRIVATE_DIR": tmp}):
            saved = assistant_config.save({
                "base_url": "https://example.test/v1",
                "model": "model-x",
                "reasoning_effort": "max",
                "api_key": "sk-test-12345678",
            })
            self.assertTrue(saved["configured"])
            self.assertEqual(saved["base_url"], "https://example.test/v1")
            self.assertEqual(saved["model"], "model-x")
            self.assertEqual(saved["reasoning_effort"], "max")
            self.assertEqual(saved["key_hint"], "…5678")
            self.assertNotIn("sk-test-12345678", json.dumps(saved))
            path = Path(tmp) / "assistant.json"
            self.assertTrue(path.is_file())
            loaded = assistant_config.load()
            self.assertEqual(loaded["api_key"], "sk-test-12345678")
            self.assertEqual(loaded["model"], "model-x")

    def test_validation_and_clear(self):
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {"ARK_PRIVATE_DIR": tmp}):
            with self.assertRaises(ValueError):
                assistant_config.save({"base_url": "file:///etc/passwd"})
            with self.assertRaises(ValueError):
                assistant_config.save({"reasoning_effort": "impossible"})
            assistant_config.save({"api_key": "sk-test-1234"})
            cleared = assistant_config.save({"clear_key": True})
            self.assertFalse(cleared["has_key"])
