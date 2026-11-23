"""go_only.FIXES predicates stay narrow (no arkstudio import)."""
from __future__ import annotations

import json
import re
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parent))
import go_only  # noqa: E402


def resp(status: int, body: bytes):
    return SimpleNamespace(status=status, body=body, headers={})


PY_INF = b'{"error": "Internal error: cannot convert float infinity to integer"}'


class FixesTest(unittest.TestCase):
    def test_resources_nonfinite(self):
        fix = go_only.fix_for("GET", "/api/resources/graph?x=1", resp(500, PY_INF), resp(200, b"{}"))
        self.assertEqual(fix.id, "resources-graph-nonfinite-gpu-memory")
        self.assertIsNotNone(go_only.fix_for("HEAD", "/api/resources/graph", resp(500, PY_INF), resp(200, b"")))

    def test_narrow(self):
        self.assertIsNone(go_only.fix_for("GET", "/api/resources/graph", resp(500, b"other"), resp(200, b"{}")))
        self.assertIsNone(go_only.fix_for("GET", "/api/resources/graph", resp(500, PY_INF), resp(500, PY_INF)))
        self.assertIsNone(go_only.fix_for("GET", "/api/resources", resp(500, PY_INF), resp(200, b"{}")))
        self.assertIsNone(go_only.fix_for("POST", "/api/resources/graph", resp(500, PY_INF), resp(200, b"{}")))

    def test_entries_documented(self):
        for fix in go_only.FIXES:
            self.assertTrue(fix.id and fix.go_file and fix.note, fix)
            self.assertTrue((Path(__file__).resolve().parents[3] / fix.go_file.split(" ")[0]).is_file(), fix.go_file)


REPO = Path(__file__).resolve().parents[3]


class ApkReleaseTest(unittest.TestCase):
    """T-0029: Android APKs from the latest GitHub release (app_release.go)."""

    PAGE = "https://github.com/MasterYip/ARK-Studio/releases/latest"
    PY_404 = (b'{"ok": false, "error": {"status": 404, "code": "APK_NOT_FOUND", '
              b'"message": "ARK Studio (native) is not available on this server."}}')

    @staticmethod
    def redirect(location):
        return SimpleNamespace(status=302, body=b"", headers={"location": location})

    def test_download_redirect(self):
        for path in go_only.APK_PATHS:
            self.assertEqual(go_only.fix_for("GET", path, resp(404, self.PY_404), self.redirect(self.PAGE)).id,
                             "apk-download-redirect")
            self.assertIsNotNone(go_only.fix_for("HEAD", path, resp(404, b""), self.redirect(self.PAGE)))

    def test_download_redirect_narrow(self):
        path = "/download/ark.apk"
        self.assertIsNone(go_only.fix_for("GET", path, resp(404, b'{"code": "OTHER"}'), self.redirect(self.PAGE)))
        self.assertIsNone(go_only.fix_for("GET", path, resp(404, self.PY_404), self.redirect("/elsewhere")))
        self.assertIsNone(go_only.fix_for("GET", path, resp(404, self.PY_404), resp(404, self.PY_404)))
        self.assertIsNone(go_only.fix_for("GET", path, resp(200, b"APK"), self.redirect(self.PAGE)))
        self.assertIsNone(go_only.fix_for("GET", "/download/other.apk", resp(404, self.PY_404),
                                          self.redirect(self.PAGE)))

    @classmethod
    def apps(cls, go: bool, **change):
        out = {"ok": True, "apps": {}}
        for key, file, app_id, title in (("native", "ARK-Studio-android.apk", "com.masteryip.arkstudio",
                                          "ARK Studio (native)"),
                                         ("webapp", "ARK-Studio-webapp.apk", "com.masteryip.arkstudio.web",
                                          "ARK Studio Web")):
            app = {"available": False, "url": f"./download/{file}", "file": file, "app_id": app_id,
                   "title": title, "size": 0, "updated": None}
            if go and key == "webapp":
                continue  # Go no longer lists the WebView app
            if go:
                app.update(available=True, url=cls.PAGE, file="", direct=False, version="", qr_svg="<svg/>",
                           local=False, local_url=f"./download/{file}")
                app.update(change)
            out["apps"][key] = app
        if go:
            out.update(version="", release_url=cls.PAGE)
        return out

    def test_mobile_apps(self):
        path = "/api/mobile/apps"
        py = resp(200, body(self.apps(False)))
        self.assertEqual(go_only.fix_for("GET", path, py, resp(200, body(self.apps(True)))).id,
                         "mobile-apps-github-release")
        self.assertIsNone(go_only.fix_for("GET", path, py, resp(200, body(self.apps(True, url="./x")))))
        self.assertIsNone(go_only.fix_for("GET", path, py, resp(200, body(self.apps(True, title="X")))))
        self.assertIsNone(go_only.fix_for("GET", path, py, resp(200, body(self.apps(True, extra=1)))))
        self.assertIsNone(go_only.fix_for("GET", path, py, resp(500, body(self.apps(True)))))
        self.assertIsNone(go_only.fix_for("GET", path, py, resp(200, body(self.apps(False)))))


