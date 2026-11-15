"""fetch_docling_models: pinned commits, exact-set SHA-256 verification, no retry."""

import hashlib
import re
import sys
import types

import pytest

import fetch_docling_models as fdm


def _sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def test_verify_accepts_matching_files_and_ignores_hf_cache(tmp_path):
    (tmp_path / "a.json").write_bytes(b"ok")
    (tmp_path / ".cache" / "huggingface").mkdir(parents=True)
    (tmp_path / ".cache" / "huggingface" / "x.lock").write_bytes(b"junk")
    fdm.verify(tmp_path, {"a.json": _sha(b"ok")})


def test_verify_rejects_a_hash_mismatch(tmp_path):
    (tmp_path / "a.json").write_bytes(b"tampered")
    want = _sha(b"ok")
    with pytest.raises(SystemExit) as exc:
        fdm.verify(tmp_path, {"a.json": want})
    msg = str(exc.value)
    assert "a.json" in msg and want in msg and _sha(b"tampered") in msg


def test_verify_rejects_a_missing_file(tmp_path):
    (tmp_path / "a.json").write_bytes(b"ok")
    with pytest.raises(SystemExit, match="b.bin"):
        fdm.verify(tmp_path, {"a.json": _sha(b"ok"), "b.bin": _sha(b"b")})


def test_verify_rejects_an_unrecorded_file(tmp_path):
    (tmp_path / "a.json").write_bytes(b"ok")
    (tmp_path / "evil.py").write_bytes(b"import os")
    with pytest.raises(SystemExit, match="evil.py"):
        fdm.verify(tmp_path, {"a.json": _sha(b"ok")})


def test_every_repo_is_pinned_to_a_commit():
    assert len(fdm._REPOS) == 3
    for repo_id, commit, want in fdm._REPOS:
        assert re.fullmatch(r"[0-9a-f]{40}", commit), f"{repo_id}: {commit!r}"
        assert want, f"{repo_id}: empty manifest"
        for rel, digest in want.items():
            assert re.fullmatch(r"[0-9a-f]{64}", digest), f"{repo_id}/{rel}: {digest!r}"


_A = ("org/a", "a" * 40, {"x.json": b"xa", "sub/y.bin": b"ya"})
_B = ("org/b", "b" * 40, {"z.json": b"zb"})


@pytest.fixture
def setup(tmp_path, monkeypatch):
    root = tmp_path / "root"
    root.mkdir()
    monkeypatch.setattr(fdm, "_ROOT", root)
    monkeypatch.setattr(
        fdm,
        "_REPOS",
        [(rid, c, {p: _sha(b) for p, b in files.items()}) for rid, c, files in (_A, _B)],
    )
    return root


def _fake_hub(monkeypatch, *, tamper=None, sibling=None, fail=None):
    """Fake huggingface_hub whose snapshot_download writes each repo's files into local_dir.
    tamper: (repo_id, path) written with wrong bytes into local_dir.
    sibling: write correct files into local_dir plus a tampered copy beside it."""
    calls = []

    def snapshot_download(*, repo_id, revision, local_dir):
        calls.append((repo_id, revision, local_dir))
        if fail:
            raise fail
        files = dict({_A[0]: _A[2], _B[0]: _B[2]}[repo_id])
        if tamper and tamper[0] == repo_id:
            files[tamper[1]] = b"EVIL"
        for rel, data in files.items():
            dest = local_dir / rel
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(data)
        if sibling:
            beside = local_dir.parent / (local_dir.name + "-evil")
            beside.mkdir(exist_ok=True)
            (beside / "x.json").write_bytes(b"EVIL")

    mod = types.ModuleType("huggingface_hub")
    mod.snapshot_download = snapshot_download
    monkeypatch.setitem(sys.modules, "huggingface_hub", mod)
    return calls


def test_main_fetches_each_repo_once_at_its_recorded_commit(setup, monkeypatch):
    calls = _fake_hub(monkeypatch)
    fdm.main()
    assert calls == [
        ("org/a", "a" * 40, setup / "org--a"),
        ("org/b", "b" * 40, setup / "org--b"),
    ]


def test_main_fails_when_a_fetched_file_is_tampered(setup, monkeypatch):
    _fake_hub(monkeypatch, tamper=("org/b", "z.json"))
    with pytest.raises(SystemExit, match="z.json"):
        fdm.main()


def test_main_verifies_the_directory_it_downloaded_to(setup, monkeypatch):
    _fake_hub(monkeypatch, sibling=True)
    fdm.main()  # the sibling copy is never read
    _fake_hub(monkeypatch, tamper=("org/a", "x.json"))
    with pytest.raises(SystemExit):
        fdm.main()


def test_main_propagates_a_download_failure(setup, monkeypatch):
    calls = _fake_hub(monkeypatch, fail=OSError("network"))
    with pytest.raises(OSError, match="network"):
        fdm.main()
    assert calls == [("org/a", "a" * 40, setup / "org--a")]
