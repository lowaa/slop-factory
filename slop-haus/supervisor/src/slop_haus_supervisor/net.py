from __future__ import annotations

import urllib.request
from typing import Any


class NoRedirects(urllib.request.HTTPRedirectHandler):
    """urllib would carry the Authorization header to wherever a redirect points: the credential going to a stranger."""

    def redirect_request(self, *args: Any, **kwargs: Any) -> None:
        return None


def opener_without_redirects() -> urllib.request.OpenerDirector:
    return urllib.request.build_opener(NoRedirects)