class StorageTest(unittest.TestCase):
    """GPU server storage (ark_studio/server/internal/gpu/storage.go) is a Go-only addition."""

    ITEM = {"mount": "/data", "used_bytes": 9, "total_bytes": 10, "free_bytes": 1, "percent": 90.0, "data": True}

    def test_probe_and_dashboard_strip_storage(self):
        probe = {"enabled": True, "servers": [{"server": "a", "ok": True, "storage": [self.ITEM]},
                                              {"server": "b", "ok": False}], "ranked": []}
        want = {"enabled": True, "servers": [{"server": "a", "ok": True}, {"server": "b", "ok": False}], "ranked": []}
        strip = go_only.augmented("/api/gpu/probe")
        self.assertEqual(json.loads(strip(json.dumps(probe).encode())), want)
        dash = go_only.augmented("/api/gpu/dashboard?project=x")
        self.assertEqual(json.loads(dash(json.dumps({"probe": probe, "jobs": []}).encode())),
                         {"probe": want, "jobs": []})
        self.assertEqual(dash(b'{"probe": null}'), b'{"probe": null}')
        self.assertEqual(strip(b"not json"), b"not json")

    def test_graph_strips_device_storage(self):
        graph = {"devices": [{"id": "gpu:a", "storage": [self.ITEM]}, {"id": "ark"}], "resources": []}
        out = json.loads(go_only.augmented("/api/resources/graph")(json.dumps(graph).encode()))
        self.assertEqual(out, {"devices": [{"id": "gpu:a"}, {"id": "ark"}], "resources": []})

    def test_ssh_suffix_matches_go(self):
        source = (REPO / "ark_studio/server/internal/gpu/storage.go").read_text(encoding="utf-8")
        decl = source.split("const StorageCommand = ", 1)[1].split("\n\n", 1)[0]
        decl = decl.replace('" + storageMarker + "', "__ARK_DF__")
        command = "".join(re.findall(r'"([^"]*)"', decl))
        self.assertEqual("\n" + command, go_only.GO_ONLY_SSH_SUFFIX)
        self.assertIn("` + StorageCommand + `\ntrue`", (REPO / "ark_studio/server/internal/gpu/ssh.go").read_text(encoding="utf-8"))
        argv = ["-p", "22", "--", "a.fake", "bash", "-lc", "'echo __ARK_LOAD__\ndone" + go_only.GO_ONLY_SSH_SUFFIX + "\ntrue'"]
        self.assertEqual(go_only.ssh_call_canonical(argv)[-1], "'echo __ARK_LOAD__\ndone\ntrue'")


def err(status: int, code: str, message: str) -> bytes:
    return json.dumps({"ok": False, "error": {"status": status, "code": code, "message": message}}).encode()


def body(obj) -> bytes:
    return json.dumps(obj).encode()


