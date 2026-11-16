"""An early error reply must reach the client even if the body is still unread.

http.client sends the request headers and body in separate writes.  When the
handler refuses a request from its headers alone (auth, permission) and closes
the connection with the body still arriving, the OS resets the socket and the
client loses the response (ConnectionAbortedError on Windows,
ConnectionResetError on Linux).  These tests send the body late on purpose.
"""
import json
import socket
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

from arkstudio import server as server_mod
from arkstudio.server import App, serve


class ErrorBodyDrainTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=False)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.port = cls.httpd.server_address[1]

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.shutdown()
        cls.tmp.cleanup()

    def post_late_body(self, body: bytes, *, send_body=True, delay=0.2) -> bytes:
        # The task-agent route rejects a bad token before reading the body.
        head = (f"POST /api/task-agent/v1/gpu/jobs HTTP/1.1\r\nHost: 127.0.0.1:{self.port}\r\n"
                f"Content-Type: application/json\r\nContent-Length: {len(body)}\r\n"
                "X-ARK-Read-Token: not-a-token\r\n\r\n").encode()
        with socket.create_connection(("127.0.0.1", self.port), timeout=10) as sock:
            sock.sendall(head)
            time.sleep(delay)  # the handler has the headers and replies meanwhile
            if send_body:
                sock.sendall(body)
            data = b""
            while chunk := sock.recv(65536):
                data += chunk
        return data

    def test_refused_request_with_late_body_still_gets_its_response(self):
        body = json.dumps({"command": "true"}).encode()
        for _ in range(5):
            response = self.post_late_body(body)
            self.assertTrue(response.startswith(b"HTTP/1.1 401 "), response[:200])
            self.assertIn(b'"ok": false', response)

    def test_body_that_never_arrives_does_not_hold_the_reply(self):
        with mock.patch.object(server_mod, "ERROR_DRAIN_TIMEOUT", 0.3):
            started = time.monotonic()
            response = self.post_late_body(b"x" * 64, send_body=False, delay=0)
        self.assertTrue(response.startswith(b"HTTP/1.1 401 "), response[:200])
        self.assertLess(time.monotonic() - started, 5)


if __name__ == "__main__":
    unittest.main()
