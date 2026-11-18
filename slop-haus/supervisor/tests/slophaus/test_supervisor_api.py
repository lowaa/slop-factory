from __future__ import annotations

import json
import logging
import unittest

from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.slophaus.supervisor_api import SlotRef, SupervisorApi
from slop_haus_supervisor.slophaus.transport import ApiError, Transport
from slop_haus_supervisor.workspace.manifest_source import ApiManifestSource
from slop_haus_supervisor.workspace.reconcile import Manifest, RepoSpec
from stub_server import StubServer, serving

SLOTS = "/api/supervisor/slots"


class SupervisorApiTest(unittest.TestCase):
    def setUp(self) -> None:
        self.stub = StubServer()
        serving_cm = serving(self.stub)
        serving_cm.__enter__()
        self.addCleanup(serving_cm.__exit__, None, None, None)
        self.api = SupervisorApi(Transport(self.stub.url), lambda: "sup-token")

    def test_create_sends_the_name_with_the_supervisors_token(self) -> None:
        self.stub.on("POST", SLOTS, (201, {"id": "abc", "name": "w1"}))

        slot = self.api.create_slot("w1")

        self.assertEqual(SlotRef("abc", "w1"), slot)
        seen = self.stub.requests("POST", SLOTS)[0]
        self.assertEqual("Bearer sup-token", seen.auth)
        self.assertEqual({"name": "w1"}, json.loads(seen.body))

    def test_desired_state_is_read_and_observed_state_reported_with_the_supervisors_token(self) -> None:
        self.stub.on("GET", "/api/supervisor/desired_state", (200, {"revision": 3, "agents": []}))
        self.stub.on("POST", "/api/supervisor/desired_state/observed", (204, None))

        self.assertEqual({"revision": 3, "agents": []}, self.api.desired_state())
        self.api.report_observed([{"id": "x", "generation": 1, "phase": "running"}])

        seen = self.stub.requests("POST", "/api/supervisor/desired_state/observed")[0]
        self.assertEqual("Bearer sup-token", seen.auth)
        self.assertEqual({"agents": [{"id": "x", "generation": 1, "phase": "running"}]}, json.loads(seen.body))

    def test_adopt_posts_the_name_to_the_adopt_route(self) -> None:
        self.stub.on("POST", SLOTS + "/adopt", (200, {"id": "abc", "name": "w1"}))

        self.assertEqual(SlotRef("abc", "w1"), self.api.adopt_slot("w1"))

        seen = self.stub.requests("POST", SLOTS + "/adopt")[0]
        self.assertEqual("Bearer sup-token", seen.auth)
        self.assertEqual({"name": "w1"}, json.loads(seen.body))

    def test_a_refused_adoption_carries_the_status(self) -> None:
        self.stub.on("POST", SLOTS + "/adopt", (404, {"detail": "not found"}))

        with self.assertRaises(ApiError) as caught:
            self.api.adopt_slot("w1")

        self.assertEqual(404, caught.exception.status)

    def test_a_refusal_carries_the_servers_words_and_status(self) -> None:
        self.stub.on("POST", SLOTS, (409, {"detail": "the supervisor is at its slot cap: retire a slot first"}))

        with self.assertRaises(ApiError) as caught:
            self.api.create_slot("w1")

        self.assertEqual(409, caught.exception.status)
        self.assertIn("slot cap", str(caught.exception))
        self.assertIn("'w1'", str(caught.exception))

    def test_find_matches_by_name_and_misses_cleanly(self) -> None:
        self.stub.on("GET", SLOTS, (200, {"slots": [{"id": "1", "name": "a"}, {"id": "2", "name": "b"}], "slot_cap": 3}))

        self.assertEqual(SlotRef("2", "b"), self.api.find_slot("b"))
        self.assertEqual("Bearer sup-token", self.stub.requests("GET", SLOTS)[0].auth)
        self.assertIsNone(self.api.find_slot("zzz"))

    def test_is_retired_reads_the_archived_listing(self) -> None:
        listing = f"{SLOTS}?include_archived=true"
        self.stub.on(
            "GET",
            listing,
            (200, {"slots": [{"id": "1", "name": "a", "archived_at": None}, {"id": "2", "name": "b", "archived_at": "2026-01-01T00:00:00Z"}]}),
        )

        self.assertTrue(self.api.is_retired("b"))
        self.assertFalse(self.api.is_retired("a"))
        self.assertFalse(self.api.is_retired("zzz"))
        self.assertEqual("Bearer sup-token", self.stub.requests("GET", listing)[0].auth)

    def test_taken_names_include_retired_slots(self) -> None:
        listing = f"{SLOTS}?include_archived=true"
        self.stub.on("GET", listing, (200, {"slots": [{"id": "1", "name": "a", "archived_at": None}, {"id": "2", "name": "b", "archived_at": "2026-01-01T00:00:00Z"}]}))

        self.assertEqual({"a", "b"}, self.api.taken_names())

    def test_retire_deletes_the_slot_by_id(self) -> None:
        self.stub.on("DELETE", f"{SLOTS}/2", (200, {}))

        self.api.retire_slot(SlotRef("2", "b"))

        self.assertEqual(["Bearer sup-token"], [r.auth for r in self.stub.requests("DELETE", f"{SLOTS}/2")])

    def test_credential_is_parsed_and_its_tokens_stay_out_of_repr(self) -> None:
        self.stub.on("POST", f"{SLOTS}/2/credential", (200, {"access_token": "acc-secret", "refresh_token": "ref-secret", "expires_in": 900}))

        tokens = self.api.issue_credential(SlotRef("2", "b"), now=lambda: 1000.0)

        self.assertEqual(TokenPair("acc-secret", "ref-secret", 1900), tokens)
        self.assertEqual("Bearer sup-token", self.stub.requests("POST", f"{SLOTS}/2/credential")[0].auth)
        self.assertNotIn("secret", repr(tokens))

    def test_a_credential_without_a_refresh_token_is_refused(self) -> None:
        self.stub.on("POST", f"{SLOTS}/2/credential", (200, {"access_token": "acc-secret"}))

        with self.assertRaises(ApiError) as caught:
            self.api.issue_credential(SlotRef("2", "b"))

        self.assertNotIn("acc-secret", str(caught.exception))

    def test_steward_is_ensured_with_a_put_and_its_credential_issued(self) -> None:
        self.stub.on("PUT", "/api/supervisor/steward", (200, {"id": "st1", "name": "steward-1"}))
        self.stub.on("POST", "/api/supervisor/steward/credential", (200, {"access_token": "a", "refresh_token": "r", "expires_in": 60}))

        self.assertEqual(SlotRef("st1", "steward-1"), self.api.ensure_steward())
        self.assertEqual("Bearer sup-token", self.stub.requests("PUT", "/api/supervisor/steward")[0].auth)
        self.assertEqual(TokenPair("a", "r", 1060), self.api.issue_steward_credential(now=lambda: 1000.0))
        self.assertEqual("Bearer sup-token", self.stub.requests("POST", "/api/supervisor/steward/credential")[0].auth)

    def test_a_remote_control_host_is_ensured_and_credentialed_by_name(self) -> None:
        path = "/api/supervisor/remote-control/rc%2F1"
        self.stub.on("PUT", path, (200, {"id": "h1", "name": "rc/1"}))
        self.stub.on("POST", path + "/credential", (200, {"access_token": "a", "refresh_token": "r", "expires_in": 60}))
        self.stub.on("POST", path + "/credential/revoke", (204, ""))

        self.assertEqual(SlotRef("h1", "rc/1"), self.api.ensure_remote_control_host("rc/1"))
        self.assertEqual(TokenPair("a", "r", 1060), self.api.issue_remote_control_credential("rc/1", now=lambda: 1000.0))
        self.api.revoke_remote_control_credential("rc/1", TokenPair("a", "r", 1060))

        self.assertEqual("Bearer sup-token", self.stub.requests("POST", path + "/credential")[0].auth)
        self.assertIn("refresh_token", self.stub.requests("POST", path + "/credential/revoke")[0].body)

    def test_an_unrecognisable_remote_control_host_is_refused(self) -> None:
        self.stub.on("PUT", "/api/supervisor/remote-control/rc", (200, {"id": "h1"}))

        with self.assertRaises(ApiError):
            self.api.ensure_remote_control_host("rc")

    def test_an_unrecognisable_steward_is_refused(self) -> None:
        self.stub.on("PUT", "/api/supervisor/steward", (200, {"id": "st1"}))

        with self.assertRaises(ApiError):
            self.api.ensure_steward()

    def test_a_server_that_cannot_be_reached_is_a_message_not_a_trace(self) -> None:
        api = SupervisorApi(Transport("http://127.0.0.1:1"), lambda: "t")

        with self.assertRaises(ApiError) as caught:
            api.find_slot("a")

        self.assertIn("could not reach", str(caught.exception))

    def test_update_script_is_fetched_as_the_slot_not_the_supervisor(self) -> None:
        self.stub.on("GET", "/api/workspace/update_script", (200, "#!/usr/bin/env bash\n"))

        script = self.api.update_script(TokenPair("slot-access", "r", 1))

        self.assertEqual("#!/usr/bin/env bash\n", script)
        self.assertEqual("Bearer slot-access", self.stub.requests("GET", "/api/workspace/update_script")[0].auth)

    def test_update_script_carries_the_repos_the_server_listed(self) -> None:
        self.stub.on("GET", "/api/workspace/update_script", (200, "#!/usr/bin/env bash\n"))
        self.stub.header("GET", "/api/workspace/update_script", "Slop-Haus-Repos", '[{"path":"app","remote":"github.com/acme/app"}]')

        script = self.api.update_script(TokenPair("slot-access", "r", 1))

        self.assertEqual((("app", "github.com/acme/app"),), script.repos)

    def test_update_script_without_the_repo_header_has_no_repo_list(self) -> None:
        self.stub.on("GET", "/api/workspace/update_script", (200, "#!/usr/bin/env bash\n"))

        self.assertIsNone(self.api.update_script(TokenPair("slot-access", "r", 1)).repos)

    def test_an_unreadable_repo_list_is_dropped(self) -> None:
        for value in ("not json", '{"path":"a"}', '[{"path":"a"}]', '[{"path":1,"remote":"r"}]'):
            self.stub.on("GET", "/api/workspace/update_script", (200, "#!/usr/bin/env bash\n"))
            self.stub.header("GET", "/api/workspace/update_script", "Slop-Haus-Repos", value)

            self.assertIsNone(self.api.update_script(TokenPair("slot-access", "r", 1)).repos, value)

    def test_a_redirect_is_not_followed_with_the_token(self) -> None:
        self.stub.on("GET", SLOTS, (302, ""))

        with self.assertRaises(ApiError):
            self.api.find_slot("a")

    def test_plain_http_to_another_host_is_refused(self) -> None:
        with self.assertRaises(ApiError) as caught:
            Transport("http://slop-haus.example")

        self.assertIn("https", str(caught.exception))


