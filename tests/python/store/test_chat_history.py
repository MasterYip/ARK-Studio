"""Per-user Assistant chat history: threads, clearing, migration."""
import sqlite3
import tempfile
import unittest
from pathlib import Path

from arkstudio import db
from tests.python.store.test_store import make_store


class ChatHistoryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_threads_are_per_user_and_clear_only_affects_that_user(self):
        legacy = self.store.add_chat("user", "shared before per-user history")
        self.store.add_chat("user", "alice asks", user="alice")
        self.store.add_chat("assistant", "answer to alice", user="alice")
        self.store.add_chat("user", "bob asks", user="bob")
        alice = [m["content"] for m in self.store.chat_history(user="alice")]
        bob = [m["content"] for m in self.store.chat_history(user="bob")]
        self.assertEqual(alice, [legacy["content"], "alice asks", "answer to alice"])
        self.assertEqual(bob, [legacy["content"], "bob asks"])
        # Alice clears: her rows go, the shared row is hidden for her only.
        self.assertEqual(self.store.clear_chat(user="alice"), 3)
        self.assertEqual(self.store.chat_history(user="alice"), [])
        self.assertEqual([m["content"] for m in self.store.chat_history(user="bob")],
                         [legacy["content"], "bob asks"])
        # New messages after a clear show up again; clearing twice is fine.
        self.store.add_chat("user", "alice again", user="alice")
        self.assertEqual([m["content"] for m in self.store.chat_history(user="alice")], ["alice again"])
        self.assertEqual(self.store.clear_chat(user="alice"), 1)
        self.assertEqual(self.store.clear_chat(user="alice"), 0)
        # user=None is the maintenance view of every message.
        self.assertEqual(len(self.store.chat_history()), 2)

    def test_old_database_gains_the_user_column(self):
        path = Path(self.tmp.name) / "old.sqlite3"
        con = sqlite3.connect(path)
        con.executescript("""
            CREATE TABLE chat_messages(id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL,
              role TEXT NOT NULL, content TEXT NOT NULL, tools_json TEXT NOT NULL DEFAULT '[]',
              meta_json TEXT NOT NULL DEFAULT '{}');
            INSERT INTO chat_messages(at, role, content) VALUES('2026-01-01T00:00:00+00:00', 'user', 'old');
        """)
        con.commit()
        con.close()
        upgraded = db.connect(path)
        try:
            columns = {row[1] for row in upgraded.execute("PRAGMA table_info(chat_messages)")}
            self.assertIn("user", columns)
            self.assertEqual(upgraded.execute("SELECT user FROM chat_messages").fetchone()[0], "")
            self.assertEqual(db.meta_get(upgraded, "schema_version"), str(db.SCHEMA_VERSION))
        finally:
            upgraded.close()


if __name__ == "__main__":
    unittest.main()
