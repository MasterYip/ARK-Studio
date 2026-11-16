"""Enabled queues resume without a browser; concurrent calls share one worker."""
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import tempfile
import time
import unittest
from unittest import mock

from arkstudio import gpu_probe
from arkstudio.server import App, serve


class QueueStartupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.path = self.root / "servers.yaml"
        self.path.write_text("enabled: true\nqueue:\n  enabled: true\nservers:\n  - name: sandbox\n    host: localhost\n    enabled: false\n")
        self.service = gpu_probe.GpuService(self.path, environ={})
        self.addCleanup(self.service.close)

    def test_http_startup_resumes_the_queue_before_any_request(self):
        with mock.patch.object(gpu_probe.VENDOR.queue, "GpuQueue") as constructor:
            app = App(self.root / "projects", seed_demo=False, gpu_service=self.service)
            try:
                httpd = serve(app, port=0, block=False)
                try:
                    constructor.assert_called_once()
                    constructor.return_value.start.assert_called_once()
                finally:
                    httpd.shutdown()
                    httpd.server_close()
            finally:
                app.shutdown()

    def test_concurrent_requests_create_only_one_worker(self):
        def construct(*args, **kwargs):
            time.sleep(.02)
            return mock.Mock()
        with mock.patch.object(gpu_probe.VENDOR.queue, "GpuQueue", side_effect=construct) as constructor:
            with ThreadPoolExecutor(max_workers=8) as pool:
                queues = list(pool.map(lambda _: self.service._queue_service(), range(8)))
            self.assertEqual(constructor.call_count, 1)
            self.assertTrue(all(queue is queues[0] for queue in queues))

    def test_missing_or_disabled_config_stays_inert(self):
        self.path.unlink()
        self.assertFalse(self.service.start_configured_queue())
        self.assertIsNone(self.service._queue)

    def test_optional_queue_failure_does_not_block_the_workbench(self):
        with mock.patch.object(self.service, "_queue_service", side_effect=OSError("state unavailable")):
            self.assertFalse(self.service.start_configured_queue())
        self.assertEqual(self.service.queue_status()["worker_error"], "state unavailable")
