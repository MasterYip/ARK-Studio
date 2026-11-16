"""Only release-versioned static assets may persist in the browser cache."""
import re
import gzip
import http.client
import unittest

from tests.python.auth.test_auth import AuthHttpMixin, cookie_from, request
from arkstudio.server import Handler


class StaticCacheTests(AuthHttpMixin, unittest.TestCase):
    def test_versioned_asset_cache_and_dynamic_privacy(self):
        cookie = cookie_from(self.login())
        headers = {"Cookie": cookie}
        page = request(self.base, "/", headers=headers)
        self.assertEqual(page.headers["cache-control"], "no-store")
        asset = re.search(r'src="(./js/app.js\?v=[^"]+)"', page.text).group(1)[1:]
        response = request(self.base, asset, headers=headers)
        self.assertEqual(response.status, 200)
        self.assertIn("private", response.headers["cache-control"])
        self.assertIn("immutable", response.headers["cache-control"])
        for path in ["/js/app.js", "/js/app.js?v=old-release", "/sw.js", "/api/bootstrap"]:
            self.assertEqual(request(self.base, path, headers=headers).headers["cache-control"], "no-store", path)
        version = asset.split("v=")[1]
        self.assertIn('./views/tasks.js?v=' + version, response.text)
        # A representation warmed by a signed-in user cannot bypass the gate.
        self.assertEqual(request(self.base, asset).status, 302)

    def test_cached_gzip_identity_head_and_viz_security(self):
        cookie = cookie_from(self.login())
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        self.addCleanup(conn.close)
        bodies = []
        for accept in ("gzip", "identity", "gzip;q=0", "gzip"):
            conn.request("GET", "/js/app.js", headers={"Cookie": cookie, "Accept-Encoding": accept})
            response = conn.getresponse()
            data = response.read()
            self.assertEqual(response.status, 200)
            self.assertEqual(response.getheader("Vary"), "Accept-Encoding")
            self.assertEqual(response.getheader("Cache-Control"), "no-store")
            self.assertEqual(response.getheader("X-Frame-Options"), "DENY")
            self.assertEqual(int(response.getheader("Content-Length")), len(data))
            if accept == "gzip":
                self.assertEqual(response.getheader("Content-Encoding"), "gzip")
                data = gzip.decompress(data)
            else:
                self.assertIsNone(response.getheader("Content-Encoding"))
            bodies.append(data)
        self.assertTrue(all(data == bodies[0] for data in bodies))
        conn.request("HEAD", "/js/app.js", headers={"Cookie": cookie, "Accept-Encoding": "gzip"})
        response = conn.getresponse()
        self.assertEqual(response.status, 200)
        self.assertEqual(response.getheader("Content-Encoding"), "gzip")
        self.assertEqual(response.read(), b"")
        conn.request("GET", "/viz-frame.html", headers={"Cookie": cookie})
        response = conn.getresponse()
        response.read()
        self.assertEqual(response.getheader("Content-Security-Policy"), Handler.VIZ_CSP)
        self.assertEqual(response.getheader("X-Frame-Options"), "SAMEORIGIN")

    def test_public_login_assets_keep_private_app_gated(self):
        page = self.login_page()
        self.assertEqual(page.headers["cache-control"], "no-store")
        self.assertNotIn("{{CSRF}}", page.text)
        for path in ["/css/startup.css", "/js/startup-graph.js", "/css/login.css", "/js/login.js",
                     "/js/startup-force.js", "/js/graph-core.js", "/js/icons.js",
                     "/brand/ark-lockup-loading.svg", "/brand/ark-lockup-loading-dark.svg"]:
            self.assertEqual(request(self.base, path).status, 200, path)
        self.assertEqual(request(self.base, "/js/app.js").status, 302)
        self.assertEqual(request(self.base, "/js/startup-graph.js/private").status, 302)


if __name__ == "__main__":
    unittest.main()
