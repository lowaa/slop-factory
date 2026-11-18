from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import unittest
from typing import Any
from unittest import mock

from slop_haus_supervisor.remote_control.repo.working_copy import (
    MAX_REF_CHARS,
    MAX_REPO_PATH_BYTES,
    MAX_REPO_PATHS,
    MAX_SUBJECT_BYTES,
    WorkingCopy,
)
from slop_haus_supervisor.remote_control.wire.errors import Refused


def git(cwd: str, *args: str) -> str:
    out = subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True)
    if out.returncode != 0:
        raise AssertionError(f"git {' '.join(args)}: {out.stderr}")
    return out.stdout.strip()


class RepoCase(unittest.TestCase):
    """A clone of a local origin, read by a WorkingCopy, as a turn would leave it."""

    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp(prefix="remote-control-repo-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        env = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
        patcher = mock.patch.dict(os.environ, env)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.origin = os.path.join(self.tmp, "origin")
        self.root = os.path.join(self.tmp, "workspace")
        os.makedirs(self.origin)
        git(self.origin, "init", "-q", "-b", "main")
        self.first = self.commit_in(self.origin, "a.txt", "one")
        # So a clone can push to main, which origin has checked out.
        git(self.origin, "config", "receive.denyCurrentBranch", "updateInstead")
        subprocess.run(["git", "clone", "-q", self.origin, self.root], check=True)
        os.makedirs(os.path.join(self.root, ".slop-haus"))
        with open(os.path.join(self.root, ".git", "info", "exclude"), "a") as f:
            f.write(".slop-haus/\n")
        self.copy = WorkingCopy(self.root)
        # What the host keeps in the conversation's state between turns.
        self.pushed_paths: set[str] = set()

    def commit_in(self, repo: str, name: str, body: str, message: str | None = None) -> str:
        self.write(name, body, repo)
        git(repo, "add", name)
        git(repo, "commit", "-q", "-m", message or "add " + name)
        return git(repo, "rev-parse", "HEAD")

    def write(self, name: str, body: str, repo: str | None = None) -> None:
        with open(os.path.join(repo or self.root, name), "w") as f:
            f.write(body)

    def state(self, base: str | None = None, before: dict[str, Any] | None = None) -> dict[str, Any]:
        return self.copy.repo_state(self.first if base is None else base, before, self.pushed_paths)

    def turn(self, *commands: str, base: str | None = None) -> dict[str, Any]:
        """The state after shell commands run in the working copy, as a turn
        that began with a snapshot."""
        before = self.copy.snapshot()
        for command in commands:
            subprocess.run(command, shell=True, cwd=self.root, check=True, capture_output=True)
        self.pushed_paths.update(self.copy.pushed_paths(self.first if base is None else base, before))
        return self.state(base, before)


class BaselineTest(RepoCase):
    def test_a_clean_clone_reports_nothing_changed(self) -> None:
        repo = self.state()
        self.assertEqual(self.first, repo["head"])
        self.assertEqual("main", repo["branch"])
        self.assertEqual(
            {"dirty": False, "ahead": 0, "behind": 0, "commits": [], "pushes": [], "changed_paths": []},
            {k: repo[k] for k in ("dirty", "ahead", "behind", "commits", "pushes", "changed_paths")},
        )

    def test_head_base_commit_and_branch(self) -> None:
        self.assertEqual((self.first, self.first, "main"), (self.copy.head(), self.copy.base_commit(), self.copy.branch()))

    def test_a_repo_with_no_commit_has_no_base(self) -> None:
        empty = os.path.join(self.tmp, "empty")
        os.makedirs(empty)
        git(empty, "init", "-q")
        self.assertEqual("", WorkingCopy(empty).base_commit())

    def test_a_git_that_fails_is_refused_with_the_reason(self) -> None:
        with self.assertRaises(Refused):
            WorkingCopy(os.path.join(self.tmp, "nowhere")).head()

    def test_a_program_the_repository_config_names_does_not_run_for_the_hosts_reads(self) -> None:
        sentinel = os.path.join(self.tmp, "fsmonitor-ran")
        git(self.root, "config", "core.fsmonitor", f"touch {sentinel}; echo")
        self.copy.repo_state(self.first, self.copy.snapshot())
        self.assertFalse(os.path.exists(sentinel))
        git(self.root, "status")
        self.assertTrue(os.path.exists(sentinel), "git itself would have run it")

    def test_git_is_not_sent_to_another_repository_by_the_environment(self) -> None:
        with mock.patch.dict(os.environ, {"GIT_DIR": os.path.join(self.origin, "nonexistent")}):
            self.assertEqual(self.first, self.copy.head())


class TurnTest(RepoCase):
    def test_a_turn_that_commits_and_pushes_says_so(self) -> None:
        repo = self.turn(
            "git switch -q -c ticket-9-x && echo n > c.txt && git add c.txt && git commit -q -m 'add c'"
            " && git push -q -u origin ticket-9-x"
        )
        after = git(self.root, "rev-parse", "HEAD")
        self.assertEqual("ticket-9-x", repo["branch"])
        self.assertEqual(after, repo["head"])
        self.assertEqual([{"sha": after, "subject": "add c"}], repo["commits"])
        self.assertEqual([{"ref": "origin/ticket-9-x", "from": "", "to": after}], repo["pushes"])
        self.assertEqual((0, 0, False), (repo["ahead"], repo["behind"], repo["dirty"]))
        self.assertEqual("origin/ticket-9-x", repo["upstream"])
        self.assertEqual(["c.txt"], repo["changed_paths"])

    def test_a_commit_left_on_a_branch_the_turn_switched_away_from_is_still_reported(self) -> None:
        repo = self.turn("git switch -q -c scratch && echo n > c.txt && git add c.txt && git commit -q -m 'on scratch' && git switch -q main")
        self.assertEqual("main", repo["branch"])
        self.assertEqual(["on scratch"], [c["subject"] for c in repo["commits"]])

    def test_a_second_push_reports_the_move(self) -> None:
        self.turn("git switch -q -c t && echo n > c.txt && git add c.txt && git commit -q -m one && git push -q -u origin t")
        first = git(self.root, "rev-parse", "HEAD")
        repo = self.turn("echo m > d.txt && git add d.txt && git commit -q -m two && git push -q")
        second = git(self.root, "rev-parse", "HEAD")
        self.assertEqual([{"ref": "origin/t", "from": first, "to": second}], repo["pushes"])
        self.assertEqual(["c.txt", "d.txt"], repo["changed_paths"], "everything since the conversation began")

    def test_a_fetch_is_not_a_push(self) -> None:
        self.commit_in(self.origin, "z.txt", "other people's work")
        repo = self.turn("git fetch -q origin")
        self.assertEqual([], repo["pushes"])
        self.assertEqual(1, repo["behind"], "but it shows we are behind")

    def test_a_push_followed_by_a_fetch_in_one_turn_is_still_a_push(self) -> None:
        repo = self.turn("git switch -q -c t && echo n > c.txt && git add c.txt && git commit -q -m one && git push -q -u origin t && git fetch -q origin")
        self.assertEqual(["origin/t"], [p["ref"] for p in repo["pushes"]])

    def test_uncommitted_edits_show_as_dirty_and_as_changed_paths(self) -> None:
        repo = self.turn("echo edit >> a.txt && echo new > n.txt")
        self.assertTrue(repo["dirty"])
        self.assertEqual(2, repo["dirty_count"])
        self.assertEqual(["a.txt", "n.txt"], repo["changed_paths"])
        self.assertEqual([], repo["commits"])

    def test_unpushed_commits_show_as_ahead(self) -> None:
        repo = self.turn("echo n > c.txt && git add c.txt && git commit -q -m local")
        self.assertEqual(1, repo["ahead"])

    def test_ahead_without_an_upstream_counts_what_no_remote_holds(self) -> None:
        repo = self.turn("git switch -q -c solo && echo n > c.txt && git add c.txt && git commit -q -m local")
        self.assertEqual(("", 1), (repo["upstream"], repo["ahead"]))

    def test_no_snapshot_means_no_commits_or_pushes(self) -> None:
        self.commit_in(self.root, "c.txt", "x")
        repo = self.state(before=None)
        self.assertEqual(([], []), (repo["commits"], repo["pushes"]))

    def test_changes_are_counted_from_where_the_working_copy_was_when_the_conversation_began(self) -> None:
        base = self.commit_in(self.root, "early.txt", "before", "before the conversation")
        repo = self.turn("echo n > c.txt", base=base)
        self.assertEqual(["c.txt"], repo["changed_paths"], "not what was already committed when it began")

    def test_a_base_that_is_not_a_commit_counts_nothing(self) -> None:
        self.assertEqual([], self.turn("echo n > c.txt", base="")["changed_paths"])
        self.assertEqual([], self.copy.changed_paths("--output=x"))


class OwnWorkTest(RepoCase):
    """Changed paths and commits are the session's own, never what a remote gave it."""

    def push_other_work(self, *names: str) -> None:
        for name in names:
            self.commit_in(self.origin, name, "other people's work")

    def test_a_turn_that_pulls_upstream_commits_reports_no_paths_and_no_commits(self) -> None:
        self.push_other_work("u1.txt", "u2.txt", "u3.txt")
        repo = self.turn("git pull -q")
        self.assertEqual(3, len(self.copy.git("log", "--format=%H", "%s..HEAD" % self.first).split()), "the pull did arrive")
        self.assertEqual(([], []), (repo["changed_paths"], repo["commits"]))

    def test_a_pull_in_an_earlier_turn_is_not_counted_either(self) -> None:
        self.push_other_work("u1.txt")
        self.turn("git pull -q")
        self.assertEqual([], self.turn("true")["changed_paths"])

    def test_a_local_commit_is_reported_and_still_is_after_it_is_pushed(self) -> None:
        local = self.turn("echo n > c.txt && git add c.txt && git commit -q -m local")
        self.assertEqual((["c.txt"], ["local"]), (local["changed_paths"], [c["subject"] for c in local["commits"]]))
        pushed = self.turn("git push -q origin HEAD:main")
        self.assertEqual(["c.txt"], pushed["changed_paths"])
        later = self.turn("echo m > d.txt")
        self.assertEqual(["c.txt", "d.txt"], later["changed_paths"], "the push is still the session's work")

    def test_a_pull_and_a_push_in_one_turn_count_only_the_push(self) -> None:
        self.push_other_work("u1.txt")
        repo = self.turn("git pull -q --rebase && echo n > c.txt && git add c.txt && git commit -q -m mine && git push -q")
        self.assertEqual(["c.txt"], repo["changed_paths"])
        self.assertEqual(["mine"], [c["subject"] for c in repo["commits"]])

    def test_a_dirty_file_is_reported(self) -> None:
        self.push_other_work("u1.txt")
        repo = self.turn("git pull -q && echo edit >> a.txt")
        self.assertEqual(["a.txt"], repo["changed_paths"])

    def test_merging_main_into_a_feature_branch_reports_only_the_branchs_paths(self) -> None:
        self.turn("git switch -q -c feature && echo n > c.txt && git add c.txt && git commit -q -m own && git push -q -u origin feature")
        self.push_other_work("u1.txt", "u2.txt")
        repo = self.turn("git fetch -q origin && git merge -q --no-edit origin/main")
        self.assertTrue(os.path.exists(os.path.join(self.root, "u2.txt")))
        self.assertEqual(["c.txt"], repo["changed_paths"])

    def test_merging_local_main_into_a_branch_with_no_upstream_reports_only_the_branchs_paths(self) -> None:
        self.push_other_work("u1.txt")
        repo = self.turn("git pull -q && git switch -q -c solo && echo n > c.txt && git add c.txt && git commit -q -m own")
        self.assertEqual(("", ["c.txt"]), (repo["upstream"], repo["changed_paths"]))

    def test_an_edit_made_in_a_merge_commit_is_the_sessions_work(self) -> None:
        self.turn("git switch -q -c feature && echo mine > a.txt && git commit -qam own && git push -q -u origin feature")
        self.commit_in(self.origin, "a.txt", "theirs")
        self.push_other_work("u1.txt")
        repo = self.turn("git fetch -q origin && (git merge -q origin/main || true) && echo resolved > a.txt && echo edited > u1.txt && git commit -qam merge")
        self.assertEqual(["a.txt", "u1.txt"], repo["changed_paths"], "the merge's own edit to a file it brought in")

    def test_a_rebase_onto_new_upstream_commits_reports_only_the_branchs_paths(self) -> None:
        self.turn("git switch -q -c feature && echo n > c.txt && git add c.txt && git commit -q -m own")
        self.push_other_work("u1.txt", "u2.txt")
        repo = self.turn("git fetch -q origin && git rebase -q origin/main")
        self.assertEqual(["c.txt"], repo["changed_paths"])

    def test_a_detached_head_reports_its_own_commits_only(self) -> None:
        self.push_other_work("u1.txt")
        repo = self.turn("git fetch -q origin && git checkout -q --detach origin/main && echo n > c.txt && git add c.txt && git commit -q -m own")
        self.assertEqual((["c.txt"], ["own"]), (repo["changed_paths"], [c["subject"] for c in repo["commits"]]))

    def test_a_commit_pre_dating_the_session_is_not_its_work(self) -> None:
        base = self.commit_in(self.root, "early.txt", "before", "before the conversation")
        repo = self.turn("echo n > c.txt && git add c.txt && git commit -q -m own", base=base)
        self.assertEqual(["c.txt"], repo["changed_paths"])


class SetupFilesTest(RepoCase):
    def test_the_mcp_config_is_not_a_change(self) -> None:
        self.write(".mcp.json", "{}")
        repo = self.state()
        self.assertEqual(([], False), (repo["changed_paths"], repo["dirty"]))

    def test_a_created_claude_md_holding_only_the_import_is_not_a_change(self) -> None:
        self.write("CLAUDE.md", "# Agent instructions\n\n@.slop-haus/briefing.md\n")
        repo = self.state()
        self.assertEqual(([], False), (repo["changed_paths"], repo["dirty"]))

    def test_a_created_claude_md_with_other_text_is_a_change(self) -> None:
        self.write("CLAUDE.md", "# Agent instructions\nbe nice\n")
        repo = self.state()
        self.assertEqual((["CLAUDE.md"], True), (repo["changed_paths"], repo["dirty"]))

    def test_an_import_line_added_to_a_tracked_claude_md_is_not_a_change(self) -> None:
        self.commit_in(self.root, "CLAUDE.md", "rules\n")
        base = git(self.root, "rev-parse", "HEAD")
        self.write("CLAUDE.md", "rules\n@.slop-haus/briefing.md\n")
        repo = self.state(base)
        self.assertEqual(([], False), (repo["changed_paths"], repo["dirty"]))

    def test_other_edits_to_a_tracked_claude_md_are_a_change(self) -> None:
        self.commit_in(self.root, "CLAUDE.md", "rules\n")
        base = git(self.root, "rev-parse", "HEAD")
        self.write("CLAUDE.md", "rules\n@.slop-haus/briefing.md\nmore\n")
        repo = self.state(base)
        self.assertEqual((["CLAUDE.md"], True), (repo["changed_paths"], repo["dirty"]))


    def test_a_line_removed_from_a_tracked_claude_md_is_a_change(self) -> None:
        self.commit_in(self.root, "CLAUDE.md", "rules\nmore rules\n")
        base = git(self.root, "rev-parse", "HEAD")
        self.write("CLAUDE.md", "rules\n@.slop-haus/briefing.md\n")
        repo = self.state(base)
        self.assertEqual((["CLAUDE.md"], True), (repo["changed_paths"], repo["dirty"]))

    def test_a_committed_claude_md_edit_is_a_change_even_with_the_import_added_after(self) -> None:
        self.commit_in(self.root, "CLAUDE.md", "rules\n")
        base = git(self.root, "rev-parse", "HEAD")
        self.commit_in(self.root, "CLAUDE.md", "rules\nmore rules\n")
        self.write("CLAUDE.md", "rules\nmore rules\n@.slop-haus/briefing.md\n")
        repo = self.state(base)
        self.assertEqual(["CLAUDE.md"], repo["changed_paths"])


class BoundsTest(RepoCase):
    def test_multibyte_subjects_and_paths_are_cut_to_the_servers_byte_limits(self) -> None:
        name = "é" * 120 + ".txt"
        self.write(name, "x")
        repo = self.turn("git add . && git commit -q -m \"$(printf 'é%.0s' $(seq 1 150))\"")
        (commit,) = repo["commits"]
        self.assertEqual("é" * (MAX_SUBJECT_BYTES // 2), commit["subject"])
        self.assertEqual(["é" * (MAX_REPO_PATH_BYTES // 2)], repo["changed_paths"])

    def test_a_tab_or_newline_in_a_subject_or_path_is_not_sent_because_the_server_refuses_it(self) -> None:
        self.write("a\tb\nc.txt", "x")
        self.write("tabbed.txt", "y")
        repo = self.turn("git add tabbed.txt && git commit -q -m \"$(printf 'sub\\tject')\"")
        self.assertEqual(["sub ject"], [c["subject"] for c in repo["commits"]])
        self.assertEqual(["a b c.txt", "tabbed.txt"], repo["changed_paths"])
        for text in [c["subject"] for c in repo["commits"]] + repo["changed_paths"] + [repo["branch"], repo["upstream"]]:
            self.assertFalse(any(ord(ch) < 0x20 or ord(ch) == 0x7F for ch in text), repr(text))

    def test_a_branch_name_is_bounded_to_what_the_server_accepts(self) -> None:
        # Git caps each path component, so the length comes from several.
        long_name = "/".join(["b" * 100] * 3)
        repo = self.turn("git switch -q -c " + long_name)
        self.assertEqual(long_name[:MAX_REF_CHARS], repo["branch"])

    def test_the_paths_are_bounded(self) -> None:
        repo = self.turn("for i in $(seq 1 250); do echo x > f$i.txt; done")
        self.assertEqual(MAX_REPO_PATHS, len(repo["changed_paths"]))
        self.assertEqual(250, repo["changed_total"])


if __name__ == "__main__":
    unittest.main()
