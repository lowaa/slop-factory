from __future__ import annotations

import unittest

from slop_haus_supervisor.net import NoRedirects, opener_without_redirects


class NoRedirectsTest(unittest.TestCase):
    def test_a_redirect_is_not_followed(self) -> None:
        self.assertIsNone(NoRedirects().redirect_request(None, None, 302, "Found", {}, "http://elsewhere/"))

    def test_the_opener_carries_the_handler(self) -> None:
        self.assertTrue(any(isinstance(h, NoRedirects) for h in getattr(opener_without_redirects(), "handlers")))


if __name__ == "__main__":
    unittest.main()
