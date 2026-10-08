"""Shared repo-root resolution for the file-scanning specs (test_pins.py, test_fixtures.py).

The docling:test image (Dockerfile's `test` stage) carries only sidecar/docling/, not
the whole repo, so requirements.txt/Dockerfile/internal/extraction/testdata
cannot be found by walking up from __file__ the way a bare local pytest run can.
REPO_ROOT, when set, points at the repo mounted read-only into the container:
`docker run --rm -v "$PWD:/repo:ro" -e REPO_ROOT=/repo docling:test`. Unset (a bare
local run), this falls back to the real repo root, found by walking up from this file
to the go.mod marker. Either way, a repo that cannot be found raises -- never skips --
so a missing mount reads as a loud failure, not a quiet pass.
"""

import os
from pathlib import Path

import pytest


def repo_root() -> Path:
    env = os.environ.get("REPO_ROOT")
    if env:
        root = Path(env)
        if not root.is_dir():
            raise RuntimeError(
                f"REPO_ROOT={env!r} does not exist -- mount the repo read-only, e.g. "
                '`docker run --rm -v "$PWD:/repo:ro" -e REPO_ROOT=/repo docling:test`'
            )
        return root

    for candidate in Path(__file__).resolve().parents:
        if (candidate / "go.mod").is_file():
            return candidate
    raise RuntimeError(
        f"no go.mod found above {__file__} -- set REPO_ROOT to the repo root, e.g. "
        '`docker run --rm -v "$PWD:/repo:ro" -e REPO_ROOT=/repo docling:test`'
    )


@pytest.fixture
def sentry_capture():
    # sentry_sdk is imported here, not at module level: the file-scan specs import this
    # conftest in a bare venv.
    import sentry_sdk
    from sentry_capture import CapturingTransport

    import convert
    import sentry_setup

    # A boot warm-up must not add a stray event to a row that counts events.
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    options = sentry_setup.sentry_options(
        {
            "SENTRY_DSN": "https://public@o0.ingest.sentry.io/1",
            "RAILWAY_ENVIRONMENT_NAME": "production",
        }
    )
    transport = CapturingTransport()
    sentry_sdk.init(**options, transport=transport)
    try:
        yield transport
    finally:
        sentry_sdk.flush()
        sentry_sdk.get_client().close()
        # close() leaves the client bound; unbind it or the next test inherits it.
        sentry_sdk.get_global_scope().set_client(None)
        # Crumbs live on the scope, not the client; a red row's crumb would taint the next one.
        sentry_sdk.get_isolation_scope().clear_breadcrumbs()
        assert sentry_setup.sentry_state() == "off"
