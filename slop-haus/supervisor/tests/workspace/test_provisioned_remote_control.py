from __future__ import annotations

import logging
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "slophaus"))

from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.slophaus.credentials import TokenPair
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned
from slop_haus_supervisor.workspace.update_script import UpdateScript

HELPER = "mkdir -p .slop-haus\nprintf '#!/bin/sh\\n' > .slop-haus/token.sh\n"


class FakeEnrollment:
    server = "https://example.test"

    def __init__(self, script: UpdateScript) -> None:
        self._script = script

    def issue(self) -> TokenPair:
        return TokenPair("access", "refresh", 9999999999)

    def update_script(self, tokens: TokenPair) -> str:
        return self._script

    def refresh(self, tokens: TokenPair) -> TokenPair:
        return TokenPair("access-2", "refresh-2", 9999999999)

    def revoke(self, tokens: TokenPair) -> None:
        raise AssertionError("a live credential is kept, not replaced")


def unit(directory: Path, kind: Kind, script: UpdateScript) -> Provisioned:
    return Provisioned(directory, kind, SubprocessRunner(), logging.getLogger("test"), FakeEnrollment(script))


def test_remote_control_workspace_is_not_marked_no_clone(tmp_path: Path) -> None:
    unit(tmp_path / "rc", Kind.REMOTE_CONTROL, UpdateScript(HELPER, ())).ensure()

    assert not (tmp_path / "rc" / ".slop-haus" / "no-clone").exists()


def test_steward_workspace_is_still_marked_no_clone(tmp_path: Path) -> None:
    unit(tmp_path / "st", Kind.STEWARD, UpdateScript(HELPER, ())).ensure()

    assert (tmp_path / "st" / ".slop-haus" / "no-clone").is_file()


def test_a_marker_left_by_an_earlier_workspace_is_removed(tmp_path: Path) -> None:
    directory = tmp_path / "rc"
    (directory / ".slop-haus").mkdir(parents=True)
    (directory / ".slop-haus" / "no-clone").touch()

    unit(directory, Kind.REMOTE_CONTROL, UpdateScript(HELPER, ())).ensure()

    assert not (directory / ".slop-haus" / "no-clone").exists()


def test_remote_control_must_have_the_manifests_repos(tmp_path: Path) -> None:
    script = UpdateScript(HELPER, (("app", "github.com/acme/app"),))

    with pytest.raises(ConfigError, match="github.com/acme/app"):
        unit(tmp_path / "rc", Kind.REMOTE_CONTROL, script).ensure()


def test_a_complete_workspace_loses_its_marker_too(tmp_path: Path) -> None:
    directory = tmp_path / "rc"
    unit(directory, Kind.REMOTE_CONTROL, UpdateScript(HELPER, ())).ensure()
    (directory / ".slop-haus" / "no-clone").touch()

    unit(directory, Kind.REMOTE_CONTROL, UpdateScript(HELPER, ())).ensure()

    assert not (directory / ".slop-haus" / "no-clone").exists()


def test_a_complete_workspace_made_before_cloning_is_installed_again_for_its_repos(tmp_path: Path) -> None:
    directory = tmp_path / "rc"
    unit(directory, Kind.REMOTE_CONTROL, UpdateScript(HELPER, ())).ensure()
    (directory / ".slop-haus" / "no-clone").touch()
    clones = HELPER + "git init -q app && git -C app remote add origin git@github.com:acme/app.git\n"

    unit(directory, Kind.REMOTE_CONTROL, UpdateScript(clones, (("app", "github.com/acme/app"),))).ensure()

    assert (directory / "app" / ".git").is_dir()
    assert not (directory / ".slop-haus" / "clones-pending").exists()
