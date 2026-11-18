from __future__ import annotations

import os
from unittest import mock

from host_fixtures import HostCase, git, item, start_item, wait_for_file
from slop_haus_supervisor.remote_control.repo.working_copy import MAX_REF_CHARS, MAX_REPO_PATHS, WorkingCopy
from slop_haus_supervisor.remote_control.wire.errors import Refused


class RepoStateTest(HostCase):
    def test_the_state_is_reported_when_the_conversation_is_ready(self) -> None:
        self.start()
        repo = self.repo()
        self.assertEqual(self.first, repo["head"])
        self.assertEqual("main", repo["branch"])
        self.assertEqual(
            {"dirty": False, "ahead": 0, "behind": 0, "commits": [], "pushes": [], "changed_paths": []},
            {k: repo[k] for k in ("dirty", "ahead", "behind", "commits", "pushes", "changed_paths")},
        )
        kinds = [e["kind"] for e in self.reporter.events]
        self.assertLess(kinds.index("ready"), kinds.index("repo_state"))

    def test_the_state_follows_every_turn_after_its_end(self) -> None:
        self.start()
        self.converse(2, "hello")
        kinds = [e["kind"] for e in self.reporter.events]
        self.assertEqual(["ready", "repo_state", "assistant", "assistant", "turn_end", "repo_state", "context"], kinds)
        self.assertEqual(1, self.reporter.of("repo_state")[-1]["turn"])

    def test_a_turn_that_commits_and_pushes_says_so(self) -> None:
        self.start()
        before = git(self.root, "rev-parse", "HEAD")
        self.converse(2, "run: git switch -q -c ticket-9-x && echo n > c.txt && git add c.txt && git commit -q -m 'add c' && git push -q -u origin ticket-9-x")
        repo = self.repo()
        after = git(self.root, "rev-parse", "HEAD")
        self.assertEqual("ticket-9-x", repo["branch"])
        self.assertEqual(after, repo["head"])
        self.assertEqual([{"sha": after, "subject": "add c"}], repo["commits"])
        self.assertEqual([{"ref": "origin/ticket-9-x", "from": "", "to": after}], repo["pushes"])
        self.assertEqual((0, 0, False), (repo["ahead"], repo["behind"], repo["dirty"]))
        self.assertEqual("origin/ticket-9-x", repo["upstream"])
        self.assertEqual(["c.txt"], repo["changed_paths"])
        self.assertNotEqual(before, after)

    def test_a_commit_left_on_a_branch_the_turn_switched_away_from_is_still_reported(self) -> None:
        self.start()
        self.converse(2, "run: git switch -q -c scratch && echo n > c.txt && git add c.txt && git commit -q -m 'on scratch' && git switch -q main")
        repo = self.repo()
        self.assertEqual("main", repo["branch"])
        self.assertEqual(["on scratch"], [c["subject"] for c in repo["commits"]])

    def test_a_second_push_reports_the_move(self) -> None:
        self.start()
        self.converse(2, "run: git switch -q -c t && echo n > c.txt && git add c.txt && git commit -q -m one && git push -q -u origin t")
        first = git(self.root, "rev-parse", "HEAD")
        self.converse(3, "run: echo m > d.txt && git add d.txt && git commit -q -m two && git push -q")
        second = git(self.root, "rev-parse", "HEAD")
        self.assertEqual([{"ref": "origin/t", "from": first, "to": second}], self.repo()["pushes"])
        self.assertEqual(["c.txt", "d.txt"], self.repo()["changed_paths"], "everything since the conversation began")

    def test_a_report_says_which_start_the_session_is_on(self) -> None:
        self.start(5)
        self.assertEqual(5, self.reporter.of("repo_state")[0]["work_seq"])
        self.handle(item(9, "stop"), start_item(10, resume=True))
        self.converse(11, "hello")
        self.assertEqual(10, self.reporter.of("repo_state")[-1]["work_seq"])

    def test_a_turn_cut_short_by_a_crash_still_reports_what_it_did(self) -> None:
        self.start()
        self.message(2, "run: echo n > c.txt && git add c.txt && git commit -q -m partial; crash now")
        self.pump(lambda: self.reporter.of("turn_end") != [])
        repo = self.repo()
        self.assertEqual(["partial"], [c["subject"] for c in repo["commits"]])
        self.assertEqual(["c.txt"], repo["changed_paths"])

    def test_a_tab_or_newline_in_a_subject_or_path_is_not_sent_because_the_server_refuses_it(self) -> None:
        self.start()
        self.write("a\tb\nc.txt", "x")
        self.write("tabbed.txt", "y")
        self.converse(2, "run: git add tabbed.txt && git commit -q -m \"$(printf 'sub\\tject')\"")

        repo = self.repo()
        self.assertEqual(["sub ject"], [c["subject"] for c in repo["commits"]])
        self.assertEqual(["a b c.txt", "tabbed.txt"], repo["changed_paths"])
        texts: list[str] = [c["subject"] for c in repo["commits"]] + repo["changed_paths"] + [repo["branch"], repo["upstream"]]
        for text in texts:
            self.assertFalse(any(ord(ch) < 0x20 or ord(ch) == 0x7F for ch in text), repr(text))

    def test_stopping_in_the_middle_of_a_turn_reports_what_it_had_done(self) -> None:
        self.start()
        self.message(2, "hang: echo half > c.txt")
        wait_for_file(os.path.join(self.logs, "hanging"))
        before = len(self.reporter.of("repo_state"))

        self.handle(item(3, "stop"))

        self.assertEqual(before + 1, len(self.reporter.of("repo_state")))
        self.assertEqual(["c.txt"], self.repo()["changed_paths"], "what the cut-off turn did, which was not there at its start")

    def test_a_fetch_is_not_a_push(self) -> None:
        self.start()
        self.commit_in_origin("z.txt", "other people's work")
        self.converse(2, "run: git fetch -q origin")
        self.assertEqual([], self.repo()["pushes"])
        self.assertEqual(1, self.repo()["behind"], "but it shows we are behind")

    def test_uncommitted_edits_show_as_dirty_and_as_changed_paths(self) -> None:
        self.start()
        self.converse(2, "run: echo edit >> a.txt && echo new > n.txt")
        repo = self.repo()
        self.assertTrue(repo["dirty"])
        self.assertEqual(2, repo["dirty_count"])
        self.assertEqual(["a.txt", "n.txt"], repo["changed_paths"])
        self.assertEqual([], repo["commits"])

    def test_unpushed_commits_show_as_ahead(self) -> None:
        self.start()
        self.converse(2, "run: echo n > c.txt && git add c.txt && git commit -q -m local")
        self.assertEqual(1, self.repo()["ahead"])

    def test_ahead_without_an_upstream_counts_what_no_remote_holds(self) -> None:
        self.start()
        self.converse(2, "run: git switch -q -c solo && echo n > c.txt && git add c.txt && git commit -q -m local")
        repo = self.repo()
        self.assertEqual(("", 1), (repo["upstream"], repo["ahead"]))

    def test_setup_files_are_not_changes(self) -> None:
        self.write(".mcp.json", "{}")
        self.start()
        self.converse(2, "hello")
        self.assertEqual([], self.repo()["changed_paths"])

    def test_a_branch_name_is_bounded_to_what_the_server_accepts(self) -> None:
        self.start()
        # Git caps each path component, so the length comes from several.
        long_name = "/".join(["b" * 100] * 3)
        self.converse(2, "run: git switch -q -c " + long_name)
        self.assertEqual(long_name[:MAX_REF_CHARS], self.repo()["branch"])

    def test_the_paths_are_bounded(self) -> None:
        self.start()
        self.converse(2, "run: for i in $(seq 1 250); do echo x > f$i.txt; done")
        repo = self.repo()
        self.assertEqual(MAX_REPO_PATHS, len(repo["changed_paths"]))
        self.assertEqual(250, repo["changed_total"])

    def test_changes_are_counted_from_where_the_working_copy_was_when_the_conversation_began(self) -> None:
        self.write("early.txt", "before")
        git(self.root, "add", "early.txt")
        git(self.root, "commit", "-q", "-m", "before the conversation")
        self.start()
        self.converse(2, "run: echo n > c.txt")
        self.assertEqual(["c.txt"], self.repo()["changed_paths"], "not what was already committed when it began")

    def test_the_base_survives_a_restart_and_a_reconnect(self) -> None:
        self.start()
        self.converse(2, "run: echo n > c.txt && git add c.txt && git commit -q -m one && git push -q origin main")
        self.handle(item(3, "stop"))
        self.restart_host()
        self.handle(start_item(4, resume=True))

        self.converse(5, "run: echo m > d.txt")

        self.assertEqual(["c.txt", "d.txt"], self.repo()["changed_paths"], "c.txt from before the reconnect, counted from the first start")

    def test_a_git_failure_reporting_the_state_does_not_end_the_conversation(self) -> None:
        self.start()
        before = self.turns()
        with mock.patch.object(WorkingCopy, "repo_state", side_effect=Refused("boom")):
            self.converse(2, "hello")
        self.assertEqual([], self.reporter.of("failed"))
        self.assertEqual(before + 1, self.turns())
