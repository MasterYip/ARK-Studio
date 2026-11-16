from datetime import datetime, timedelta, timezone
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock
from urllib.parse import urlencode

from arkstudio.gpu_probe import GpuService, VENDOR
from tests.python.auth.test_org import AuthHttpCase


def queue_fixture(folder, project="prj_test", count=650):
    config = folder / "monitoring.yaml"
    config.write_text("enabled: true\nservers:\n"
                      "  - name: gpu-a\n    host: gpu-a.example.org\n"
                      "  - name: gpu-b\n    host: gpu-b.example.org\n"
                      "queue:\n  enabled: true\n", encoding="utf-8")
    runner = Mock(side_effect=AssertionError("monitoring must not contact SSH"))
    queue = VENDOR.queue.GpuQueue(config, db_path=folder / "monitoring.sqlite3", environ={}, command_runner=runner)
    template = queue.submit({"job": "fixture", "command": "true", "project": project}, start=False)
    expected = []
    with queue._connect() as conn:
        row = dict(conn.execute("SELECT * FROM gpu_jobs WHERE id=?", (template["id"],)).fetchone())
        conn.execute("DELETE FROM gpu_jobs")
        for index in range(count):
            stamp = (datetime(2026, 1, 1, tzinfo=timezone.utc) + timedelta(seconds=index // 3)).isoformat()
            state = ("queued", "starting", "running", "completed", "errored", "canceled")[index % 6]
            job = {**row, "id": f"gpu_{index:016x}", "job": f"rebuttal-ce22-{index % 40}",
                "project": project if index < count - 50 else "prj_foreign",
                "owner": "alice" if index % 2 else "bob", "session": "session-a" if index % 2 else "session-b",
                "created_at": stamp, "updated_at": stamp, "status": state,
                "server": "gpu-a" if index % 2 else "gpu-b",
                "ended_at": stamp if state in {"completed", "errored", "canceled"} else None,
                "exit_code": 7 if state == "errored" else 0 if state == "completed" else None}
            expected.append(job)
        columns = list(row)
        conn.executemany(f"INSERT INTO gpu_jobs ({','.join(columns)}) VALUES ({','.join('?' for _ in columns)})",
                         [[job[key] for key in columns] for job in expected])
    return queue, expected, runner


class GpuMonitoringTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.queue, self.expected, self.runner = queue_fixture(Path(self.tmp.name))

    def tearDown(self):
        self.queue.close()
        self.tmp.cleanup()

    def expected_ids(self, predicate=lambda job: True):
        return [row["id"] for row in sorted(self.expected, key=lambda row: (row["created_at"], row["id"]), reverse=True)
                if row["project"] == "prj_test" and predicate(row)]

    def test_paging_beyond_500_counts_all_matches_and_has_no_gaps_for_ties(self):
        page = self.queue.list(project="prj_test", limit=500)
        self.assertEqual((page["total"], page["count"], page["next_offset"], page["has_more"]), (600, 500, 500, True))
        seen, offset = [], 0
        while True:
            page = self.queue.list(project="prj_test", limit=127, offset=offset)
            self.assertEqual(page["total"], 600)
            seen.extend(job["id"] for job in page["jobs"])
            if not page["has_more"]:
                self.assertIsNone(page["next_offset"])
                break
            offset = page["next_offset"]
        self.assertEqual(seen, self.expected_ids())
        self.assertEqual(len(set(seen)), 600)
        empty = self.queue.list(project="prj_test", offset=999)
        self.assertEqual((empty["total"], empty["count"], empty["has_more"]), (600, 0, False))
        self.runner.assert_not_called()

    def test_combined_filters_exact_attempts_and_batch_ids(self):
        page = self.queue.list(project="prj_test", status="running,errored", server="gpu-b", name_prefix="rebuttal-ce22-1", limit=500)
        expected = self.expected_ids(lambda row: row["status"] in {"running", "errored"} and row["server"] == "gpu-b" and row["job"].startswith("rebuttal-ce22-1"))
        self.assertEqual([job["id"] for job in page["jobs"]], expected)
        page = self.queue.list(project="prj_test", name="rebuttal-ce22-1", limit=500)
        self.assertEqual([job["id"] for job in page["jobs"]], self.expected_ids(lambda row: row["job"] == "rebuttal-ce22-1"))
        self.assertIn("canceled", {job["status"] for job in page["jobs"]})
        self.assertTrue(all(job["name"] == job["job"] for job in page["jobs"]))
        ids = [self.expected[index]["id"] for index in (3, 8, 21, 640)]
        page = self.queue.list(project="prj_test", ids=ids)
        self.assertEqual({job["id"] for job in page["jobs"]}, set(ids[:3]))
        self.assertEqual(self.queue.list(project="prj_test", name_prefix="Rebuttal")["total"], 0)
        self.assertEqual(self.queue.list(project="prj_test", name="' OR 1=1 --")["total"], 0)
        self.runner.assert_not_called()

    def test_since_normalizes_timezone_and_fractional_seconds(self):
        for value, threshold in (("2026-01-01T08:02:00+08:00", "2026-01-01T00:02:00+00:00"),
                                 ("2026-01-01T00:02:00.5Z", "2026-01-01T00:02:01+00:00")):
            page = self.queue.list(project="prj_test", since=value, limit=500)
            self.assertEqual([job["id"] for job in page["jobs"]], self.expected_ids(lambda row: row["created_at"] >= threshold))

    def test_invalid_filters_and_session_isolation(self):
        for kwargs in ({"status": "running,unknown"}, {"status": ","}, {"name_prefix": "rebuttal*"},
                       {"since": "2026-01-01"}, {"since": "invalid"}, {"limit": 501}, {"offset": -1},
                       {"ids": []}, {"ids": ["gpu_a"] * 501}, {"ids": ["a' OR 1=1"]}):
            with self.subTest(kwargs=kwargs), self.assertRaises(VENDOR.queue.QueueError):
                self.queue.list(project="prj_test", **kwargs)
        page = self.queue.list(project="prj_test", owner="alice", session="session-a", limit=500)
        self.assertTrue(all(job["owner"] == "alice" and job["session"] == "session-a" for job in page["jobs"]))

    def test_failure_tail_is_persisted_in_existing_status_transport_and_reads_are_local(self):
        job_id = self.expected[0]["id"]
        with self.queue._connect() as conn:
            conn.execute("DELETE FROM gpu_jobs WHERE id<>?", (job_id,))
            conn.execute("UPDATE gpu_jobs SET status='running',pid=4242 WHERE id=?", (job_id,))
        lines = [f"log line {n}" for n in range(40)] + ["RuntimeError: CUDA out of memory"]
        self.runner.side_effect = None
        self.runner.return_value = VENDOR.ssh.CommandResult(0, stdout="DONE:7\n" + VENDOR.queue.LOG_TAIL_MARKER + "\n" + "\n".join(lines))
        self.queue._reap(self.queue._load_config())
        self.runner.assert_called_once()
        self.assertIn("tail -n 20", self.runner.call_args.args[1])
        job = self.queue.get(job_id)
        self.assertEqual((job["status"], job["exit_code"]), ("errored", 7))
        self.assertIn("CUDA out of memory", job["error"])
        self.assertEqual(job["log_tail"].splitlines(), lines[-20:])
        self.assertIsNotNone(job["ended_at"])
        self.runner.reset_mock()
        self.runner.side_effect = AssertionError("no SSH for list/get")
        self.assertEqual(self.queue.list(project="prj_test")["jobs"][0]["log_tail"], job["log_tail"])
        restored = VENDOR.queue.GpuQueue(self.queue.config_path, db_path=self.queue.db_path, environ={})
        self.assertEqual(restored.get(job_id)["log_tail"], job["log_tail"])
        restored.close()
        self.runner.assert_not_called()

    def test_logs_accept_tail_and_return_cached_summary_if_transport_fails(self):
        job_id = self.expected[0]["id"]
        with self.queue._connect() as conn:
            conn.execute("UPDATE gpu_jobs SET log='one\ntwo\nthree',log_path='/tmp/example/job.log' WHERE id=?", (job_id,))
        self.runner.side_effect = None
        self.runner.return_value = VENDOR.ssh.CommandResult(1, message="unreachable")
        response = self.queue.logs(job_id, limit=2)
        self.assertEqual((response["log"], response["cached"]), ("two\nthree", True))
        self.assertIn("tail -n 2", self.runner.call_args.args[1])
        with self.assertRaises(VENDOR.queue.QueueError):
            self.queue.logs(job_id, limit=501)

    @unittest.skipUnless(os.name == "posix", "the remote worker executes POSIX shell")
    def test_status_shell_reads_real_exit_file_and_bounded_failure_tail(self):
        job_id = self.expected[0]["id"]
        state_dir = Path(self.tmp.name) / "remote state with spaces"
        state = state_dir / job_id
        state.mkdir(parents=True)
        lines = [f"line {i}" for i in range(40)] + ["RuntimeError: fixture failure"]
        (state / "job.log").write_text("\n".join(lines) + "\n", encoding="utf-8")
        (state / "status").write_text("7\n", encoding="utf-8")
        with self.queue._connect() as conn:
            conn.execute("DELETE FROM gpu_jobs WHERE id<>?", (job_id,))
            conn.execute("UPDATE gpu_jobs SET status='running',pid=999999999 WHERE id=?", (job_id,))
        config = self.queue._load_config()
        config["queue"]["state_dir"] = str(state_dir)
        def local_shell(server, command, timeout=None):
            result = subprocess.run(["bash", "-c", command], capture_output=True, text=True, timeout=5)
            return VENDOR.ssh.CommandResult(result.returncode, stdout=result.stdout, stderr=result.stderr)
        self.runner.side_effect = local_shell
        self.queue._reap(config)
        job = self.queue.get(job_id)
        self.assertEqual(job["exit_code"], 7)
        self.assertEqual(job["log_tail"].splitlines(), lines[-20:])

    def test_cancel_during_status_check_keeps_canceled_state(self):
        job_id = self.expected[0]["id"]
        with self.queue._connect() as conn:
            conn.execute("DELETE FROM gpu_jobs WHERE id<>?", (job_id,))
            conn.execute("UPDATE gpu_jobs SET status='running',pid=4242 WHERE id=?", (job_id,))
        def concurrent_cancel(server, command, timeout=None):
            with self.queue._connect() as conn:
                conn.execute("UPDATE gpu_jobs SET status='canceled',error='canceled by user' WHERE id=?", (job_id,))
            return VENDOR.ssh.CommandResult(0, stdout="DONE:7")
        self.runner.side_effect = concurrent_cancel
        self.queue._reap(self.queue._load_config())
        self.assertEqual(self.queue.get(job_id)["status"], "canceled")


class GpuMonitoringHttpTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.project = self.project_id()
        self.queue, self.expected, self.runner = queue_fixture(Path(self.tmp.name), self.project)
        self.app.gpu.close()
        self.app.gpu = GpuService(self.queue.config_path, environ={})
        self.app.gpu._queue = self.queue
        self.create_user("alice", "alice-pass")
        self.grant(self.project, user="alice", permission="read")
        cookie = self.login("alice", "alice-pass")
        token = self.api("/api/personal/agent-tokens", cookie=cookie, method="POST",
                         body={"label": "monitoring", "scope": "read"}).json()["token"]
        self.headers = {"Authorization": "Bearer " + token, "X-ARK-Project": self.project}

    def test_native_and_agent_list_contract_paging_name_and_filters(self):
        query = {"status": "errored,canceled", "name_prefix": "rebuttal-ce22-", "server": "gpu-a", "limit": 7, "offset": 7}
        native = self.api("/api/gpu/jobs?" + urlencode(query), cookie=self.owner)
        agent = self.api("/api/agent/v1/gpu/jobs?" + urlencode(query), headers=self.headers)
        self.assertEqual(native.status, 200, native.text)
        self.assertEqual(agent.status, 200, agent.text)
        self.assertEqual(native.json(), agent.json())
        self.assertEqual((agent.json()["count"], agent.json()["offset"]), (7, 7))
        self.assertGreater(agent.json()["total"], 7)
        all_jobs = self.api("/api/agent/v1/gpu/jobs?limit=500", headers=self.headers).json()
        self.assertEqual((all_jobs["total"], all_jobs["next_offset"]), (600, 500))
        attempts = self.api("/api/agent/v1/gpu/jobs?name=rebuttal-ce22-1", headers=self.headers).json()
        self.assertEqual(attempts["total"], 15)
        self.assertIn("canceled", {job["status"] for job in attempts["jobs"]})
        batch = self.api("/api/agent/v1/gpu/jobs?ids=" + self.expected[0]["id"] + "," + self.expected[-1]["id"], headers=self.headers).json()
        self.assertEqual([job["id"] for job in batch["jobs"]], [self.expected[0]["id"]])
        self.runner.assert_not_called()

    def test_bad_filters_are_400_and_logs_remain_project_scoped(self):
        for query in ("limit=501", "offset=-1", "limit=invalid", "since=invalid", "status=unknown", "ids=invalid", "limit=2&limit=3"):
            response = self.api("/api/agent/v1/gpu/jobs?" + query, headers=self.headers)
            self.assertEqual(response.status, 400, (query, response.text))
        own = self.expected[0]["id"]
        logs = self.api(f"/api/agent/v1/gpu/jobs/{own}/logs?tail=2", headers=self.headers)
        self.assertEqual(logs.status, 200, logs.text)
        self.assertEqual(self.api(f"/api/gpu/jobs/{own}/logs?tail=501", cookie=self.owner).status, 400)
        foreign = self.expected[-1]["id"]
        self.assertEqual(self.api(f"/api/agent/v1/gpu/jobs/{foreign}/logs?tail=2", headers=self.headers).status, 404)
