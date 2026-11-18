# slop-haus supervisor

Tests and type check: `./test.sh` (pyright strict, then pytest).

## Smoke tests against the real `claude`

`tests/harness/test_claude_smoke.py` runs the installed `claude` binary with no login, so nothing is spent. The result depends on the host's `claude` version, so the default run deselects them (marker `smoke`). Run them with:

    uv run pytest -m smoke

They are skipped if `claude` is not on `PATH`.