class GpuScopeTest(unittest.TestCase):
    """T-0018 (ark_studio/server/internal/gpu/scope.go): project scope and member view."""

    def test_not_registered(self):
        py = resp(400, err(400, "unknown_server", "Unknown server(s): zzz"))
        go = resp(404, err(404, "GPU_SERVER_NOT_REGISTERED", "GPU server 'zzz' is not registered in this project."))
        for method, path in (("POST", "/api/gpu/probe"), ("POST", "/api/gpu/dispatch/prepare"),
                             ("POST", "/api/gpu/jobs"), ("POST", "/api/task-agent/v1/gpu/jobs")):
            self.assertEqual(go_only.fix_for(method, path, py, go).id, "gpu-server-not-registered")
        queue = resp(400, err(400, "queue_invalid", "unknown server 'nope'"))
        self.assertIsNotNone(go_only.fix_for("POST", "/api/gpu/jobs", queue, go))
        # Narrow: another 400, another 404 code, another route, another status.
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", resp(400, err(400, "invalid_request", "bad")), go))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py, resp(404, err(404, "GPU_JOB_NOT_FOUND", "x"))))
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/jobs", py, go))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py,
                                          resp(403, err(403, "GPU_SERVER_NOT_REGISTERED", "x"))))

    PY_SERVERS = {"available": True, "enabled": True, "config_path": "/d1/private/gpu_servers.yaml",
                  "servers": [{"name": "a", "host": "a.fake", "port": 2222, "user": "replay", "tags": ["4090"],
                               "note": "n", "enabled": True, "timeout_seconds": 8}],
                  "count": 1, "defaults": {"port": 2222}, "checked_at": "t1"}
    GO_SERVERS = {"available": True, "enabled": True,
                  "servers": [{"name": "a", "tags": ["4090"], "enabled": True}],
                  "count": 1, "checked_at": "t2", "registration": {"registered": ["a"]}}

    def test_member_view(self):
        py, go = resp(200, body(self.PY_SERVERS)), resp(200, body(self.GO_SERVERS))
        self.assertEqual(go_only.fix_for("GET", "/api/gpu/servers", py, go).id, "gpu-member-view")
        # The admin/local view (address kept) is not a member view.
        local = dict(self.PY_SERVERS, checked_at="t2")
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/servers", py, resp(200, body(local))))
        # Narrow: a config path left anywhere, an extra member, a lost server, a changed flag.
        for bad in (dict(self.GO_SERVERS, config_path="/x"), dict(self.GO_SERVERS, extra=1),
                    dict(self.GO_SERVERS, servers=[]),
                    dict(self.GO_SERVERS, servers=[{"name": "a", "tags": ["4090"], "enabled": False}]),
                    dict(self.GO_SERVERS, enabled=False)):
            self.assertIsNone(go_only.fix_for("GET", "/api/gpu/servers", py, resp(200, body(bad))), bad)
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/servers", resp(500, b"{}"), go))
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/jobs", py, go))

    def test_member_dispatch_and_graph(self):
        py = {"server": {"name": "a", "host": "a.fake", "port": 22, "user": "u", "tags": [], "enabled": True},
              "transport": {"host": "a.fake", "port": 22, "user": "u", "auth": "agent"},
              "ssh_preview": "ssh -p 22 u@a.fake", "config_path": "/d1/x", "request": {"server": "a"}}
        go = {"server": {"name": "a", "tags": [], "enabled": True}, "transport": {"auth": "agent"},
              "request": {"server": "a"}}
        self.assertIsNotNone(go_only.fix_for("POST", "/api/gpu/dispatch/prepare", resp(200, body(py)),
                                             resp(200, body(go))))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/dispatch/prepare", resp(200, body(py)),
                                          resp(200, body(dict(go, ssh_preview="ssh a")))))
        gpy = {"devices": [{"id": "gpu:a", "detail": {"host": "a.fake", "user": "u", "tags": []},
                            "status": {"state": "online"}}]}
        ggo = {"devices": [{"id": "gpu:a", "detail": {"host": None, "user": None, "tags": []},
                            "status": {"state": "online"}}]}
        self.assertIsNotNone(go_only.fix_for("GET", "/api/resources/graph", resp(200, body(gpy)), resp(200, body(ggo))))
        self.assertIsNone(go_only.fix_for("GET", "/api/resources/graph", resp(200, body(gpy)),
                                          resp(200, body({"devices": []}))))

    def test_member_config_error(self):
        py = resp(403, err(403, "disabled", "GPU probing is disabled by default. Set 'enabled: true' in /d1/g.yaml"))
        go = resp(403, err(403, "disabled", go_only.GPU_MEMBER_ERRORS["disabled"]))
        self.assertEqual(go_only.fix_for("POST", "/api/gpu/probe", py, go).id, "gpu-member-config-error")
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py, resp(403, err(403, "disabled", "other"))))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py,
                                          resp(409, err(409, "disabled", go_only.GPU_MEMBER_ERRORS["disabled"]))))

    def test_discover_names_only(self):
        def found(note):
            item = {"id": "a", "kind": "ssh", "gpu_server": "a", "use": "ask"}
            if note is not None:
                item["note"] = note
            return {"ok": True, "draft": {"compute": [{"id": "local", "kind": "local", "note": "Windows host"}, item]}}
        path = "/api/agent/v1/setup/discover"
        py, go = resp(200, body(found("4090, x"))), resp(200, body(found("")))
        self.assertEqual(go_only.fix_for("POST", path, py, go).id, "setup-discover-gpu-names-only")
        self.assertIsNotNone(go_only.fix_for("POST", path, py, resp(200, body(found(None)))))
        self.assertIsNone(go_only.fix_for("POST", path, py, resp(200, body(found("4090, x")))))
        extra = found("")
        extra["draft"]["compute"].append({"id": "b", "kind": "ssh", "gpu_server": "b"})
        self.assertIsNone(go_only.fix_for("POST", path, py, resp(200, body(extra))))

    def test_overview_unregistered(self):
        def ov(servers, free):
            return {"project": {"id": "p"}, "gpu": {"available": True, "enabled": True, "servers": servers,
                                                     "free_gpus": free, "jobs": {}}}
        path = "/api/project/overview"
        self.assertEqual(go_only.fix_for("GET", path, resp(200, body(ov(4, 2))), resp(200, body(ov(0, None)))).id,
                         "gpu-overview-unregistered")
        self.assertIsNone(go_only.fix_for("GET", path, resp(200, body(ov(4, 2))), resp(200, body(ov(3, 2)))))
        other = ov(0, None)
        other["project"]["extra"] = 1
        self.assertIsNone(go_only.fix_for("GET", path, resp(200, body(ov(4, 2))), resp(200, body(other))))
        self.assertIsNone(go_only.fix_for("GET", path, resp(200, body(ov(0, 2))), resp(200, body(ov(0, None)))))

    def test_registration_is_augmented(self):
        strip = go_only.augmented("/api/gpu/servers")
        self.assertIsNone(json.loads(strip(body(self.GO_SERVERS))).get("registration"))
        dash = go_only.augmented("/api/gpu/dashboard")
        out = json.loads(dash(body({"probe": {"servers": [{"server": "a", "storage": []}]}, "jobs": [],
                                    "registration": {"hint_code": "not_registered"}})))
        self.assertEqual(out, {"probe": {"servers": [{"server": "a"}]}, "jobs": []})

    def test_replay_registers_its_servers(self):
        import replay_gpu
        steps = replay_gpu.gpu_steps({"tokens": {"server": "s", "full_token": "f", "read_token": "r"}})
        calls = [s for s in steps if s[0] == replay_gpu.CALL]
        self.assertTrue(all(hasattr(s[3], "gpu_names") for s in calls))
        self.assertIn("q1", calls[-1][3].gpu_names)
        self.assertIn("a", calls[-1][3].gpu_names)  # cumulative
        self.assertEqual(replay_gpu._NAME.findall(replay_gpu.queue_config()), ["q1", "q2", "q3", "qoff"])


