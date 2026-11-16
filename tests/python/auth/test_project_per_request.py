"""The active project belongs to the request (tab/session/person), never the
server: creating or opening a project only moves the requester.

Two accounts, several projects, real HTTP.  A browser tab sends its project
as ``X-ARK-Project``; without one the session's project, then the person's
last project, then the newest readable project is used.
"""
from __future__ import annotations

import unittest

from tests.python.auth.test_org import AuthHttpCase


class ProjectPerRequestTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.assertEqual(self.create_user("bob", "bob-pass").status, 200)
        self.bob = self.login("bob", "bob-pass")
        self.owner_start = self.project_id()
        self.assertEqual(self.grant(self.owner_start, user="bob", permission="write").status, 200)

    def summary(self, cookie, project=""):
        headers = {"X-ARK-Project": project} if project else None
        response = self.api("/api/bootstrap", cookie=cookie, headers=headers)
        self.assertEqual(response.status, 200, response.text)
        return response.json()["summary"]["id"]

    def create(self, cookie, name, project=""):
        headers = {"X-ARK-Project": project} if project else None
        response = self.api("/api/projects", cookie=cookie, method="POST", headers=headers,
                            body={"name": name, "template": "blank"})
        self.assertEqual(response.status, 200, response.text)
        return response.json()["summary"]["id"]

    def test_creating_a_project_never_moves_another_user(self):
        bob_before = self.summary(self.bob)
        units_before = self.api("/api/units", cookie=self.bob).json()
        # The owner gets an automatic admin grant on every new project, so the
        # new project is visible to them -- it must still not become anyone
        # else's current project.
        bobs_new = self.create(self.bob, "Bob Lab")
        self.assertEqual(self.summary(self.bob), bobs_new)
        owner_now = self.summary(self.owner)
        self.assertNotEqual(owner_now, bobs_new)
        owner_new = self.create(self.owner, "Owner Lab")
        self.assertEqual(self.summary(self.owner), owner_new)
        # Bob still sees his own project: bootstrap, revision and units.
        self.assertEqual(self.summary(self.bob), bobs_new)
        self.assertNotEqual(bob_before, owner_new)
        revision = self.api("/api/revision", cookie=self.bob)
        self.assertEqual(revision.status, 200, revision.text)
        units = self.api("/api/units", cookie=self.bob).json()
        self.assertIn("items", units)
        self.assertIn("items", units_before)

    def test_restart_does_not_move_an_owner_to_a_newer_project(self):
        # Root cause on the shared server: the per-session project lived only
        # in memory; after a restart every session fell back to the newest
        # project it could read -- and owners can read every new project.
        owner_project = self.summary(self.owner)
        bobs_new = self.create(self.bob, "Bob After Restart")
        with self.app.lock:
            self.app._session_projects.clear()
        self.assertEqual(self.summary(self.owner), owner_project)
        self.assertNotEqual(self.summary(self.owner), bobs_new)

    def test_opening_a_project_only_moves_the_requester(self):
        other = self.create(self.owner, "Shared Lab")
        self.assertEqual(self.grant(other, user="bob", permission="read").status, 200)
        bob_project = self.summary(self.bob)
        opened = self.api("/api/projects/open", cookie=self.owner, method="POST",
                          body={"id": self.owner_start}).json()
        self.assertEqual(opened["summary"]["id"], self.owner_start)
        self.assertEqual(self.summary(self.bob), bob_project)

    def test_two_tabs_of_one_session_keep_their_own_project(self):
        first = self.summary(self.owner)
        # Tab 2 creates a project while sending its current project id.
        created = self.create(self.owner, "Tab Two Lab", project=first)
        self.assertNotEqual(created, first)
        # Tab 1 keeps sending its own project and keeps it.
        self.assertEqual(self.summary(self.owner, project=first), first)
        units = self.api("/api/units", cookie=self.owner, headers={"X-ARK-Project": first})
        self.assertEqual(units.status, 200, units.text)
        # Tab 2 (and a brand-new tab without a header) follow the new project.
        self.assertEqual(self.summary(self.owner, project=created), created)
        self.assertEqual(self.summary(self.owner), created)

    def test_tab_header_for_an_unreadable_project_falls_back(self):
        private = self.create(self.owner, "Owner Only")
        bob_default = self.summary(self.bob)
        self.assertNotEqual(bob_default, private)
        self.assertEqual(self.summary(self.bob, project=private), bob_default)
        units = self.api("/api/units", cookie=self.bob, headers={"X-ARK-Project": private})
        self.assertEqual(units.status, 200, units.text)

    def test_last_project_survives_a_new_session(self):
        created = self.create(self.bob, "Bob Notes")
        # A new sign-in (another device, or after a restart drops the session
        # map) resumes the person's last project, not the newest one.
        newer = self.create(self.owner, "Newest Owner Lab")
        self.assertEqual(self.grant(newer, user="bob", permission="read").status, 200)
        bob_again = self.login("bob", "bob-pass")
        self.assertEqual(self.summary(bob_again), created)
        with self.app.lock:
            self.app._session_projects.clear()
        self.assertEqual(self.summary(self.bob), created)

    def test_media_urls_can_name_the_tab_project(self):
        first = self.summary(self.owner)
        created = self.create(self.owner, "Query Lab", project=first)
        self.assertNotEqual(created, first)
        response = self.api(f"/api/bootstrap?ark_project={first}", cookie=self.owner)
        self.assertEqual(response.json()["summary"]["id"], first)


if __name__ == "__main__":
    unittest.main()
