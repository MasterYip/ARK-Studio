"""GPU probe adapter tests: config safety, mocked SSH, dry-run dispatch.

No test opens a network connection or runs a remote command. The live transport
is exercised only through ``ssh_binary`` returning nothing (the missing-OpenSSH
path), while probes use injected fake runners.
"""
import contextlib
import importlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from arkstudio import gpu_probe
from arkstudio.server import App, serve

VENDOR = gpu_probe.VENDOR
SAMPLE_A = """0, NVIDIA H200, 143771, 1200, 3
1, NVIDIA H200, 143771, 143000, 98
2, NVIDIA RTX 4090, 24564, [N/A], [N/A]
"""
SAMPLE_B = "0, NVIDIA H20, 97871, 0, 0\n"
SECRET_KEYS = {
    "password", "passphrase", "passwd", "secret", "token", "api_key",
    "apikey", "private_key", "identity", "identity_file", "key",
}

ENABLED_CONFIG = """\
enabled: true
defaults:
  port: 22
  user: ubuntu
  timeout_seconds: 6
servers:
  - name: gpu-a
    host: gpu-a.example.com
    tags: [h200, primary]
  - name: gpu-b
    host: 192.0.2.10
    port: 2222
  - name: down
    host: 198.51.100.7
  - name: authbad
    host: 198.51.100.8
  - name: off
    host: 198.51.100.9
    enabled: false
"""


def _result(stdout="", *, returncode=0, error_kind="", message="", duration_ms=5):
    return VENDOR.ssh.CommandResult(
        returncode, stdout=stdout, error_kind=error_kind,
        message=message, duration_ms=duration_ms,
    )


class FakeRunner:
    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __call__(self, server, *, timeout=None):
        self.calls.append((server["name"], timeout))
        value = self.responses[server["name"]]
        return value(server, timeout) if callable(value) else value


def _walk_keys(value):
    if isinstance(value, dict):
        for key, item in value.items():
            yield key
            yield from _walk_keys(item)
    elif isinstance(value, list):
        for item in value:
            yield from _walk_keys(item)


class ConfigTests(unittest.TestCase):
    def test_example_is_public_and_disabled(self):
        example = Path(VENDOR.__file__).resolve().parent / "servers_rp.example.yaml"
        config = VENDOR.config.load_config(example)
        self.assertFalse(config["enabled"])
        self.assertEqual(3, len(config["servers"]))
        for server in config["servers"]:
            self.assertLessEqual(set(server), set(VENDOR.config.PUBLIC_SERVER_FIELDS))
            self.assertNotRegex(server["host"], r"[\s;@/]")
            self.assertNotIn("password", server)
        self.assertEqual({"port": 22, "user": "ubuntu", "timeout_seconds": 8},
                         config["defaults"])

    def test_rejects_credential_fields(self):
        for field in ("password", "private_key", "identity_file", "api_token"):
            source = f"servers:\n  - name: node\n    host: 192.0.2.1\n    {field}: value\n"
            with self.subTest(field=field):
                with self.assertRaisesRegex(VENDOR.config.ConfigError, "credential field"):
                    VENDOR.config.validate(VENDOR.config.parse_yaml(source))

    def test_rejects_unknown_fields_bad_hosts_ports_and_duplicates(self):
        cases = {
            "unknown field": "servers:\n  - name: node\n    host: 192.0.2.1\n    color: red\n",
            "host": "servers:\n  - name: node\n    host: 'evil; rm -rf /'\n",
            "host": "servers:\n  - name: node\n    host: user@gpu.example.com\n",
            "port": "servers:\n  - name: node\n    host: 192.0.2.1\n    port: 70000\n",
            "duplicate": "servers:\n  - name: node\n    host: 192.0.2.1\n  - name: node\n    host: 192.0.2.2\n",
        }
        for marker, source in cases.items():
            with self.subTest(source=source):
                with self.assertRaisesRegex(VENDOR.config.ConfigError, marker):
                    VENDOR.config.validate(VENDOR.config.parse_yaml(source))

    def test_parses_comments_quotes_and_inline_lists(self):
        source = """\
# leading comment
enabled: true
servers:
  - name: "node-1"
    host: gpu-1.example.com
    note: "has: a colon, and spaces"
    tags: [4090, 200, h200]
    timeout_seconds: 12
"""
        config = VENDOR.config.validate(VENDOR.config.parse_yaml(source))
        self.assertTrue(config["enabled"])
        server = config["servers"][0]
        self.assertEqual("has: a colon, and spaces", server["note"])
        self.assertEqual(["4090", "200", "h200"], server["tags"])
        self.assertEqual(12, server["timeout_seconds"])

    def test_missing_file_is_not_a_crash(self):
        with self.assertRaises(VENDOR.config.ConfigNotFound):
            VENDOR.config.load_config(Path(tempfile.gettempdir()) / "ark-no-such-gpu.yaml")

    def test_tabs_and_document_markers_are_rejected(self):
        for source in ("servers:\n\t- name: x\n", "---\nservers: []\n"):
            with self.subTest(source=source):
                with self.assertRaises(VENDOR.config.ConfigError):
                    VENDOR.config.parse_yaml(source)