class ChatScopeTest(unittest.TestCase):
    """T-0028 (ark_studio/server/internal/store/chat.go, internal/project/archive.go):
    the Assistant chat is per user; the pre-v10 rows go to the project owner."""

    @staticmethod
    def msgs(*ids):
        return [{"id": i, "at": "t", "role": "user" if i % 2 else "assistant", "content": f"m{i}",
                 "tools": [], "meta": {}} for i in ids]

    def chat(self, *ids):
        return body({"ok": True, "messages": self.msgs(*ids), "harness": {"available": True}})

    def test_history(self):
        fix = go_only.fix_for("GET", "/api/chat?limit=400", resp(200, self.chat(1, 2, 5, 6)), resp(200, self.chat(5, 6)))
        self.assertEqual(fix.id, "chat-own-thread")
        # A window: Python's 3 newest include legacy row 2; Go's reach back to 4.
        self.assertIsNotNone(go_only.fix_for("GET", "/api/chat", resp(200, self.chat(2, 5, 6)),
                                             resp(200, self.chat(4, 5, 6))))
        self.assertIsNotNone(go_only.fix_for("GET", "/api/chat", resp(200, self.chat(1, 2)), resp(200, self.chat())))
        # Narrow: nothing dropped, a newer row dropped, a changed text, other members, other status.
        for py, go in ((self.chat(5, 6), self.chat(5, 6)), (self.chat(5, 6, 7), self.chat(5, 6)),
                       (self.chat(1, 5, 6), self.chat(5, 7)),
                       (self.chat(1, 5), body({"ok": True, "messages": [dict(self.msgs(5)[0], content="x")],
                                               "harness": {"available": True}})),
                       (self.chat(1, 5), body({"ok": True, "messages": self.msgs(5), "harness": {}, "x": 1}))):
            self.assertIsNone(go_only.fix_for("GET", "/api/chat", resp(200, py), resp(200, go)), (py, go))
        self.assertIsNone(go_only.fix_for("GET", "/api/chat", resp(500, self.chat(1, 5)), resp(200, self.chat(5))))
        self.assertIsNone(go_only.fix_for("POST", "/api/chat", resp(200, self.chat(1, 5)), resp(200, self.chat(5))))

    def test_clear(self):
        fix = go_only.fix_for("DELETE", "/api/chat", resp(200, body({"ok": True, "cleared": 4})),
                              resp(200, body({"ok": True, "cleared": 2})))
        self.assertEqual(fix.id, "chat-clear-own-thread")
        for py, go in ((4, 4), (2, 4), (4, -1), (4, True)):
            self.assertIsNone(go_only.fix_for("DELETE", "/api/chat", resp(200, body({"ok": True, "cleared": py})),
                                              resp(200, body({"ok": True, "cleared": go}))), (py, go))
        self.assertIsNone(go_only.fix_for("DELETE", "/api/chat", resp(200, body({"ok": True, "cleared": 4})),
                                          resp(200, body({"ok": True, "cleared": 2, "x": 1}))))

    def test_activity(self):
        def act(n, title="t"):
            return body({"ok": True, "activity": [
                {"id": 9, "at": "<NOW>", "actor": "human:user0001", "action": "chat.clear", "unit_id": None,
                 "detail": {"messages": n}},
                {"id": 8, "at": "<NOW>", "actor": "human:user0001", "action": "unit.update", "unit_id": 3,
                 "detail": {"title": title}}]})
        fix = go_only.fix_for("GET", "/api/activity?limit=120", resp(200, act(4)), resp(200, act(2)))
        self.assertEqual(fix.id, "chat-clear-activity-count")
        self.assertIsNone(go_only.fix_for("GET", "/api/activity", resp(200, act(4)), resp(200, act(4))))
        self.assertIsNone(go_only.fix_for("GET", "/api/activity", resp(200, act(2)), resp(200, act(4))))
        other = json.loads(act(2))
        other["activity"][1]["detail"] = {"title": "t", "extra": 1}
        self.assertIsNone(go_only.fix_for("GET", "/api/activity", resp(200, act(4)), resp(200, body(other))))

    @staticmethod
    def archive(rows, meta, excluded, extra_units=0):
        import io
        import sqlite3
        import tempfile
        import zipfile
        with tempfile.TemporaryDirectory() as scratch:
            path = Path(scratch) / "db.sqlite3"
            con = sqlite3.connect(str(path))
            con.execute("CREATE TABLE chat_messages(id INTEGER PRIMARY KEY, at TEXT, role TEXT, content TEXT, user TEXT)")
            con.execute("CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT)")
            con.execute("CREATE TABLE units(id INTEGER PRIMARY KEY)")
            con.executemany("INSERT INTO chat_messages VALUES(?,?,?,?,?)", [(i, "t", "user", f"m{i}", u) for i, u in rows])
            con.executemany("INSERT INTO meta VALUES(?,?)", list(meta.items()))
            con.executemany("INSERT INTO units VALUES(?)", [(i,) for i in range(3 + extra_units)])
            con.commit()
            con.close()
            data = path.read_bytes()
        out = io.BytesIO()
        with zipfile.ZipFile(out, "w") as z:
            z.writestr("p.ark/ark.sqlite3", data)
            z.writestr("p.ark/project.json", "{}")
            z.writestr("ARK-EXPORT.json", json.dumps({"format": "x", "excluded": excluded}))
        return out.getvalue()

    BASE = ["external files and symlinks"]
    PY_ROWS = [(1, ""), (2, ""), (3, "user0001"), (4, "user0002")]
    PY_META = {"schema_version": "13", "chat_cleared:user0002": "2"}

    def test_export_member(self):
        py = resp(200, self.archive(self.PY_ROWS, self.PY_META, self.BASE))
        go = resp(200, self.archive([(3, "user0001")], {"schema_version": "13", go_only.LEGACY_CHAT_OWNER: "owner"},
                                    self.BASE + [go_only.ARCHIVE_CHAT_EXCLUDED]))
        for path in ("/api/export?format=project", "/api/agent/v1/export"):
            self.assertEqual(go_only.fix_for("GET", path, py, go).id, "export-chat-scope")
        # Narrow: no exclusion recorded, two users kept, another table changed, a changed row.
        bad = [
            self.archive([(3, "user0001")], {"schema_version": "13", go_only.LEGACY_CHAT_OWNER: "owner"}, self.BASE),
            self.archive([(3, "user0001"), (4, "user0002")], {"schema_version": "13"},
                         self.BASE + [go_only.ARCHIVE_CHAT_EXCLUDED]),
            self.archive([(3, "user0001")], {"schema_version": "13"}, self.BASE + [go_only.ARCHIVE_CHAT_EXCLUDED],
                         extra_units=1),
            self.archive([(3, "user0002")], {"schema_version": "13"}, self.BASE + [go_only.ARCHIVE_CHAT_EXCLUDED]),
            self.archive([(3, "user0001")], {"schema_version": "14"} | {"other": "x"},
                         self.BASE + [go_only.ARCHIVE_CHAT_EXCLUDED]),
        ]
        for data in bad:
            self.assertIsNone(go_only.fix_for("GET", "/api/export", py, resp(200, data)))
        self.assertIsNone(go_only.fix_for("GET", "/api/export", py, resp(200, b"not a zip")))

    def test_constants_match_go(self):
        archive = (REPO / "ark_studio/server/internal/project/archive.go").read_text(encoding="utf-8")
        self.assertIn(f'const ArchiveChatExcluded = "{go_only.ARCHIVE_CHAT_EXCLUDED}"', archive)
        chat = (REPO / "ark_studio/server/internal/store/chat.go").read_text(encoding="utf-8")
        self.assertIn(f'const LegacyChatOwner = "{go_only.LEGACY_CHAT_OWNER}"', chat)

    def test_export_admin_legacy_owner(self):
        py = resp(200, self.archive(self.PY_ROWS, self.PY_META, self.BASE))
        rows = [(1, "owner"), (2, "owner"), (3, "user0001"), (4, "user0002")]
        meta = dict(self.PY_META, **{go_only.LEGACY_CHAT_OWNER: "owner"})
        self.assertIsNotNone(go_only.fix_for("GET", "/api/export", py, resp(200, self.archive(rows, meta, self.BASE))))
        # Without the meta record, or adopted by someone else, it is a mismatch.
        self.assertIsNone(go_only.fix_for("GET", "/api/export", py, resp(200, self.archive(rows, self.PY_META, self.BASE))))
        self.assertIsNone(go_only.fix_for("GET", "/api/export", py, resp(200, self.archive(
            rows, dict(self.PY_META, **{go_only.LEGACY_CHAT_OWNER: "user0001"}), self.BASE))))
        # Identical archives need no fix (and the predicate does not claim them).
        self.assertIsNone(go_only.fix_for("GET", "/api/export", py, py))


