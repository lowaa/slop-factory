from __future__ import annotations

import logging
import logging.handlers
import os
import sys
from collections.abc import Generator
from contextlib import contextmanager
from pathlib import Path
from io import TextIOWrapper

from slop_haus_supervisor.harness.text import SecretMasker

FORMAT = "%(asctime)s %(levelname)s %(name)s: %(message)s"
DEFAULT_LOG_BYTES = 10 * 1024 * 1024
LOG_BACKUPS = 5


class MaskingFilter(logging.Filter):
    """Known secret values never reach a handler, whatever a log call interpolated into the line."""

    def __init__(self, masker: SecretMasker) -> None:
        super().__init__()
        self._masker = masker
        self._formatter = logging.Formatter()

    def filter(self, record: logging.LogRecord) -> bool:
        record.msg = self._masker.mask(record.getMessage())
        record.args = None
        if record.exc_info:
            record.exc_text = self._masker.mask(self._formatter.formatException(record.exc_info))
            record.exc_info = None
        elif record.exc_text:
            record.exc_text = self._masker.mask(record.exc_text)
        return True


class PrivateRotatingFileHandler(logging.handlers.RotatingFileHandler):
    """The log can hold ticket text and paths: every file, including the one made at a rollover, is 0600."""

    def _open(self) -> TextIOWrapper:
        fd = os.open(self.baseFilename, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        return open(fd, "a", encoding=self.encoding)


def _file_handler(path: Path, max_bytes: int) -> logging.Handler:
    return PrivateRotatingFileHandler(path, maxBytes=max_bytes, backupCount=LOG_BACKUPS, encoding="utf-8")


@contextmanager
def configured(masker: SecretMasker, log_file: Path | None = None, max_bytes: int = DEFAULT_LOG_BYTES) -> Generator[None]:
    """Lines to stdout and stderr always, and to a size-rotated file when asked; handlers are removed on exit."""
    root = logging.getLogger()
    # Errors on stderr and the rest on stdout, so a launcher that keeps them apart shows what went wrong.
    errors = logging.StreamHandler(sys.stderr)
    errors.setLevel(logging.ERROR)
    info = logging.StreamHandler(sys.stdout)
    info.addFilter(lambda record: record.levelno < logging.ERROR)
    handlers: list[logging.Handler] = [info, errors]
    if log_file is not None:
        handlers.append(_file_handler(log_file, max_bytes))
    previous_level = root.level
    for handler in handlers:
        handler.setFormatter(logging.Formatter(FORMAT))
        handler.addFilter(MaskingFilter(masker))
        root.addHandler(handler)
    root.setLevel(logging.INFO)
    try:
        yield
    finally:
        root.setLevel(previous_level)
        for handler in handlers:
            root.removeHandler(handler)
            handler.close()
