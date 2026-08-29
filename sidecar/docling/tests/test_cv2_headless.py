"""Guards the Dockerfile's opencv swap (deps stage installs -headless over rapidocr's
transitive GUI build; the test stage's requirements-dev.txt re-resolve undoes it and
must redo the swap). A regression here is a convincing false green: dist-info can be
present while cv2/ itself is missing (confirmed empirically pre-fix) -- import AND
distribution-name checks are both required.

Needs docling importable -- run only via the Docker `test` stage.
"""

from importlib import metadata


def test_cv2_imports_and_only_the_headless_distribution_is_present():
    import cv2

    assert cv2.__version__, "cv2 imported but reports no version"

    names = {d.metadata["Name"].lower() for d in metadata.distributions() if d.metadata.get("Name")}

    # Every opencv variant owns the same `cv2` namespace, so naming only `opencv-python` lets
    # `opencv-contrib-python` (GUI, and equally a co-installation) through. Pin the whole set.
    opencv = {n for n in names if n.startswith("opencv")}
    assert opencv == {"opencv-python-headless"}, (
        f"opencv distributions are {sorted(opencv) or 'none'}, want exactly "
        "['opencv-python-headless'] -- every variant shares the cv2 namespace, so any other "
        "one present means the deps-stage swap to -headless was undone or shadowed"
    )
