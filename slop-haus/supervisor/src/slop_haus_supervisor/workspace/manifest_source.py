from __future__ import annotations

import logging
from typing import cast

from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.workspace.reconcile import Manifest, RepoSpec


class ApiManifestSource:
    """The reconciler's view of the manifest, over the server's slot-readable read."""

    def __init__(self, api: SupervisorApi, logger: logging.Logger) -> None:
        self._api = api
        self._log = logger

    def fetch(self) -> Manifest | None:
        try:
            return self._parse(self._api.workspace_manifest())
        except Exception as err:  # noqa: BLE001 -- no manifest means the workspace is left as it is
            self._log.warning("workspace manifest not read: %s", type(err).__name__)
            return None

    @staticmethod
    def _parse(body: dict[str, object]) -> Manifest | None:
        revision, listed = body.get("revision"), body.get("repos")
        if not isinstance(revision, int) or isinstance(revision, bool) or not isinstance(listed, list):
            return None
        repos: list[RepoSpec] = []
        for entry in cast(list[object], listed):
            fields = cast(dict[str, object], entry) if isinstance(entry, dict) else {}
            path, remote, base = fields.get("path"), fields.get("remote"), fields.get("base", "")
            if not (isinstance(path, str) and isinstance(remote, str) and isinstance(base, str)):
                # Leaving an entry out would read as "removed from the manifest", so one bad entry voids the whole answer.
                return None
            repos.append(RepoSpec(path, remote, base))
        return Manifest(str(revision), tuple(repos))
