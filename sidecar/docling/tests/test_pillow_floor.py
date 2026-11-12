"""The image installs Pillow 13 or later, the line that carries the patched advisories."""

from importlib.metadata import version

import PIL


def test_installed_pillow_is_13_or_later():
    assert int(version("pillow").split(".")[0]) >= 13
    assert int(PIL.__version__.split(".")[0]) >= 13
