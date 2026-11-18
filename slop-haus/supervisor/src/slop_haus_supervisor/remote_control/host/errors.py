from __future__ import annotations


class Undelivered(Exception):
    """A report the server has not confirmed: the item that asked for it stays unacknowledged."""
