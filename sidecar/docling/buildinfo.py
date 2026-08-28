"""Build sha baked into the image at internal/platform/buildsha.txt, copied here as build.txt."""

from pathlib import Path

BUILD_FILE = Path(__file__).parent / "build.txt"


def read_build_sha(path: Path) -> str:
    """Stripped file contents, or "dev" if path does not exist."""
    if not path.exists():
        return "dev"
    return path.read_text().strip()
