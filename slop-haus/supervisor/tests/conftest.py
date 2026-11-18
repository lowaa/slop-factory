"""Shared by every test: a git identity for the throwaway repos tests commit in.

Many tests commit through the environment they inherit, so without this they
pass only where the machine has a global git identity (a developer's) and fail
on a fresh CI runner. A test that sets its own identity still wins.
"""

import os

for _name, _value in {
    "GIT_AUTHOR_NAME": "test",
    "GIT_AUTHOR_EMAIL": "test@example.com",
    "GIT_COMMITTER_NAME": "test",
    "GIT_COMMITTER_EMAIL": "test@example.com",
}.items():
    os.environ.setdefault(_name, _value)
