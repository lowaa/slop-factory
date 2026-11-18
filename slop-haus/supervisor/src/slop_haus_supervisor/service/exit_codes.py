from __future__ import annotations

from enum import IntEnum


class ExitCode(IntEnum):
    """What `slop-haus-supervisor run` exits with, for a process manager to act on.

    A manager restarts on failure only: STOPPED must not be restarted (someone asked for it), and
    the two refusals below cannot be fixed by trying again. argparse's own usage error is 2.
    """

    STOPPED = 0
    # Anything unexpected; also what an uncaught exception gives.
    FAILED = 1
    # The server asked for a newer supervisor. Restarting is the point, so it is a non-zero code.
    RESTART = 3
    # sysexits EX_TEMPFAIL: another `run` holds this tenant's instance lock.
    ALREADY_RUNNING = 75
    # sysexits EX_CONFIG: a missing root, a bad supervisor.toml, no credential, git cannot push.
    CONFIG = 78
