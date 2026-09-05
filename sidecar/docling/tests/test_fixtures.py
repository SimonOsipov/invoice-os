"""Fixture provenance guards for T-03-6/T-03-7/T-04-9/T-04-18 -- pure file scans plus one
docx regeneration compare (build_docx.py imports `docx`, already installed for
msword_backend.py -- see its own docstring), no DocumentConverter needed.

native_invoice.pdf and native_3page.pdf are byte-identical copies of
internal/extraction/testdata/*.pdf (EXTR-02's own committed corpus, reused rather than
duplicated by hand) -- this guards against silent drift if either fixture is ever
regenerated (`go test ./internal/extraction/ -run TestFixtures_MatchTheirGenerator -update`).
scanned_ocr_fixture.pdf is new here: EXTR-02's own scanned_invoice.pdf is a 4x4 checkerboard
with no real glyphs (see gen_scanned_ocr_fixture.py's docstring), so it can't serve T-03-6.

table_invoice.pdf has no local copy: it lives only under internal/extraction/testdata/
(fxBuildTable), reached through repo_root() by the tests that need it (test_tables.py,
test_convert.py's T-04-17) -- Go's own TestFixtures_MatchTheirGenerator already guards its
bytes, so a second guard here would just be duplication.
"""

import importlib
import sys
from pathlib import Path

from conftest import repo_root

TESTDATA = Path(__file__).parent / "testdata"
FIXTURES = Path(__file__).parent / "fixtures"

sys.path.insert(0, str(FIXTURES))
import build_docx
import build_invoice_docx


def test_native_invoice_fixture_matches_the_go_extraction_corpus():
    # repo_root() resolved here, not at module level: this is the only test in this
    # file that needs the wider repo (docling:test carries sidecar/docling/ only), so
    # a missing mount must not also fail the self-contained tests below.
    go_extraction_testdata = repo_root() / "internal" / "extraction" / "testdata"
    ours = TESTDATA / "native_invoice.pdf"
    theirs = go_extraction_testdata / "native_invoice.pdf"
    assert theirs.exists(), f"{theirs} is gone -- internal/extraction's own corpus moved"
    assert ours.read_bytes() == theirs.read_bytes(), (
        f"{ours} has drifted from {theirs} -- copy it again after any Go-side regeneration"
    )


def test_native_3page_fixture_matches_the_go_extraction_corpus():
    # T-04-9 needs a local copy the same way T-03-6/7 already do for native_invoice.pdf.
    go_extraction_testdata = repo_root() / "internal" / "extraction" / "testdata"
    ours = TESTDATA / "native_3page.pdf"
    theirs = go_extraction_testdata / "native_3page.pdf"
    assert theirs.exists(), f"{theirs} is gone -- internal/extraction's own corpus moved"
    assert ours.read_bytes() == theirs.read_bytes(), (
        f"{ours} has drifted from {theirs} -- copy it again after any Go-side regeneration"
    )


def test_scanned_ocr_fixture_is_present_and_non_trivial():
    fixture = TESTDATA / "scanned_ocr_fixture.pdf"
    assert fixture.exists(), f"{fixture} is missing -- regenerate with gen_scanned_ocr_fixture.py"
    size = fixture.stat().st_size
    assert size > 1000, f"{fixture} is only {size} byte(s) -- looks truncated"


def test_t04_18_table_invoice_docx_matches_its_generator():
    # Mirrors fixtures_test.go's own TestFixtures_MatchTheirGenerator idiom on the Python
    # side: regenerate from build_docx.build() and byte-compare against the committed file.
    committed = build_docx.FIXTURE.read_bytes()
    regenerated = build_docx.build()
    assert committed == regenerated, (
        f"{build_docx.FIXTURE} does not match its generator -- "
        "run `python tests/fixtures/build_docx.py --update`"
    )


def test_invoice_docx_matches_its_generator():
    # EXTR-18-04: same idiom as above. This is the ONLY byte-fidelity guard for invoice.docx --
    # the Go-side TestFixtures_MatchTheirGenerator counts committed .pdf files as a floor, not
    # an equality, so a green Go run proves nothing about this DOCX's bytes.
    committed = build_invoice_docx.FIXTURE.read_bytes()
    regenerated = build_invoice_docx.build()
    assert committed == regenerated, (
        f"{build_invoice_docx.FIXTURE} does not match its generator -- "
        "run `python tests/fixtures/build_invoice_docx.py --update`"
    )


# --- EXTR-19-01: the discriminating pair and its control ---------------------------------
#
# Written Mode A: red until EXTR-19-01 commits the two generators and their .docx files.


def _assert_docx_matches_its_generator(module: str) -> None:
    # Imported here rather than at module level: the generator does not exist yet, and a
    # module-level import would error collection for every test in this file, not just this one.
    path = FIXTURES / f"{module}.py"
    assert path.exists(), f"{path} is missing -- EXTR-19-01 has not written this generator yet"

    build = importlib.import_module(module)
    assert build.FIXTURE.exists(), (
        f"{build.FIXTURE} is not committed -- run `python tests/fixtures/{module}.py --update`"
    )
    committed = build.FIXTURE.read_bytes()
    regenerated = build.build()
    assert committed == regenerated, (
        f"{build.FIXTURE} does not match its generator -- "
        f"run `python tests/fixtures/{module}.py --update`"
    )


def test_stacked_invoice_docx_matches_its_generator():
    # Fixture B. Same idiom as test_invoice_docx_matches_its_generator above: this is the ONLY
    # byte-fidelity guard for boxless_stacked_invoice.docx.
    _assert_docx_matches_its_generator("build_stacked_invoice_docx")


def test_inline_variant_docx_matches_its_generator():
    # Fixture A-prime, the near-miss control.
    _assert_docx_matches_its_generator("build_inline_variant_docx")
