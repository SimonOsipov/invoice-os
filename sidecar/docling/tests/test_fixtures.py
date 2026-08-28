"""Fixture provenance guards for T-03-6/T-03-7 -- pure file scans, no docling import needed.

native_invoice.pdf is a byte-identical copy of internal/extraction/testdata/native_invoice.pdf
(EXTR-02's own committed corpus, reused rather than duplicated by hand) -- this guards against
silent drift if that fixture is ever regenerated
(`go test ./internal/extraction/ -run TestFixtures_MatchTheirGenerator -update`).
scanned_ocr_fixture.pdf is new here: EXTR-02's own scanned_invoice.pdf is a 4x4 checkerboard
with no real glyphs (see gen_scanned_ocr_fixture.py's docstring), so it can't serve T-03-6.
"""

from pathlib import Path

from conftest import repo_root

TESTDATA = Path(__file__).parent / "testdata"


def test_native_invoice_fixture_matches_the_go_extraction_corpus():
    # repo_root() resolved here, not at module level: this is the only test in this
    # file that needs the wider repo (docling:test carries sidecar/docling/ only), so
    # a missing mount must not also fail the self-contained test below.
    go_extraction_testdata = repo_root() / "internal" / "extraction" / "testdata"
    ours = TESTDATA / "native_invoice.pdf"
    theirs = go_extraction_testdata / "native_invoice.pdf"
    assert theirs.exists(), f"{theirs} is gone -- internal/extraction's own corpus moved"
    assert ours.read_bytes() == theirs.read_bytes(), (
        f"{ours} has drifted from {theirs} -- copy it again after any Go-side regeneration"
    )


def test_scanned_ocr_fixture_is_present_and_non_trivial():
    fixture = TESTDATA / "scanned_ocr_fixture.pdf"
    assert fixture.exists(), f"{fixture} is missing -- regenerate with gen_scanned_ocr_fixture.py"
    size = fixture.stat().st_size
    assert size > 1000, f"{fixture} is only {size} byte(s) -- looks truncated"