class ProbeParsingTests(unittest.TestCase):
    def test_nvidia_smi_csv_and_missing_values(self):
        gpus, warnings = VENDOR.probe.parse_nvidia_smi(SAMPLE_A)
        self.assertEqual(3, len(gpus))
        self.assertEqual(142571.0, gpus[0]["free_memory_mb"])
        self.assertAlmostEqual(138293.9, gpus[0]["score"], places=1)
        self.assertIsNone(gpus[2]["free_memory_mb"])
        self.assertIsNone(gpus[2]["score"])
        missing_total, warnings = VENDOR.probe.parse_nvidia_smi(
            "4, Mystery GPU, [N/A], [N/A], [N/A]\n")
        self.assertIsNone(missing_total[0]["memory_total_mb"])
        self.assertTrue(any("total memory" in warning for warning in warnings))
        self.assertEqual([], [gpu for gpu in gpus if gpu["score"] == 0])

    def test_malformed_lines_become_warnings(self):
        gpus, warnings = VENDOR.probe.parse_nvidia_smi("not, enough\n0, GPU, 100, 0, 0\n")
        self.assertEqual(1, len(gpus))
        self.assertTrue(any("malformed" in warning for warning in warnings))

    def test_rank_skips_full_or_tiny_free_gpus(self):
        results = [
            {"ok": True, "server": "a", "host": "a", "gpus": [
                {"index": 0, "name": "A", "free_memory_mb": 5000.0, "utilization_pct": 10.0, "score": 4500.0},
                {"index": 1, "name": "A", "free_memory_mb": 100.0, "utilization_pct": 0.0, "score": 100.0},
                {"index": 2, "name": "A", "free_memory_mb": 5000.0, "utilization_pct": 100.0, "score": 0.0},
                {"index": 3, "name": "A", "free_memory_mb": None, "utilization_pct": 0.0, "score": None},
            ]},
            {"ok": False, "server": "b", "host": "b", "gpus": []},
            {"ok": True, "server": "c", "host": "c", "gpus": [
                {"index": 0, "name": "C", "free_memory_mb": 9000.0, "utilization_pct": 0.0, "score": 9000.0},
            ]},
        ]
        ranked = VENDOR.probe.rank_gpus(results)
        self.assertEqual(["c", "a"], [item["server"] for item in ranked])
        self.assertEqual(2, len(ranked))

    def test_bad_command_exit_is_data_not_an_exception(self):
        def runner(server, *, timeout=None):
            return _result(returncode=127, error_kind="command_failed",
                           message="nvidia-smi: command not found", stdout="")

        result = VENDOR.probe.probe_server({"name": "n", "host": "h", "port": 22,
                                            "user": "u", "timeout_seconds": 5}, runner)
        self.assertFalse(result["ok"])
        self.assertEqual("command_failed", result["error"]["kind"])
        self.assertIn("nvidia-smi", result["error"]["message"])

    def test_runner_exception_is_contained(self):
        def runner(server, *, timeout=None):
            raise RuntimeError("boom")

        result = VENDOR.probe.probe_server({"name": "n", "host": "h", "port": 22,
                                            "user": "u"}, runner)
        self.assertFalse(result["ok"])
        self.assertEqual("runner_error", result["error"]["kind"])
        self.assertIn("boom", result["error"]["message"])


