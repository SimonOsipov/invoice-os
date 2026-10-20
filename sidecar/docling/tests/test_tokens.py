"""Line cells split at a wide word gap, as docling-parse 7.20 did. Needs docling: Docker `test` stage."""

from types import SimpleNamespace

import pytest
from conftest import repo_root
from docling_core.types.doc import BoundingBox, CoordOrigin
from fastapi.testclient import TestClient

import convert
from app import app


def _cell(text, l, r, t=100.0, b=110.0):
    box = BoundingBox(l=l, r=r, t=t, b=b, coord_origin=CoordOrigin.TOPLEFT)
    return SimpleNamespace(text=text, rect=SimpleNamespace(to_bounding_box=lambda: box))


def _page(lines, words):
    return SimpleNamespace(textline_cells=lines, word_cells=words)


@pytest.mark.parametrize(
    "gap, want",
    [(9.0, ["A B"]), (10.0, ["A B"]), (10.1, ["A", "B"]), (12.6, ["A", "B"])],
    ids=["0.9", "1.0", "1.01", "1.26"],
)
def test_a_line_splits_only_past_the_measured_gap(gap, want):
    words = [_cell("A", 0, 10), _cell("B", 10 + gap, 20 + gap)]
    page = _page([_cell("A B", 0, 20 + gap)], words)
    got = [t["text"] for t in convert._page_tokens(page, 600.0, 800.0)]
    assert got == want


def test_a_split_token_takes_the_union_of_its_words():
    words = [_cell("VAT", 0, 20), _cell("No:", 22, 40), _cell("9", 60, 70, t=101.0, b=109.0)]
    page = _page([_cell("VAT No: 9", 0, 70)], words)
    toks = convert._page_tokens(page, 100.0, 1000.0)
    assert [(t["text"], t["x0"], t["x1"]) for t in toks] == [("VAT No:", 0.0, 0.4), ("9", 0.6, 0.7)]
    assert toks[1]["y0"] == 0.101


def test_a_line_takes_only_the_words_on_its_own_line():
    top = [_cell("A", 0, 10), _cell("B", 40, 50)]
    low = [_cell("C", 0, 10, t=200.0, b=210.0), _cell("D", 40, 50, t=200.0, b=210.0)]
    lines = [_cell("A B", 0, 50), _cell("C D", 0, 50, t=200.0, b=210.0)]
    toks = convert._page_tokens(_page(lines, top + low), 600.0, 800.0)
    assert [(t["text"], t["y0"]) for t in toks] == [("A", 0.125), ("B", 0.125), ("C", 0.25), ("D", 0.25)]


def test_an_ocr_line_with_no_words_passes_through():
    page = _page([_cell("TIN: 99999999-1201", 0, 300)], [])
    assert [t["text"] for t in convert._page_tokens(page, 600.0, 800.0)] == ["TIN: 99999999-1201"]


def test_the_bare_tin_label_and_value_reach_the_wire_as_two_tokens():
    pdf = repo_root().joinpath("internal", "extraction", "testdata", "wild_two_party_bare_tin_asprinted.pdf")
    resp = TestClient(app).post("/v1/read", content=pdf.read_bytes(), headers={"content-type": "application/pdf"})
    assert resp.status_code == 200
    texts = [t["text"] for p in resp.json()["pages"] for t in p["tokens"]]
    assert "VAT Reg. No:" in texts and "99999999-0801" in texts, texts
