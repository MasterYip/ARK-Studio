"""GPU task tokens never override the account's current project grant."""
import http.client
import json
from pathlib import Path

from arkstudio.sessions import ManagedSession
from tests.python.auth.test_org import AuthHttpCase


class QueueStub:
    def __init__(self):
        self.jobs = {}
        self.canceled = []

    def queue_submit(self, body, **identity):
        job = {**body, **identity, "id": "gpu_test"}
        self.jobs[job["id"]] = job
        return job

    def queue_get(self, job_id):
        return self.jobs[job_id]

    def queue_cancel(self, job_id):
        self.canceled.append(job_id)
        return self.jobs[job_id]

    def close(self):
        pass

    def queue_list(self, *, project="", owner="", session="", **kwargs):
        jobs = [job for job in self.jobs.values() if job["project"] == project
                and (not owner or job.get("owner") == owner) and (not session or job.get("session") == session)]
        return {"jobs": jobs, "total": len(jobs)}

    def queue_logs(self, job_id, limit=200):
        return {"log": "fixture"}


class TaskGpuAuthorizationTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.create_user("alice", "alice-pass")
        self.project = self.project_id()
        self.grant(self.project, user="alice", permission="write")
        self.app.gpu.close()
        self.queue = QueueStub()
        self.app.gpu = self.queue
        self.token = "ops-task-session-token"
        self.session = ManagedSession(self.app.sessions, Path(self.tmp.name) / "session", {
            "id": "s_20261109000000000001", "actor": "alice", "access_mode": "full_access",
            "read_token": self.token, "project": {"id": self.project}, "status": "stopped",
        })
        self.app.sessions._sessions[self.session.id] = self.session

    def gpu(self, path="/api/task-agent/v1/gpu/jobs", *, cookie=""):
        return self.api(path, method="POST", body={"command": "true"}, cookie=cookie,
                        headers={"X-ARK-Read-Token": self.token})

    def test_current_writer_can_submit_and_identity_is_bound_to_task(self):
        response = self.gpu()
        self.assertEqual(response.status, 200, response.text)
        job = response.json()["job"]
        self.assertEqual((job["owner"], job["project"], job["session"]),
                         ("alice", self.project, self.session.id))

    def test_read_only_task_mode_cannot_submit(self):
        self.session.meta["access_mode"] = "read_only"
        self.assertEqual(self.gpu().status, 403)
        self.assertEqual(self.queue.jobs, {})

    def test_downgrade_blocks_submit_and_cancel(self):
        self.assertEqual(self.gpu().status, 200)
        self.grant(self.project, user="alice", permission="read")
        self.assertEqual(self.gpu().status, 403)
        self.assertEqual(self.gpu("/api/task-agent/v1/gpu/jobs/gpu_test/cancel").status, 403)
        self.assertEqual(self.queue.canceled, [])

    def test_disabled_account_cannot_dispatch(self):
        user = self.app.org.user_id_for("alice")
        self.api("/api/org/users/" + user, method="PATCH", body={"disabled": True}, cookie=self.owner)
        self.assertIn(self.gpu().status, (401, 403, 404))
        self.assertEqual(self.queue.jobs, {})

    def test_owner_cookie_does_not_elevate_a_task_token(self):
        self.grant(self.project, user="alice", permission="read")
        self.assertEqual(self.gpu(cookie=self.owner).status, 403)
        self.assertEqual(self.queue.jobs, {})

    def test_task_batch_list_stays_within_its_own_session(self):
        self.assertEqual(self.gpu().status, 200)
        self.queue.jobs["gpu_other_session"] = {"id": "gpu_other_session", "project": self.project,
                                                "owner": "alice", "session": "another-session"}
        response = self.api("/api/task-agent/v1/gpu/jobs?ids=gpu_test,gpu_other_session",
                            headers={"X-ARK-Read-Token": self.token})
        self.assertEqual(response.status, 200, response.text)
        self.assertEqual([job["id"] for job in response.json()["jobs"]], ["gpu_test"])

    def personal(self, scope, path, *, method="POST"):
        cookie = self.login("alice", "alice-pass")
        minted = self.api("/api/personal/agent-tokens", cookie=cookie, method="POST",
                          body={"label": "queue", "scope": scope}).json()["token"]
        return self.api(path, method=method, body={"command": "true"} if method == "POST" else None,
                        headers={"Authorization": "Bearer " + minted, "X-ARK-Project": self.project})

    def test_personal_write_token_can_use_native_queue_without_a_login_cookie(self):
        response = self.personal("write", "/api/agent/v1/gpu/jobs")
        self.assertEqual(response.status, 200, response.text)
        self.assertEqual(response.json()["job"]["owner"], "alice")
        self.assertEqual(self.personal("read", "/api/agent/v1/gpu/jobs", method="GET").status, 200)

    def test_personal_read_token_cannot_submit_or_cancel(self):
        self.assertEqual(self.personal("write", "/api/agent/v1/gpu/jobs").status, 200)
        self.assertEqual(self.personal("read", "/api/agent/v1/gpu/jobs").status, 403)
        self.assertEqual(self.personal("read", "/api/agent/v1/gpu/jobs/gpu_test/cancel").status, 403)

    def test_read_after_rejected_post_does_not_parse_leftover_body_as_a_request(self):
        cookie = self.login("alice", "alice-pass")
        token = self.api("/api/personal/agent-tokens", cookie=cookie, method="POST",
                         body={"label": "monitoring", "scope": "read"}).json()["token"]
        headers = {"Authorization": "Bearer " + token, "X-ARK-Project": self.project,
                   "Content-Type": "application/json"}
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        try:
            connection.request("POST", "/api/agent/v1/gpu/jobs", json.dumps({"command": "true"}), headers)
            rejected = connection.getresponse()
            rejected.read()
            self.assertEqual(rejected.status, 403)
            connection.request("GET", "/api/agent/v1/gpu/jobs", headers=headers)
            response = connection.getresponse()
            payload = response.read()
            self.assertEqual(response.status, 200, payload)
            self.assertEqual(json.loads(payload)["jobs"], [])
        finally:
            connection.close()

    def test_personal_token_cannot_read_or_cancel_another_projects_job(self):
        self.queue.jobs["gpu_foreign"] = {"id": "gpu_foreign", "project": "prj_foreign"}
        for path, method in (("/api/agent/v1/gpu/jobs/gpu_foreign", "GET"),
                             ("/api/agent/v1/gpu/jobs/gpu_foreign/logs", "GET"),
                             ("/api/agent/v1/gpu/jobs/gpu_foreign/cancel", "POST")):
            self.assertEqual(self.personal("write", path, method=method).status, 404)
