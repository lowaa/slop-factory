from __future__ import annotations

import glob
import logging
import os
import re
import tempfile
import urllib.error
import urllib.request
import uuid
from typing import IO, Any, Protocol, cast

from slop_haus_supervisor.remote_control.wire.client import Client
from slop_haus_supervisor.text import clean_text

log = logging.getLogger(__name__)

# Built from parts: contracts/paths.json lists every spelled-out .slop-haus path, and this one is the host's own scratch.
STATE_PARTS = (".slop-haus", "conductor", "attachments")
# As the model sees it, from the working copy's root.
RELATIVE_DIR = "/".join(STATE_PARTS)
MAX_NAME_CHARS = 100
CHUNK = 64 * 1024
DOWNLOAD_TIMEOUT_SECONDS = 60.0
_UNSAFE = re.compile(r"[^A-Za-z0-9._-]+")


class TooLarge(Exception):
    pass


class Downloader(Protocol):
    def download(self, path: str, sink: IO[bytes], max_bytes: int) -> None:
        """Streams the body of GET `path` into `sink`; raises TooLarge when it is longer than `max_bytes`, OSError otherwise."""
        ...


class ClientDownloader:
    """Downloads with the conductor's own credential, the way the poller is authenticated."""

    def __init__(self, client: Client, timeout: float = DOWNLOAD_TIMEOUT_SECONDS) -> None:
        self.client = client
        self.timeout = timeout

    def download(self, path: str, sink: IO[bytes], max_bytes: int) -> None:
        token = self.client.tokens.get()
        request = urllib.request.Request(self.client.server + path, headers={"Authorization": "Bearer " + token})
        try:
            with self.client.opener.open(request, timeout=self.timeout) as response:
                declared = response.headers.get("Content-Length")
                if declared is not None and declared.isdigit() and int(declared) > max_bytes:
                    raise TooLarge(f"{declared} bytes advertised, at most {max_bytes} expected")
                copy_capped(response, sink, max_bytes)
        except urllib.error.HTTPError as e:
            raise OSError(f"slop-haus answered {e.code}") from e
        except urllib.error.URLError as e:
            raise OSError(str(e)) from e


def copy_capped(source: IO[bytes], sink: IO[bytes], max_bytes: int) -> None:
    received = 0
    while True:
        chunk = source.read(CHUNK)
        if not chunk:
            return
        received += len(chunk)
        if received > max_bytes:
            raise TooLarge(f"more than the {max_bytes} bytes advertised")
        sink.write(chunk)


def safe_name(filename: str, attachment_id: str) -> str:
    """A name that cannot leave the directory or hide: the basename, a conservative charset, no leading dots, bounded."""
    base = filename.replace("\\", "/").rsplit("/", 1)[-1]
    name = _UNSAFE.sub("_", base).lstrip("._")[:MAX_NAME_CHARS]
    return name or attachment_id


def is_uuid(value: object) -> bool:
    try:
        return isinstance(value, str) and str(uuid.UUID(value)) == value.lower()
    except ValueError:
        return False


class AttachmentStore:
    """A message's attachments, fetched into the working copy so a harness can read them as files.
    What is fetched is only ever written: never executed or interpreted here."""

    def __init__(self, root: str, downloader: Downloader) -> None:
        self.directory = os.path.join(root, *STATE_PARTS)
        self.downloader = downloader

    def lines(self, raw: Any, fetched: list[str]) -> list[str]:
        """One line per attachment for the message text; the ids that landed on disk are added to `fetched`.
        A file that cannot be fetched is said so in its line: the message goes on."""
        out: list[str] = []
        entries: list[Any] = cast("list[Any]", raw) if isinstance(raw, list) else []
        for entry in entries:
            if not isinstance(entry, dict):
                continue
            meta = cast("dict[str, Any]", entry)
            filename = clean_text(str(meta.get("filename") or "file"), 200)
            attachment_id, size = meta.get("id"), meta.get("size_bytes")
            if not is_uuid(attachment_id) or isinstance(size, bool) or not isinstance(size, int) or size < 0:
                out.append(f"[attachment {filename} could not be downloaded]")
                continue
            assert isinstance(attachment_id, str)
            attachment_id = attachment_id.lower()
            name = f"{attachment_id}-{safe_name(str(meta.get('filename') or ''), attachment_id)}"
            try:
                self.fetch(attachment_id, name, size)
            except (OSError, TooLarge) as e:
                log.warning("attachment %s not downloaded: %s", attachment_id, clean_text(str(e), 300))
                out.append(f"[attachment {filename} could not be downloaded]")
                continue
            fetched.append(attachment_id)
            out.append(f"[attached: {filename} -> {RELATIVE_DIR}/{name}]")
        return out

    def fetch(self, attachment_id: str, name: str, size: int) -> None:
        os.makedirs(self.directory, mode=0o700, exist_ok=True)
        os.chmod(self.directory, 0o700)
        fd, tmp = tempfile.mkstemp(dir=self.directory, prefix=".tmp-")  # 0600
        try:
            with os.fdopen(fd, "wb") as sink:
                self.downloader.download(f"/api/agent/attachments/{attachment_id}", sink, size)
            os.replace(tmp, os.path.join(self.directory, name))
        except BaseException:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise

    def remove(self, ids: list[str]) -> None:
        for attachment_id in ids:
            if not is_uuid(attachment_id):
                continue
            for path in glob.glob(os.path.join(glob.escape(self.directory), glob.escape(attachment_id) + "-*")):
                try:
                    os.unlink(path)
                except OSError:
                    pass