class HomeOnboardingTest(unittest.TestCase):
    """T-0030: blank projects without built-in topics; overview KPIs without topics."""

    TOPICS = [{"id": 10 + i, "uid": f"TP-000{i + 1}", "type": "topic", "title": title, "status": "active",
               "origin": "system", "meta": {"default": key}, "default": key}
              for i, (key, title) in enumerate([("story", "Story"), ("related", "Related work"),
                                                 ("method", "Method"), ("experiment", "Experiment")])]

    def test_topics_list(self):
        py = resp(200, body({"items": self.TOPICS, "total": 4}))
        go = resp(200, body({"items": [], "total": 0}))
        self.assertEqual(go_only.fix_for("GET", "/api/topics", py, go).id, "blank-project-no-default-topics")
        mine = {"id": 9, "uid": "TP-0009", "type": "topic", "title": "Mine", "status": "open", "origin": "ui",
                "meta": {}, "default": None}
        self.assertIsNotNone(go_only.fix_for("GET", "/api/topics", resp(200, body({"items": [mine] + self.TOPICS,
                                                                                   "total": 5})),
                                             resp(200, body({"items": [mine], "total": 1}))))
        # Narrow: a user topic missing, Go still listing a built-in one, no difference at all,
        # another status, another route.
        self.assertIsNone(go_only.fix_for("GET", "/api/topics", resp(200, body({"items": [mine], "total": 1})), go))
        self.assertIsNone(go_only.fix_for("GET", "/api/topics", py,
                                          resp(200, body({"items": self.TOPICS[:1], "total": 1}))))
        self.assertIsNone(go_only.fix_for("GET", "/api/topics", go, go))
        self.assertIsNone(go_only.fix_for("GET", "/api/topics", py, resp(500, body({"items": []}))))
        self.assertIsNone(go_only.fix_for("GET", "/api/topic-list", py, go))

    def test_bootstrap_counts(self):
        activity = [{"actor": "system:ark", "action": "unit.create", "unit_uid": "TP-0001", "unit_type": "topic",
                     "unit_title": "Story"},
                    {"actor": "system", "action": "project.create", "unit_uid": None, "unit_type": None}]
        py = {"summary": {"id": "p", "template": "blank"}, "stats": {"total": 5, "by_type": {"note": 1, "topic": 4}},
              "activity": activity}
        go = {"summary": {"id": "p", "template": "blank"}, "stats": {"total": 1, "by_type": {"note": 1}},
              "activity": activity[1:]}
        self.assertIsNotNone(go_only.fix_for("POST", "/api/projects/open", resp(200, body(py)), resp(200, body(go))))
        # Another difference beside the topics is still a mismatch.
        self.assertIsNone(go_only.fix_for("POST", "/api/projects/open", resp(200, body(py)),
                                          resp(200, body(dict(go, extra=1)))))
        self.assertIsNone(go_only.fix_for("POST", "/api/projects/open", resp(200, body(py)),
                                          resp(200, body(dict(go, activity=[])))))

    @staticmethod
    def overview(active, open_, unassigned, topics=4, agents=False):
        nodes = [{"id": 1, "type": "note", "status": "open", "degree": 0}]
        nodes += [{"id": 10 + i, "type": "topic", "status": "active", "degree": 0} for i in range(topics)]
        kpi = lambda now, prev: {"now": now, "prev": prev, "delta": now - prev}  # noqa: E731
        out = {"today": "2026-11-16", "graph": {"nodes": nodes, "edges": []},
               "kpis": {"active": kpi(active, 0), "blocked": kpi(0, 0), "review": kpi(0, 0), "overdue": kpi(0, 0),
                        "open": {"now": open_}, "created": {"week": 5, "prev": 0}, "completed": {"week": 0, "prev": 0}},
               "days": [], "stages": [], "members": [], "unassigned": {"open": unassigned, "active": active},
               "due": {}, "blocked": {}, "research": {}, "activity": [], "store_ms": 1.0}
        if agents:
            out["agents"] = {"first_at": None, "recent": [], "days": 30}
        return out

    def test_overview_kpis(self):
        py = resp(200, body(self.overview(4, 5, 5)))
        go = resp(200, body(self.overview(0, 1, 1, agents=True)))
        self.assertEqual(go_only.fix_for("GET", "/api/project/overview", py, go).id, "overview-kpis-without-topics")
        # Narrow: more removed than there are topics, another member changed, no topics,
        # the 14-day figures changed.
        self.assertIsNone(go_only.fix_for("GET", "/api/project/overview", py,
                                          resp(200, body(self.overview(0, 0, 1, agents=True)))))
        other = self.overview(0, 1, 1, agents=True)
        other["days"] = [{"date": "x"}]
        self.assertIsNone(go_only.fix_for("GET", "/api/project/overview", py, resp(200, body(other))))
        self.assertIsNone(go_only.fix_for("GET", "/api/project/overview", resp(200, body(self.overview(4, 5, 5, 0))),
                                          go))
        week = self.overview(0, 1, 1, agents=True)
        week["kpis"]["created"]["week"] = 1
        self.assertIsNone(go_only.fix_for("GET", "/api/project/overview", py, resp(200, body(week))))
        self.assertIsNone(go_only.fix_for("GET", "/api/project/overview", py, py))

    def test_overview_agents_augmented(self):
        strip = go_only.augmented("/api/project/overview?x=1")
        payload = self.overview(1, 1, 1, agents=True)
        self.assertEqual(json.loads(strip(body(payload))), self.overview(1, 1, 1))
        self.assertEqual(strip(b"not json"), b"not json")

    def test_constants_match_go(self):
        source = (REPO / "ark_studio/server/internal/store/schema_gen.go").read_text(encoding="utf-8")
        decl = source.split("var DefaultTopics = ", 1)[1].split("}\n}", 1)[0]
        pairs = re.findall(r'\{"(\w+)", "([^"]+)", "\w+"\}', decl)
        self.assertEqual({k for k, _ in pairs}, set(go_only.DEFAULT_TOPIC_KEYS))
        self.assertEqual({v for _, v in pairs}, set(go_only.DEFAULT_TOPIC_TITLES))