def _gpu(index, used, total=24000.0, util=0.0):
    free = total - used
    gpu = {"index": index, "name": "RTX 4090", "memory_total_mb": total,
           "memory_used_mb": used, "free_memory_mb": free, "utilization_pct": util,
           "score": round(free * (1 - util / 100.0), 1)}
    gpu["occupancy_pct"] = VENDOR.probe.occupancy_pct(gpu)
    return gpu


class ClaimDispatchTests(unittest.TestCase):
    """Dispatch rule: filter GPUs that fit the job's memory claim, then least utilization."""

    CFG = {"queue": {"max_jobs_per_gpu": 2, "allow_colocation": True},
           "servers": [{"name": "gpu-server-1"}, {"name": "gpu-server-2"}]}

    def job(self, claim=8000, count=1, max_util=90):
        return {"gpu_count": count, "min_free_memory_mb": claim, "max_utilization_pct": max_util}

    def select(self, results, *, claim=8000, count=1, active=None, reserved=None):
        return VENDOR.queue.GpuQueue._select(None, self.job(claim, count), self.CFG, results,
                                             active or {}, reserved or {})

    def test_filters_by_memory_claim_then_lowest_util(self):
        too_small = _gpu(0, used=20000, util=0)    # 4 GB free: cannot hold an 8 GB claim
        busy = _gpu(1, used=2000, util=40)
        calm = _gpu(2, used=12000, util=5)         # 12 GB free: fits, least utilized
        _, gpus = self.select([{"ok": True, "server": "gpu-server-1", "gpus": [too_small, busy, calm]}])
        self.assertEqual([2], [gpu["index"] for gpu in gpus])

    def test_util_tie_prefers_more_free_memory(self):
        a, b = _gpu(0, used=10000, util=0), _gpu(1, used=1000, util=0)
        _, gpus = self.select([{"ok": True, "server": "gpu-server-1", "gpus": [a, b]}])
        self.assertEqual(1, gpus[0]["index"])

    def test_launching_claims_are_subtracted_from_free_memory(self):
        a, b = _gpu(0, used=1000, util=0), _gpu(1, used=1000, util=10)
        # GPU 0 shows 23 GB free, but 20 GB is claimed by a job still launching.
        _, gpus = self.select([{"ok": True, "server": "gpu-server-1", "gpus": [a, b]}],
                              active={("gpu-server-1", 0): 1}, reserved={("gpu-server-1", 0): 20000})
        self.assertEqual(1, gpus[0]["index"])

    def test_no_gpu_fits_claim_returns_none(self):
        result = {"ok": True, "server": "gpu-server-1", "gpus": [_gpu(0, used=20000)]}
        self.assertIsNone(self.select([result], claim=8000))

    def test_multi_gpu_compares_busiest_selected_gpu(self):
        one = {"ok": True, "server": "gpu-server-1", "gpus": [_gpu(0, used=500, util=0), _gpu(1, used=500, util=60)]}
        two = {"ok": True, "server": "gpu-server-2", "gpus": [_gpu(0, used=500, util=10), _gpu(1, used=500, util=10)]}
        self.assertEqual("gpu-server-1", self.select([one, two], count=1)[0]["name"])
        server, gpus = self.select([one, two], count=2)
        self.assertEqual("gpu-server-2", server["name"])
        self.assertEqual(2, len(gpus))

    def test_same_tick_placements_do_not_overcommit_one_gpu(self):
        cfg = {"queue": {"enabled": True, "max_jobs_per_gpu": 4, "allow_colocation": True},
               "servers": [{"name": "gpu-server-1"}]}
        results = [{"ok": True, "server": "gpu-server-1",
                    "gpus": [_gpu(0, used=4000, util=0), _gpu(1, used=4000, util=20)]}]
        active, reserved, placed = {}, {}, []
        for _ in range(2):
            job = {"gpu_count": 1, "min_free_memory_mb": 12000, "max_utilization_pct": 90}
            server, gpus = VENDOR.queue.GpuQueue._select(None, job, cfg, results, active, reserved)
            key = (server["name"], gpus[0]["index"])
            active[key] = active.get(key, 0) + 1
            reserved[key] = reserved.get(key, 0) + job["min_free_memory_mb"]
            placed.append(gpus[0]["index"])
        # GPU 0 (20 GB free, 0% util) takes the first 12 GB job; only 8 GB remain, so the
        # second job goes to GPU 1 even though GPU 1 is busier.
        self.assertEqual([0, 1], placed)

    def test_rank_gpus_matches_queue_order(self):
        results = [{"ok": True, "server": "gpu-server-1", "host": "h",
                    "gpus": [_gpu(0, used=1000, util=30), _gpu(1, used=20000, util=0),
                             _gpu(2, used=1000, util=0)]}]
        ranked = VENDOR.probe.rank_gpus(results)
        self.assertEqual([2, 1, 0], [item["gpu_index"] for item in ranked])
        self.assertEqual([1, 2, 3], [item["rank"] for item in ranked])

    def test_claim_aliases(self):
        cfg = {"queue": {"enabled": True, "min_free_memory_mb": 1024}, "servers": []}
        base = {"command": "python train.py", "work_dir": "/data/x"}
        validate = lambda body: VENDOR.queue.GpuQueue._validate(None, {**base, **body}, cfg)
        self.assertEqual(16000, validate({"gpu_memory_mb": 16000})["min_free_memory_mb"])
        self.assertEqual(9000, validate({"memory_mb": 9000})["min_free_memory_mb"])
        self.assertEqual(7000, validate({"min_free_memory_mb": 7000, "gpu_memory_mb": 1})["min_free_memory_mb"])
        self.assertEqual(1024, validate({})["min_free_memory_mb"])