MANIFEST = "/api/workspace/manifest/slot"
DESIRED = "/api/supervisor/desired_state"


class WorkspaceReconcileTest(unittest.TestCase):
    def setUp(self) -> None:
        self.stub = StubServer()
        serving_cm = serving(self.stub)
        serving_cm.__enter__()
        self.addCleanup(serving_cm.__exit__, None, None, None)
        self.api = SupervisorApi(Transport(self.stub.url), lambda: "sup-token")
        self.source = ApiManifestSource(self.api, logging.getLogger("test"))

    def test_the_flag_is_on_only_when_the_server_says_true(self) -> None:
        cases: list[tuple[dict[str, object], bool]] = [({"agents": [], "workspace_reconcile": True}, True), ({"agents": []}, False), ({"agents": [], "workspace_reconcile": "yes"}, False)]
        for document, expected in cases:
            self.stub.on("GET", DESIRED, (200, document))
            self.assertIs(expected, self.api.workspace_reconcile(), document)

    def test_the_manifest_is_read_as_the_supervisor(self) -> None:
        self.stub.on("GET", MANIFEST, (200, {"revision": 7, "repos": [{"path": "app", "remote": "github.com/acme/app", "base": "main"}, {"path": "b", "remote": "github.com/acme/b", "base": ""}]}))

        self.assertEqual(
            Manifest("7", (RepoSpec("app", "github.com/acme/app", "main"), RepoSpec("b", "github.com/acme/b", ""))), self.source.fetch()
        )
        self.assertEqual("Bearer sup-token", self.stub.requests("GET", MANIFEST)[0].auth)

    def test_an_empty_manifest_is_a_manifest(self) -> None:
        self.stub.on("GET", MANIFEST, (200, {"revision": 0, "repos": []}))
        self.assertEqual(Manifest("0", ()), self.source.fetch())

    def test_an_answer_that_cannot_be_trusted_is_no_manifest(self) -> None:
        answers: list[tuple[int, dict[str, object]]] = [
            (500, {"detail": "boom"}),
            (200, {}),
            (200, {"revision": "7", "repos": []}),
            (200, {"revision": 7, "repos": {}}),
            # One unreadable entry must not make the repo look removed.
            (200, {"revision": 7, "repos": [{"path": "a", "remote": "github.com/acme/a", "base": ""}, {"path": "b"}]}),
        ]
        for answer in answers:
            self.stub.on("GET", MANIFEST, answer)
            self.assertIsNone(self.source.fetch(), answer)

    def test_a_server_that_cannot_be_reached_is_no_manifest(self) -> None:
        down = ApiManifestSource(SupervisorApi(Transport("http://127.0.0.1:1"), lambda: "t"), logging.getLogger("test"))
        self.assertIsNone(down.fetch())
