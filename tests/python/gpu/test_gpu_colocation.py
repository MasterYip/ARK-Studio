import tempfile
import unittest
from pathlib import Path

from vendor.gpu_server_probe import config, queue, ssh


class ColocationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.path = root / "servers.yaml"
        self.path.write_text(
            "enabled: true\nservers:\n  - name: gpu-1\n    host: gpu-1.example\n"
            "queue:\n  enabled: true\n  min_free_memory_mb: 9000\n"
            "  max_utilization_pct: 60\n", encoding="utf-8")
        self.free = 20000
        self.processes = []
        self.launches = []
        self.q = queue.GpuQueue(self.path, db_path=root / "queue.sqlite3",
                                probe_runner=self.probe, command_runner=self.command)
        self.addCleanup(self.q.close)

    def probe(self, server, timeout=None):
        lines = ["__ARK_BUS__", "0, 00000000:01:00.0", "__ARK_PROC__"]
        lines += [f"{pid}, 00000000:01:00.0, {memory}"
                  for pid, job, memory in self.processes]
        lines += ["__ARK_ENV__"]
        lines += [f"{pid} {job}" for pid, job, memory in self.processes if job]
        lines += ["__ARK_GPU__", f"0, RTX 4090, 24000, {24000 - self.free}, 25"]
        return ssh.CommandResult(0, stdout="\n".join(lines) + "\n")

    def command(self, server, command, timeout=None):
        if "setsid -f" in command:
            self.launches.append(command)
            return ssh.CommandResult(0, stdout=f"{4200 + len(self.launches)}\n")
        return ssh.CommandResult(0, stdout="RUNNING\n")

    def submit(self):
        return self.q.submit({"command": "python train.py", "job": "train",
                              "project": "test"}, start=False)["id"]

    def status(self, job):
        return self.q.get(job)["status"]

    def test_shared_card_across_ticks_and_delayed_cuda_allocation(self):
        first, second, third = [self.submit() for _ in range(3)]
        self.q.tick()
        self.assertEqual(["running", "running", "queued"],
                         [self.status(job) for job in (first, second, third)])
        self.assertEqual([0], self.q.get(first)["gpu_indices"])
        self.assertEqual([0], self.q.get(second)["gpu_indices"])
        # Both launchers are alive but neither has allocated CUDA memory yet.
        self.q.tick()
        self.assertEqual("queued", self.status(third))
        self.assertEqual(2, len(self.launches))
        # One job has only initialized part of its claim. The rest stays reserved.
        self.processes = [(100, first, 2000)]
        self.free = 18000
        self.q.tick()
        self.assertEqual("queued", self.status(third))
        # Child processes together fulfill each job's claim, without double counting.
        self.processes = [(100, first, 4000), (101, first, 5000), (200, second, 9000)]
        self.free = 2000
        self.q.tick()
        self.assertEqual("queued", self.status(third))
        # Completing one job releases its claim and allows another onto the same GPU.
        with self.q._connect() as conn:
            conn.execute("UPDATE gpu_jobs SET status='completed' WHERE id=?", (first,))
            conn.commit()
        self.processes = [(200, second, 9000)]
        self.free = 11000
        self.q.tick()
        self.assertEqual("running", self.status(third))
        self.assertEqual([0], self.q.get(third)["gpu_indices"])
        self.assertEqual(3, len(self.launches))

    def test_later_submission_can_share_with_an_existing_job(self):
        first = self.submit()
        self.q.tick()
        self.processes = [(100, first, 9000)]
        self.free = 11000
        second = self.submit()
        self.q.tick()
        self.assertEqual("running", self.status(second))
        self.assertEqual([0], self.q.get(second)["gpu_indices"])

    def test_other_users_processes_do_not_satisfy_ark_claims(self):
        first = self.submit()
        self.q.tick()
        second = self.submit()
        self.processes = [(999, "", 6000)]
        self.free = 14000
        self.q.tick()
        self.assertEqual("queued", self.status(second))
        self.assertEqual("running", self.status(first))

    def test_unreadable_ownership_keeps_claim_reserved(self):
        first = self.submit()
        self.q.tick()
        second = self.submit()
        self.processes = [(100, "", 9000)]
        self.free = 11000
        self.q.tick()
        self.assertEqual("queued", self.status(second))
        self.assertEqual("running", self.status(first))

    def test_explicit_count_and_exclusive_limits_are_respected(self):
        for policy in ("max_jobs_per_gpu: 1", "allow_colocation: false"):
            with self.subTest(policy=policy):
                cfg = config.load_config(self.path)
                cfg["queue"].update(config.parse_yaml("queue:\n  " + policy)["queue"])
                results = [{"ok": True, "server": "gpu-1", "gpus": [
                    {"index": 0, "free_memory_mb": 20000, "utilization_pct": 0}]}]
                selected = self.q._select({"gpu_count": 1, "min_free_memory_mb": 9000,
                                           "max_utilization_pct": 60}, cfg, results,
                                          {("gpu-1", 0): 1})
                self.assertIsNone(selected)

    def test_default_has_no_job_count_limit_but_checks_utilization(self):
        cfg = config.load_config(self.path)
        self.assertTrue(cfg["queue"]["allow_colocation"])
        self.assertEqual(0, cfg["queue"]["max_jobs_per_gpu"])
        results = [{"ok": True, "server": "gpu-1", "gpus": [
            {"index": 0, "free_memory_mb": 20000, "utilization_pct": 25}]}]
        job = {"gpu_count": 1, "min_free_memory_mb": 9000, "max_utilization_pct": 60}
        self.assertIsNotNone(self.q._select(job, cfg, results, {("gpu-1", 0): 20}))
        job["max_utilization_pct"] = 20
        self.assertIsNone(self.q._select(job, cfg, results, {("gpu-1", 0): 20}))


if __name__ == "__main__":
    unittest.main()
