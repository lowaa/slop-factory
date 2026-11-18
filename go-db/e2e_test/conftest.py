import os
import time

import pytest
from playwright.sync_api import APIRequestContext, sync_playwright

# demo-api has no HTTP UI at all, so these tests use Playwright's API
# testing mode (playwright.request) rather than a browser page -- it's a
# real, documented Playwright capability for exercising an HTTP API
# end-to-end, and doesn't launch or need a browser.
BASE_URL = os.environ.get("DEMO_API_BASE_URL", "http://localhost:8080")


@pytest.fixture(scope="session")
def api() -> APIRequestContext:
    with sync_playwright() as p:
        request_context = p.request.new_context(base_url=BASE_URL)
        _wait_until_healthy(request_context)
        yield request_context
        request_context.dispose()


def _wait_until_healthy(request_context: APIRequestContext, timeout_seconds: float = 30) -> None:
    deadline = time.monotonic() + timeout_seconds
    last_error = None
    while time.monotonic() < deadline:
        try:
            resp = request_context.get("/healthz")
            if resp.ok:
                return
            last_error = f"status {resp.status}"
        except Exception as exc:  # noqa: BLE001 - retry on any connection error
            last_error = str(exc)
        time.sleep(0.5)
    pytest.fail(f"demo-api at {BASE_URL} never became healthy: {last_error}")