class StarterTokenTest(unittest.TestCase):
    """UX-18 (ark_studio/server/internal/org/starter_tokens.go): one starter token per project."""

    PATH = "/api/personal/agent-tokens"

    @staticmethod
    def reply(label, tokens, rotated=None):
        """Python's reply has "limit"; Go's (with rotated) has none."""
        item = {"id": "tok_new", "label": label, "prefix": "arka_abcd", "scope": "write",
                "created_at": "t", "expires_at": "e", "last_used_at": None, "expired": False}
        out = {"ok": True, "token": "arka_secret", "item": item, "user": "local", "tokens": tokens,
               "limit": 10, "scopes": ["read", "write"], "expiry_days": [0, 30, 90]}
        if rotated is not None:
            del out["limit"]
            out["rotated"] = rotated
        return body(out)

    def test_rotation(self):
        old = [{"id": "tok_a"}, {"id": "tok_b"}, {"id": "tok_c"}]
        py = resp(200, self.reply("Home starter prompt – 2026-11-16", [{"id": "tok_new"}] + old))
        go = resp(200, self.reply("Home starter prompt (prj_1) – 2026-11-16", [{"id": "tok_new"}, old[0]], ["tok_b", "tok_c"]))
        self.assertEqual(go_only.fix_for("POST", self.PATH, py, go).id, "agent-token-starter-rotation")
        first = resp(200, self.reply("Home starter prompt (prj_1) – 2026-11-16", [{"id": "tok_new"}], []))
        self.assertIsNotNone(go_only.fix_for("POST", self.PATH, py, first))

    def test_token_limit_restored(self):
        go = {"ok": True, "user": "local", "tokens": [], "scopes": ["read"], "expiry_days": [0]}
        want = {"ok": True, "user": "local", "tokens": [], "limit": 10, "scopes": ["read"], "expiry_days": [0]}
        for path in (self.PATH, self.PATH + "?x=1", self.PATH + "/tok_abc"):
            out = go_only.augmented(path)(body(go))
            self.assertEqual(list(json.loads(out).items()), list(want.items()))
        restore = go_only.augmented(self.PATH)
        self.assertEqual(restore(body(want)), body(want))
        self.assertEqual(restore(b"not json"), b"not json")
        self.assertEqual(restore(b'{"ok": false}'), b'{"ok": false}')
        go_src = (REPO / "ark_studio/server/internal/org/admin.go").read_text(encoding="utf-8")
        self.assertNotIn('"limit"', go_src.split("func (m *Manager) ListAgentTokens", 1)[1].split("\n}\n", 1)[0])

    def test_narrow(self):
        py = resp(200, self.reply("Home starter prompt – d", []))
        label = "Home starter prompt (prj_1) – d"
        # A plain create (no "rotated"), a non-starter label, an error, a wrong method or path.
        self.assertIsNone(go_only.fix_for("POST", self.PATH, py, resp(200, self.reply(label, []))))
        self.assertIsNone(go_only.fix_for("POST", self.PATH, py, resp(200, self.reply("CI", [], []))))
        self.assertIsNone(go_only.fix_for("POST", self.PATH, py, resp(200, self.reply(label, [], ["x"]))))
        self.assertIsNone(go_only.fix_for("POST", self.PATH, py, resp(409, err(409, "AGENT_TOKEN_LIMIT", "x"))))
        self.assertIsNone(go_only.fix_for("POST", self.PATH, resp(409, err(409, "AGENT_TOKEN_LIMIT", "x")),
                                          resp(200, self.reply(label, [], []))))
        self.assertIsNone(go_only.fix_for("GET", self.PATH, py, resp(200, self.reply(label, [], []))))
        extra = json.loads(self.reply(label, [], []))
        extra["more"] = 1
        self.assertIsNone(go_only.fix_for("POST", self.PATH, py, resp(200, body(extra))))

    def test_label_matches_go(self):
        source = (REPO / "ark_studio/server/internal/org/starter_tokens.go").read_text(encoding="utf-8")
        self.assertIn('starterLabelPrefix = "Home starter prompt"', source)
        self.assertIn('return starterLabelPrefix + " (" + project + ")"', source)
        self.assertEqual(go_only.STARTER_LABEL, "Home starter prompt (")


