"""T-04-1..T-04-8: geometry.py's box arithmetic -- both coord_origin branches, clamping,
zero-height-page skip, and multi-prov handling. Pure duck-typed dataclasses below (no
docling import): geometry.py is unit-tested independently of Docling (story sec. 4).
"""

from dataclasses import dataclass, field

import pytest

import geometry

# T-04-1's own worked example: an A4-height page and one BOTTOMLEFT box.
PAGE_HEIGHT = 841.92
PAGE_WIDTH = 595.32


@dataclass
class Box:
    l: float
    t: float
    r: float
    b: float
    coord_origin: str


@dataclass
class Prov:
    page_no: int
    bbox: Box
    charspan: tuple = field(default=(0, 0))


@dataclass
class PageSize:
    width: float
    height: float


def test_t04_1_bottomleft_flip_is_hand_computed():
    # b=687.87 -- 841.92-687.87 -- must NOT be dropped: the flip needs both t and b.
    box = Box(l=100, t=769.77, r=200, b=687.87, coord_origin=geometry.BOTTOMLEFT)
    result = geometry.normalise_box(box, PAGE_WIDTH, PAGE_HEIGHT)
    assert result["y0"] == pytest.approx((841.92 - 769.77) / 841.92)
    assert result["y1"] == pytest.approx((841.92 - 687.87) / 841.92)


def test_t04_2_topleft_same_numbers_gives_a_different_answer():
    # Same t/b as T-04-1, TOPLEFT this time -- the branch must change the arithmetic, not
    # just the label. y0 == t/height directly: no flip in this branch.
    box = Box(l=100, t=769.77, r=200, b=687.87, coord_origin=geometry.TOPLEFT)
    result = geometry.normalise_box(box, PAGE_WIDTH, PAGE_HEIGHT)
    assert result["y0"] == pytest.approx(769.77 / 841.92)
    assert result["y0"] != pytest.approx((841.92 - 769.77) / 841.92), (
        "TOPLEFT and BOTTOMLEFT produced the same y0 for the same raw numbers -- the "
        "coord_origin branch is not actually being taken"
    )


def test_t04_3_unknown_coord_origin_raises():
    box = Box(l=0, t=100, r=50, b=0, coord_origin="MIDDLE_OUT")
    with pytest.raises(geometry.UnknownCoordOrigin):
        geometry.normalise_box(box, PAGE_WIDTH, PAGE_HEIGHT)


def test_t04_4_bottomleft_box_at_exact_page_bottom():
    box = Box(l=0, t=100, r=50, b=0, coord_origin=geometry.BOTTOMLEFT)
    result = geometry.normalise_box(box, PAGE_WIDTH, PAGE_HEIGHT)
    assert result["y1"] == 1.0


def test_t04_5_left_greater_than_right_still_satisfies_x0_le_x1():
    box = Box(l=200, t=100, r=50, b=0, coord_origin=geometry.TOPLEFT)
    result = geometry.normalise_box(box, PAGE_WIDTH, PAGE_HEIGHT)
    assert result["x0"] <= result["x1"]


def test_t04_6_box_past_page_edge_is_clamped_and_logged(caplog):
    box = Box(l=0, t=100, r=PAGE_WIDTH + 500, b=0, coord_origin=geometry.TOPLEFT)
    with caplog.at_level("WARNING"):
        result = geometry.normalise_box(box, PAGE_WIDTH, PAGE_HEIGHT)
    assert result["x0"] >= 0.0 and result["x1"] <= 1.0
    assert result["x1"] == 1.0
    assert "clamp" in caplog.text.lower()


def test_t04_7_zero_height_page_is_skipped_not_a_zerodivisionerror(caplog):
    box = Box(l=0, t=100, r=50, b=0, coord_origin=geometry.TOPLEFT)
    with caplog.at_level("WARNING"):
        result = geometry.normalise_box(box, PAGE_WIDTH, 0)
    assert result is None
    assert caplog.text, "a zero-height page must log why it was skipped"


def test_t04_8_multi_prov_text_item_emits_one_token_per_entry():
    prov = [
        Prov(page_no=1, bbox=Box(l=0, t=100, r=50, b=80, coord_origin=geometry.TOPLEFT)),
        Prov(page_no=2, bbox=Box(l=10, t=200, r=60, b=180, coord_origin=geometry.TOPLEFT)),
    ]
    pages = {1: PageSize(600, 800), 2: PageSize(600, 800)}
    result = geometry.normalise_prov(prov, pages)
    assert len(result) == 2, "neither prov entry may be dropped nor merged into one box"
    assert result[0] != result[1]
