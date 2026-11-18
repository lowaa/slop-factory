from __future__ import annotations

import os
import shutil
import tarfile
import tempfile
import urllib.request
from pathlib import Path

PACKAGE_PATH = "/api/supervisor/package"


class PackageError(Exception):
    pass


def extract(archive: Path, destination: Path) -> None:
    """Unpack a served package; the `data` filter refuses links and paths that leave `destination`."""
    try:
        with tarfile.open(archive, "r:gz") as tar:
            tar.extractall(destination, filter="data")
    except (tarfile.TarError, OSError) as err:
        raise PackageError(f"cannot unpack {archive}: {err}") from err
    if not (destination / "pyproject.toml").is_file():
        raise PackageError(f"{archive} is not a supervisor package (no pyproject.toml)")


def _recover(root: Path) -> None:
    """Put back the app an interrupted install had moved aside, if it never got the new one in."""
    app = root / "app"
    if app.exists():
        return
    for retired in sorted(root.glob(".app-old-*")):
        os.rename(retired, app)
        return


def install(archive: Path, root: Path) -> Path:
    """Unpack into a new directory beside `<root>/app` and rename it into place.

    The new tree is complete before the old one moves, so a bad archive leaves the version that
    was running. The two renames are not one atomic step: a crash between them leaves no `app`,
    and the next install puts the retired one back before starting. A running supervisor has
    its modules loaded already, so swapping under it is safe.
    """
    _recover(root)
    app = root / "app"
    staging = Path(tempfile.mkdtemp(prefix=".app-new-", dir=root))
    retired = root / f".app-old-{staging.name.removeprefix('.app-new-')}"
    try:
        extract(archive, staging)
        staging.chmod(0o755)
        if app.exists():
            os.rename(app, retired)
        try:
            os.rename(staging, app)
        except OSError:
            if retired.exists():
                os.rename(retired, app)
            raise
    except BaseException:
        shutil.rmtree(staging, ignore_errors=True)
        raise
    shutil.rmtree(retired, ignore_errors=True)
    return app


def fetch(server: str, token: str, destination: Path, timeout: float = 60) -> None:
    """Download the server's package, as the agent whose token this is, to `destination`."""
    request = urllib.request.Request(
        server.rstrip("/") + PACKAGE_PATH,
        headers={"Authorization": f"Bearer {token}"},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response, destination.open("wb") as out:
            shutil.copyfileobj(response, out)
    except OSError as err:
        raise PackageError(f"cannot fetch the supervisor package from {server}: {err}") from err
