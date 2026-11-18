"""The harness containers are hermetic: they reach the slop-haus server and the
scripted model server, and nothing else. The scenarios would pass on a machine
with internet too, so this is what says the lockdown is really there."""

from __future__ import annotations

import socket
import unittest

from support.env import stack_from_env


def can_connect(host: str, port: int) -> bool:
    try:
        with socket.create_connection((host, port), timeout=3):
            return True
    except OSError:
        return False


class NetworkIsolation(unittest.TestCase):
    def test_the_stack_is_reachable(self) -> None:
        for url in (stack_from_env().server, stack_from_env().model_server):
            host, _, port = url.removeprefix("http://").partition(":")
            self.assertTrue(can_connect(host, int(port)), f"{url} is unreachable")

    def test_the_internet_is_not(self) -> None:
        # Addresses, not names: nothing here depends on DNS being cut off too.
        for host in ("1.1.1.1", "8.8.8.8"):
            self.assertFalse(can_connect(host, 443), f"{host}:443 is reachable from a harness container")

    def test_the_rest_of_the_stack_is_not(self) -> None:
        # Only the frontend (the board's one origin) is on the harness network.
        for host in ("api", "postgres", "mailhog"):
            with self.assertRaises(socket.gaierror, msg=f"{host} resolves from a harness container"):
                socket.getaddrinfo(host, None)


if __name__ == "__main__":
    unittest.main()