class ProcessAttributionTests(unittest.TestCase):
    NL = chr(10)

    def system(self, *sections):
        return self.NL.join(line for section in sections for line in section) + self.NL

    BUS = ["__ARK_BUS__", "0, 00000000:01:00.0", "1, 00000000:02:00.0"]
    PROC = ["__ARK_PROC__", "111, 00000000:01:00.0, 12000",
            "222, 00000000:01:00.0, [N/A]", "333, 00000000:02:00.0, 4000"]
    ENV = ["__ARK_ENV__", "111 gpu_abc", "222 gpu_abc"]

    def test_pmon_gives_per_process_compute_and_na_memory_is_kept(self):
        pmon = ["__ARK_PMON__",
                "# gpu         pid  type    sm   mem   enc   dec   command",
                "# Idx           #   C/G     %     %     %     %   name",
                "    0        111     C    95    40     -     -   python",
                "    0        222     C     -     -     -     -   python",
                "    1          -     -     -     -     -     -   -"]
        out = VENDOR.probe.parse_system(self.system(self.BUS, self.PROC, pmon, self.ENV))
        self.assertTrue(out["process_util_available"])
        by_pid = {row["pid"]: row for row in out["processes"]}
        self.assertEqual((0, "gpu_abc", 12000, 95.0),
                         (by_pid[111]["gpu_index"], by_pid[111]["job_id"],
                          by_pid[111]["memory_mb"], by_pid[111]["sm_pct"]))
        self.assertIsNone(by_pid[222]["memory_mb"])   # [N/A] no longer drops the process
        self.assertEqual(0.0, by_pid[222]["sm_pct"])  # pmon "-" = no measurable compute
        self.assertEqual("", by_pid[333]["job_id"])

    def test_without_pmon_compute_share_is_unknown(self):
        out = VENDOR.probe.parse_system(self.system(self.BUS, self.PROC, ["__ARK_PMON__"], self.ENV))
        self.assertFalse(out["process_util_available"])
        self.assertTrue(all(row["sm_pct"] is None for row in out["processes"]))


class CliTests(unittest.TestCase):
    def test_probe_and_dispatch_cli_are_dry_run(self):
        cli = importlib.import_module("gpu_server_probe.cli")
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "gpu_servers.yaml"
            path.write_text(
                "enabled: true\nservers:\n  - name: gpu-a\n    host: 192.0.2.5\n",
                encoding="utf-8",
            )
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                code = cli.main(["--config", str(path), "probe"])
            self.assertEqual(0, code)
            self.assertIn("Dry run", output.getvalue())
            self.assertIn("nvidia-smi", output.getvalue())

            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                code = cli.main(["--config", str(path), "dispatch",
                                 "--server", "gpu-a", "--command", "python train.py"])
            self.assertEqual(0, code)
            data = json.loads(output.getvalue())
            self.assertTrue(data["dry_run"])
            self.assertFalse(data["executed"])


class ServiceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.config = self.dir / "gpu_servers.yaml"
        self.config.write_text(ENABLED_CONFIG, encoding="utf-8")

    def service(self, runner=None, environ=None):
        return gpu_probe.GpuService(self.config, runner=runner, environ=environ or {})

    def test_disabled_by_default_without_config(self):
        service = gpu_probe.GpuService(self.dir / "missing.yaml", environ={})
        status = service.status()
        self.assertFalse(status["enabled"])
        self.assertFalse(status["config_present"])
        self.assertEqual([], service.servers()["servers"])
        with self.assertRaises(gpu_probe.GpuError) as raised:
            service.probe({})
        self.assertEqual(409, raised.exception.status)
        self.assertIn("No GPU server list found", raised.exception.message)
        with self.assertRaises(gpu_probe.GpuError) as raised:
            service.prepare_dispatch({"command": "python train.py"})
        self.assertEqual(409, raised.exception.status)

    def test_config_present_but_disabled_requires_opt_in(self):
        self.config.write_text(ENABLED_CONFIG.replace("enabled: true", "enabled: false"),
                               encoding="utf-8")
        service = self.service(environ={})
        self.assertFalse(service.status()["enabled"])
        self.assertEqual([], service.servers()["servers"])
        with self.assertRaises(gpu_probe.GpuError) as raised:
            service.probe({})
        self.assertEqual(403, raised.exception.status)
        self.assertIn("disabled by default", raised.exception.message)

    def test_enabled_config_lists_only_public_fields(self):
        runner = FakeRunner({})
        service = self.service(runner=runner)
        status = service.status()
        self.assertTrue(status["available"])
        self.assertTrue(status["enabled"])
        self.assertEqual("config", status["enabled_source"])
        self.assertEqual(5, status["server_count"])

        listed = service.servers()
        self.assertTrue(listed["enabled"])
        self.assertEqual(5, listed["count"])
        self.assertEqual("gpu-a", listed["servers"][0]["name"])
        self.assertEqual([], runner.calls)  # listing never probes
        lowered = {str(key).lower() for key in _walk_keys(listed)}
        self.assertFalse(lowered & SECRET_KEYS, lowered & SECRET_KEYS)
        self.assertNotIn("host", status)  # status never enumerates hosts

    def test_env_flag_enables_probe(self):
        self.config.write_text(ENABLED_CONFIG.replace("enabled: true", "enabled: false"),
                               encoding="utf-8")
        service = self.service(environ={"ARK_GPU_PROBE": "1"})
        self.assertTrue(service.status()["enabled"])
        self.assertEqual("environment", service.status()["enabled_source"])

    def test_probe_runs_mocked_ssh_and_degrades_per_server(self):
        runner = FakeRunner({
            "gpu-a": _result(SAMPLE_A),
            "gpu-b": _result(SAMPLE_B),
            "down": _result(returncode=255, error_kind="unreachable",
                            message="ssh: connect to host 198.51.100.7 port 22: Connection refused"),
            "authbad": _result(returncode=255, error_kind="auth",
                               message="Permission denied (publickey)"),
        })
        service = self.service(runner=runner)
        with mock.patch.object(VENDOR.ssh, "run_remote",
                               side_effect=AssertionError("live SSH must not run in tests")):
            data = service.probe({})

        self.assertTrue(data["enabled"])
        self.assertTrue(data["read_only"])
        self.assertEqual(["off"], data["skipped"])
        self.assertEqual(4, data["summary"]["probed"])
        self.assertEqual(2, data["summary"]["ok"])
        self.assertEqual(2, data["summary"]["failed"])
        self.assertEqual(3, data["summary"]["free_gpus"])
        self.assertEqual(4, len(runner.calls))
        self.assertTrue(all(name != "off" for name, _ in runner.calls))

        by_name = {result["server"]: result for result in data["servers"]}
        self.assertEqual("unreachable", by_name["down"]["error"]["kind"])
        self.assertEqual("auth", by_name["authbad"]["error"]["kind"])
        self.assertEqual(3, len(by_name["gpu-a"]["gpus"]))
        self.assertEqual(0, by_name["gpu-b"]["gpus"][0]["index"])
        # Least utilized first: the idle H20 (0% util) before H200 #0 (3% util).
        self.assertEqual(["gpu-b", "gpu-a"], [item["server"] for item in data["ranked"][:2]])
        self.assertTrue(all(item["free_memory_mb"] >= 512 for item in data["ranked"]))
        self.assertEqual("nvidia-smi --query-gpu=index,name,memory.total,memory.used,"
                         "utilization.gpu,utilization.memory,temperature.gpu,"
                         "power.draw --format=csv,noheader,nounits",
                         by_name["gpu-a"]["command"])

    def test_probe_selection_and_bounds(self):
        runner = FakeRunner({"gpu-b": _result(SAMPLE_B)})
        service = self.service(runner=runner)
        data = service.probe({"servers": "gpu-b", "timeout_seconds": 3})
        self.assertEqual(["gpu-b"], [result["server"] for result in data["servers"]])
        self.assertEqual(3, data["timeout_seconds"])
        self.assertEqual(("gpu-b", 3), runner.calls[0])

        with self.assertRaises(gpu_probe.GpuError) as raised:
            service.probe({"servers": ["nope"]})
        self.assertEqual(400, raised.exception.status)
        self.assertIn("Unknown server", raised.exception.message)

        with self.assertRaises(gpu_probe.GpuError) as raised:
            service.probe({"timeout_seconds": 999})
        self.assertEqual(400, raised.exception.status)

    def test_dispatch_request_is_dry_run_and_bounded(self):
        service = self.service(runner=FakeRunner({}))
        request = service.prepare_dispatch({
            "server": "gpu-a",
            "gpus": "0,1",
            "job": "train",
            "project": "demo",
            "work_dir": "/data/project",
            "timeout_minutes": 30,
            "command": "python train.py --cfg a.yaml",
        })
        self.assertTrue(request["dry_run"])
        self.assertFalse(request["executed"])
        self.assertEqual([0, 1], request["request"]["gpus"])
        self.assertEqual("0,1", request["request"]["cuda_visible_devices"])
        self.assertIn("CUDA_VISIBLE_DEVICES=0,1 timeout 30m bash -lc",
                      request["remote_command_preview"])
        self.assertTrue(request["ssh_preview"].startswith("ssh -p 22 ubuntu@gpu-a.example.com"))
        self.assertIn("never opens the remote shell", " ".join(request["notes"]))
        lowered = {str(key).lower() for key in _walk_keys(request)}
        self.assertFalse(lowered & SECRET_KEYS, lowered & SECRET_KEYS)

        with_env = service.prepare_dispatch({
            "server": "gpu-a", "command": "python train.py",
            "env": {"WANDB_MODE": "offline"},
        })
        self.assertEqual({"WANDB_MODE": "offline"}, with_env["request"]["env"])
        self.assertIn("WANDB_MODE=offline", with_env["remote_command_preview"])

        invalid = [
            {"server": "gpu-a", "gpus": "0,1,2,3,4,5,6,7,8", "command": "python x"},
            {"server": "gpu-a", "gpus": "16", "command": "python x"},
            {"server": "nope", "command": "python x"},
            {"command": "python x"},
            {"server": "off", "command": "python x"},
            {"server": "gpu-a", "command": "  "},
            {"server": "gpu-a", "work_dir": "relative/path", "command": "python x"},
            {"server": "gpu-a", "timeout_minutes": 0, "command": "python x"},
            {"server": "gpu-a", "timeout_minutes": True, "command": "python x"},
            {"server": "gpu-a", "command": "x" * 16385},
            {"server": "gpu-a", "command": "python x", "execute": True},
        ]
        for body in invalid:
            with self.subTest(body=body):
                with self.assertRaises(gpu_probe.GpuError) as raised:
                    service.prepare_dispatch(body)
                self.assertEqual(400, raised.exception.status)

    def test_dispatch_preserves_long_commands_within_limit(self):
        service = self.service(runner=FakeRunner({}))
        for size in (2090, 16384):
            with self.subTest(size=size):
                prefix = "python train.py --label "
                command = prefix + "x" * (size - len(prefix))
                self.assertEqual(size, len(command))
                request = service.prepare_dispatch({"server": "gpu-a", "command": command})
                self.assertEqual(command, request["request"]["command"])
                self.assertTrue(request["dry_run"])
                self.assertFalse(request["executed"])

    def test_single_server_is_implicit_for_dispatch(self):
        config = self.dir / "one.yaml"
        config.write_text("enabled: true\nservers:\n  - name: solo\n    host: 192.0.2.5\n",
                          encoding="utf-8")
        service = gpu_probe.GpuService(config, environ={})
        request = service.prepare_dispatch({"command": "python train.py"})
        self.assertEqual("solo", request["server"]["name"])
        self.assertEqual("ark", request["request"]["project"])

    def test_missing_vendor_reports_actionable_state(self):
        with mock.patch.object(gpu_probe, "VENDOR", None):
            service = gpu_probe.GpuService(self.config, environ={})
            status = service.status()
            self.assertFalse(status["available"])
            self.assertFalse(status["enabled"])
            self.assertIsNone(status["vendor"])
            self.assertEqual([], service.servers()["servers"])
            with self.assertRaises(gpu_probe.GpuError) as raised:
                service.probe({})
            self.assertEqual(503, raised.exception.status)
            self.assertIn("vendored GPU probe adapter is not installed",
                          raised.exception.message)

    def test_missing_openssh_is_actionable_and_offline(self):
        server = {"name": "n", "host": "192.0.2.1", "port": 22, "user": "u",
                  "timeout_seconds": 3}
        with mock.patch.object(VENDOR.ssh.shutil, "which", return_value=None), \
                mock.patch.object(VENDOR.ssh.subprocess, "run",
                                  side_effect=AssertionError("must not spawn ssh")):
            deps = VENDOR.ssh.dependency_status({})
            self.assertFalse(deps["available"])
            self.assertIn("OpenSSH", deps["hint"])
            result = VENDOR.ssh.run_remote(server, environ={})
            self.assertEqual("missing_ssh", result.error_kind)
            self.assertIn("OpenSSH", result.message)
            self.assertFalse(result.ok)


class GpuApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.dir = Path(cls.tmp.name)
        cls.config = cls.dir / "gpu_servers.yaml"
        cls.config.write_text(ENABLED_CONFIG, encoding="utf-8")
        cls.runner = FakeRunner({
            "gpu-a": _result(SAMPLE_A),
            "gpu-b": _result(SAMPLE_B),
            "down": _result(returncode=255, error_kind="unreachable", message="Connection refused"),
            "authbad": _result(returncode=255, error_kind="auth", message="Permission denied"),
        })
        service = gpu_probe.GpuService(cls.config, runner=cls.runner, environ={})
        cls.app = App(cls.dir / "projects", seed_demo=True, gpu_service=service)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()

    def request(self, path, *, method="GET", body=None):
        import urllib.error
        import urllib.request

        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, json.loads(response.read() or b"{}")
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read() or b"{}")

    def test_status_and_servers(self):
        status, data = self.request("/api/gpu/status")
        self.assertEqual(200, status)
        self.assertTrue(data["available"])
        self.assertTrue(data["enabled"])
        self.assertEqual(5, data["server_count"])
        self.assertFalse(data["asyncssh"]["required"])

        status, listed = self.request("/api/gpu/servers")
        self.assertEqual(200, status)
        self.assertEqual(5, listed["count"])
        self.assertEqual("gpu-a", listed["servers"][0]["name"])
        lowered = {str(key).lower() for key in _walk_keys(listed)}
        self.assertFalse(lowered & SECRET_KEYS)

    def test_probe_endpoint(self):
        status, data = self.request("/api/gpu/probe", method="POST", body={})
        self.assertEqual(200, status)
        self.assertEqual(2, data["summary"]["ok"])
        # Least utilized first: the idle H20 (0% util) before H200 #0 (3% util).
        self.assertEqual(["gpu-b", "gpu-a"], [item["server"] for item in data["ranked"][:2]])

    def test_dispatch_endpoint(self):
        status, data = self.request("/api/gpu/dispatch/prepare", method="POST", body={
            "server": "gpu-a", "gpus": "0", "command": "python train.py",
        })
        self.assertEqual(200, status)
        self.assertTrue(data["dry_run"])
        self.assertFalse(data["executed"])
        self.assertEqual("ark-gpu-dispatch-request", data["format"])

        status, error = self.request("/api/gpu/dispatch/prepare", method="POST", body={
            "server": "gpu-a", "command": "python train.py", "execute": True,
        })
        self.assertEqual(400, status)
        self.assertFalse(error["ok"])
        self.assertIn("never executes", error["error"]["message"])


class GpuApiDisabledTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        missing = self.dir / "no-servers.yaml"
        service = gpu_probe.GpuService(missing, environ={})
        self.app = App(self.dir / "projects", seed_demo=False, gpu_service=service)
        self.httpd = serve(self.app, port=0, block=False)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def tearDown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.app.project.close()

    def request(self, path, *, method="GET", body=None):
        import urllib.error
        import urllib.request

        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, json.loads(response.read() or b"{}")
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read() or b"{}")

    def test_ark_stays_usable_and_gpu_is_read_only(self):
        status, bootstrap = self.request("/api/bootstrap")
        self.assertEqual(200, status)
        self.assertIn("summary", bootstrap)

        status, gpu_status = self.request("/api/gpu/status")
        self.assertEqual(200, status)
        self.assertFalse(gpu_status["enabled"])
        self.assertFalse(gpu_status["config_present"])

        status, listed = self.request("/api/gpu/servers")
        self.assertEqual(200, status)
        self.assertFalse(listed["enabled"])
        self.assertEqual([], listed["servers"])

        status, error = self.request("/api/gpu/probe", method="POST", body={})
        self.assertIn(status, (403, 409))
        self.assertFalse(error["ok"])
        self.assertIn("No GPU server list found", error["error"]["message"])

        status, error = self.request("/api/gpu/dispatch/prepare", method="POST", body={
            "command": "python train.py",
        })
        self.assertEqual(409, status)
        self.assertFalse(error["ok"])

    def test_invalid_config_is_reported_without_breaking_the_app(self):
        bad = self.dir / "bad-servers.yaml"
        bad.write_text("enabled: true\nservers:\n  - name: x\n    host: 'a b'\n",
                       encoding="utf-8")
        self.app.gpu = gpu_probe.GpuService(bad, environ={})
        status, data = self.request("/api/gpu/status")
        self.assertEqual(200, status)
        self.assertFalse(data["enabled"])
        self.assertIn("host", data["config_error"])
        status, error = self.request("/api/gpu/probe", method="POST", body={})
        self.assertEqual(422, status)
        self.assertFalse(error["ok"])