class ProjectPickerTest(unittest.TestCase):
    """T-0034 (ark_studio/server/internal/server/project_activity.go,
    internal/settings/projectprefs.go): personal activity per listed project,
    pins and hidden projects on the profile."""

    PROJECT = {"id": "p1", "name": "One", "path": "/x/one.ark", "updated": "2026-11-16T10:00:00+00:00"}

    def test_projects_strip_my_activity(self):
        strip = go_only.augmented("/api/projects?x=1")
        go = {"projects": [dict(self.PROJECT, my_activity=2.45, my_last_active_at="2026-11-16T09:00:00+00:00"),
                           dict(self.PROJECT, id="p2", my_activity=0.0, my_last_active_at=None)],
              "session": {"user": "local"}}
        want = {"projects": [self.PROJECT, dict(self.PROJECT, id="p2")], "session": {"user": "local"}}
        self.assertEqual(json.loads(strip(body(go))), want)
        self.assertEqual(strip(b"not json"), b"not json")
        self.assertEqual(strip(b'{"error": "x"}'), b'{"error": "x"}')

    def test_members_match_go(self):
        source = (REPO / "ark_studio/server/internal/server/project_activity.go").read_text(encoding="utf-8")
        for member in go_only.PROJECT_ACTIVITY_MEMBERS:
            self.assertIn(f'"{member}"', source)

    def test_profile_project_prefs(self):
        py = resp(400, b'{"error": {"message": "unknown preference: projects", "code": "PROFILE_INVALID"}}')
        go = resp(200, body({"profile": {"prefs": {"projects": {"pinned": ["a"], "hidden": []}}}}))
        fix = go_only.fix_for("PATCH", "/api/me", py, go)
        self.assertEqual(fix.id, "profile-project-prefs")
        # Narrow: another refused preference, a Go error or another route is not explained.
        other = resp(400, b'{"error": {"message": "unknown preference: colour", "code": "PROFILE_INVALID"}}')
        self.assertIsNone(go_only.fix_for("PATCH", "/api/me", other, go))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/me", py, resp(400, py.body)))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/me", py, resp(200, b"[]")))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/personal/settings", py, go))


