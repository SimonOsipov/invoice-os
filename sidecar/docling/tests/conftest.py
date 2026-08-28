"""Shared repo-root resolution for the file-scanning specs (test_pins.py, test_fixtures.py).

The docling:test image (Dockerfile's `test` stage) carries only sidecar/docling/, not
the whole repo, so requirements.txt/Dockerfile/docs/**/internal/extraction/testdata
cannot be found by walking up from __file__ the way a bare local pytest run can.
REPO_ROOT, when set, points at the repo mounted read-only into the container:
`docker run --rm -v "$PWD:/repo:ro" -e REPO_ROOT=/repo docling:test`. Unset (a bare
local run), this falls back to the real repo root, found by walking up from this file
to the go.mod marker. Either way, a repo that cannot be found raises -- never skips --
so a missing mount reads as a loud failure, not a quiet pass.
"""

import os
from pathlib import Path


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