if __name__ == "__main__":
    unittest.main()

class GpuQueueTests(unittest.TestCase):
    def test_queue_places_and_launches_with_path_env_adaptation(self):
        import tempfile
        from pathlib import Path
        from vendor.gpu_server_probe import queue, ssh

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config = root / "servers.yaml"
            config.write_text(
                "enabled: true\n"
                "servers:\n"
                "  - name: gpu-1\n"
                "    host: gpu-1.example\n"
                "    tags: [4090]\n"
                "queue:\n"
                "  enabled: true\n"
                "  state_dir: /tmp/ark-gpu-queue-test\n"
                "  allow_colocation: true\n"
                "  max_jobs_per_gpu: 2\n"
                "  max_utilization_pct: 30\n"
                "  min_free_memory_mb: 1000\n"
                "  path_map:\n"
                "    - from: /mnt/local\n"
                "      to: /data/remote\n"
                "  env:\n"
                "    PYTHONUNBUFFERED: '1'\n",
                encoding="utf-8",
            )
            calls = []
            def probe_runner(server, timeout=None):
                return ssh.CommandResult(0, stdout="0, RTX 4090, 24564, 100, 5\n")
            def command_runner(server, command, timeout=None):
                calls.append(command)
                return ssh.CommandResult(0, stdout="4242\n")
            q = queue.GpuQueue(config, db_path=root / "queue.sqlite3",
                               probe_runner=probe_runner,
                               command_runner=command_runner)
            job = q.submit({"command": "python train.py", "work_dir": "/mnt/local/proj",
                            "gpu_count": 1, "job": "test", "project": "ark",
                            "env": {"FOO": "bar"}}, start=False)
            q.tick()
            result = q.get(job["id"])
            self.assertEqual(result["status"], "running")
            self.assertEqual(result["gpu_indices"], [0])
            self.assertTrue(calls)
            remote = calls[-1]
            self.assertIn("CUDA_VISIBLE_DEVICES=0", remote)
            self.assertIn("/data/remote/proj", remote)
            self.assertIn("FOO=bar", remote)
            q.close()