class GraphDisplayTest(unittest.TestCase):
    """T-0036 (ark_studio/server/internal/settings/graphdisplay.go): graph
    display defaults and overrides, per-project Mind layout."""

    PROFILE = {"profile": {"username": "local", "prefs": {"lang": "en"}}}

    def test_strip_project_settings(self):
        strip = go_only.augmented("/api/project/settings")
        go = {"settings": {"graph": {"repulsion": 1}, "graph_display": {"mind": {"show_images": False}}}}
        self.assertEqual(json.loads(strip(body(go))), {"settings": {"graph": {"repulsion": 1}}})
        same = body({"settings": {"graph": {}}})
        self.assertEqual(strip(same), same)
        self.assertEqual(strip(b"not json"), b"not json")

    def test_strip_profile(self):
        strip = go_only.augmented("/api/me")
        go = {"profile": {"username": "local", "prefs": {
            "lang": "en", "mind_layout": {"p": {"widths": {"U": 200}}},
            "appearance": {"graph_display": {"force": {"marker_scale": 2}}}}}}
        self.assertEqual(json.loads(strip(body(go))), self.PROFILE)
        kept = {"profile": {"prefs": {"appearance": {"graph": {"repulsion": 1}, "graph_display": {}}}}}
        self.assertEqual(json.loads(strip(body(kept))), {"profile": {"prefs": {"appearance": {"graph": {"repulsion": 1}}}}})
        same = body(self.PROFILE)
        self.assertEqual(strip(same), same)
        self.assertEqual(strip(b'{"error": "x"}'), b'{"error": "x"}')

    def test_project_fix(self):
        py = resp(400, err(400, "BAD_REQUEST", "unknown settings section: graph_display"))
        go = resp(200, body({"settings": {"graph_display": {"local": {"depth": 3}}}}))
        self.assertEqual(go_only.fix_for("PATCH", "/api/project/settings", py, go).id, "project-graph-display")
        other = resp(400, err(400, "BAD_REQUEST", "unknown settings section: colours"))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/project/settings", other, go))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/project/settings", py, resp(400, py.body)))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/project/settings", py, resp(200, b"[]")))
        self.assertIsNone(go_only.fix_for("GET", "/api/project/settings", py, go))

    def test_profile_fixes(self):
        go = resp(200, body(self.PROFILE))
        group = resp(400, b'{"error": {"message": "unknown appearance group: graph_display", "code": "PROFILE_INVALID"}}')
        layout = resp(400, b'{"error": {"message": "unknown preference: mind_layout", "code": "PROFILE_INVALID"}}')
        self.assertEqual(go_only.fix_for("PATCH", "/api/me", group, go).id, "profile-graph-display")
        self.assertEqual(go_only.fix_for("PATCH", "/api/me", layout, go).id, "profile-mind-layout")
        # Narrow: another group or preference, a Go error, another route.
        other = resp(400, b'{"error": {"message": "unknown appearance group: fonts", "code": "PROFILE_INVALID"}}')
        self.assertIsNone(go_only.fix_for("PATCH", "/api/me", other, go))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/me", layout, resp(400, layout.body)))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/me", group, resp(200, b"{}")))
        self.assertIsNone(go_only.fix_for("PATCH", "/api/personal/settings", layout, go))

    def test_members_match_go(self):
        source = (REPO / "ark_studio/server/internal/settings/graphdisplay.go").read_text(encoding="utf-8")
        self.assertIn("graph_display", source)
        self.assertIn("prefs.mind_layout", source)
        for name in ("settings.go", "writes.go"):
            text = (REPO / "ark_studio/server/internal/settings" / name).read_text(encoding="utf-8")
            self.assertIn('"graph_display"', text, name)
        self.assertIn('"mind_layout"', (REPO / "ark_studio/server/internal/org/admin.go").read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
