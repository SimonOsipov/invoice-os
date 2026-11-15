"""Fail the build when `pip check` reports anything but a clean result or the opencv swap.

The locks install with --no-deps, so pip no longer notices a lock that leaves out a
dependency. The swap line is the intended result: rapidocr declares opencv-python and the
lock installs opencv-python-headless instead.
"""

import re
import subprocess
import sys

_CLEAN = "No broken requirements found."
_SWAP = re.compile(r"rapidocr \S+ requires opencv-python, which is not installed\.")


def offending(rc: int, out: str) -> list[str]:
    lines = [line for line in out.splitlines() if line.strip()]
    if rc == 0 and lines == [_CLEAN]:
        return []
    if rc not in (0, 1) or not lines:
        return [f"pip check exited {rc} with {len(lines)} line(s) of output"]
    return [line for line in lines if not (rc == 1 and _SWAP.fullmatch(line))]


def main() -> None:
    res = subprocess.run(
        [sys.executable, "-m", "pip", "check"], capture_output=True, text=True
    )
    bad = offending(res.returncode, res.stdout + res.stderr)
    if bad:
        raise SystemExit("pip check:\n" + "\n".join(bad))


if __name__ == "__main__":
    main()
