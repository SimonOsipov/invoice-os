"""pip_check_guard.offending: the verdicts that decide whether an install step passes."""

import subprocess
import types

import pytest

import pip_check_guard
from pip_check_guard import offending

SWAP = "rapidocr 3.9.2 requires opencv-python, which is not installed.\n"
CLEAN = "No broken requirements found.\n"
IDNA = "requests 2.32.5 requires idna, which is not installed."


def test_guard_accepts_the_opencv_swap_line_alone():
    assert offending(1, SWAP) == []


def test_guard_accepts_a_clean_check():
    assert offending(0, CLEAN) == []


def test_guard_rejects_any_other_finding():
    assert offending(1, SWAP + IDNA + "\n") == [IDNA]


def test_guard_rejects_a_near_miss_swap_line():
    contrib = "rapidocr 3.9.2 requires opencv-contrib-python, which is not installed."
    floor = "rapidocr 3.9.2 has requirement opencv-python>=5, but you have opencv-python 4.0."
    assert offending(1, contrib + "\n") == [contrib]
    assert offending(1, floor + "\n") == [floor]


@pytest.mark.parametrize("rc,out", [(0, ""), (1, ""), (2, SWAP)])
def test_guard_rejects_empty_output_and_unknown_exit_codes(rc, out):
    bad = offending(rc, out)
    assert len(bad) == 1
    assert f"exited {rc}" in bad[0]


def test_guard_rejects_clean_text_with_a_failing_exit():
    assert offending(1, "No broken requirements found.") == ["No broken requirements found."]


def _fake_run(rc, out):
    return lambda *a, **k: types.SimpleNamespace(returncode=rc, stdout=out, stderr="")


def test_main_exits_non_zero_on_an_offending_check(monkeypatch):
    monkeypatch.setattr(subprocess, "run", _fake_run(1, SWAP + IDNA + "\n"))
    with pytest.raises(SystemExit) as exc:
        pip_check_guard.main()
    assert "idna" in str(exc.value)

    monkeypatch.setattr(subprocess, "run", _fake_run(1, SWAP))
    pip_check_guard.main()
