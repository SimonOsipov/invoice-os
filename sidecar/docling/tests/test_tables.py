"""T-04-10..T-04-13: geometry.map_table against hand-constructed TableItem/TableData objects
-- deterministic, unlike TableFormer's verdict on a real PDF (T-04-14 below is the coarse
floor for that). Needs docling importable -- run only via the Docker `test` stage.
"""

from types import SimpleNamespace

import pytest
from conftest import repo_root
from docling_core.types.doc.base import BoundingBox, CoordOrigin
from docling_core.types.doc.document import ProvenanceItem, TableCell, TableData, TableItem
from docling_core.types.doc.labels import DocItemLabel
from fastapi.testclient import TestClient

import geometry
from app import app

PDF_CONTENT_TYPE = "application/pdf"

PAGE_WIDTH = 612.0
PAGE_HEIGHT = 792.0


def _cell(row, col, row_span=1, col_span=1, text="", bbox=None) -> TableCell:
    return TableCell(
        bbox=bbox,
        row_span=row_span,
        col_span=col_span,
        start_row_offset_idx=row,
        end_row_offset_idx=row + row_span,
        start_col_offset_idx=col,
        end_col_offset_idx=col + col_span,
        text=text,
        column_header=(row == 0),
        row_header=False,
        row_section=False,
    )


def _table_item(cells, num_rows, num_cols, table_bbox) -> TableItem:
    prov = ProvenanceItem(page_no=1, bbox=table_bbox, charspan=(0, 0))
    data = TableData(table_cells=cells, num_rows=num_rows, num_cols=num_cols)
    return TableItem(self_ref="#/tables/0", label=DocItemLabel.TABLE, prov=[prov], data=data)


def _pages():
    return {1: SimpleNamespace(width=PAGE_WIDTH, height=PAGE_HEIGHT)}


def _table_bbox_bottomleft():
    # story sec. 3: a table's own prov.bbox is already flipped to BOTTOMLEFT by
    # readingorder_model, while table_cells keep TOPLEFT -- the two-origin trap.
    return BoundingBox(l=50, t=700, r=550, b=500, coord_origin=CoordOrigin.BOTTOMLEFT)


def test_t04_10_3x4_table_maps_rows_cols_and_all_twelve_cells():
    cells = []
    for row in range(3):
        for col in range(4):
            bbox = BoundingBox(
                l=50 + col * 125, t=650 - row * 50, r=50 + (col + 1) * 125, b=600 - row * 50,
                coord_origin=CoordOrigin.TOPLEFT,
            )
            cells.append(_cell(row, col, text=f"r{row}c{col}", bbox=bbox))
    item = _table_item(cells, num_rows=3, num_cols=4, table_bbox=_table_bbox_bottomleft())

    wire = geometry.map_table(item, _pages())

    assert wire["rows"] == 3
    assert wire["cols"] == 4
    assert len(wire["cells"]) == 12
    cell00 = next(c for c in wire["cells"] if c["row"] == 0 and c["col"] == 0)
    assert cell00["text"] == "r0c0"


def test_t04_11_every_cell_box_falls_inside_the_tables_own_prov_box():
    # The arithmetic proof both origins were handled: fails if either branch is dropped
    # (a cell normalised with the wrong origin lands outside the table's own box).
    table_bbox = _table_bbox_bottomleft()
    cells = []
    for row in range(3):
        for col in range(4):
            bbox = BoundingBox(
                l=50 + col * 125, t=650 - row * 50, r=50 + (col + 1) * 125, b=600 - row * 50,
                coord_origin=CoordOrigin.TOPLEFT,
            )
            cells.append(_cell(row, col, text=f"r{row}c{col}", bbox=bbox))
    item = _table_item(cells, num_rows=3, num_cols=4, table_bbox=table_bbox)
    pages = _pages()

    wire = geometry.map_table(item, pages)
    table_box = geometry.normalise_box(table_bbox, PAGE_WIDTH, PAGE_HEIGHT)

    assert len(wire["cells"]) == 12  # floor: nothing below can pass vacuously over zero cells
    for cell in wire["cells"]:
        assert table_box["x0"] <= cell["x0"] <= cell["x1"] <= table_box["x1"], (
            f"cell ({cell['row']},{cell['col']}) x-range falls outside the table's own box"
        )
        assert table_box["y0"] <= cell["y0"] <= cell["y1"] <= table_box["y1"], (
            f"cell ({cell['row']},{cell['col']}) y-range falls outside the table's own box"
        )


def test_t04_12_a_spanning_cell_reports_its_span_and_no_two_cells_share_a_start_cell():
    bbox = BoundingBox(l=50, t=650, r=300, b=600, coord_origin=CoordOrigin.TOPLEFT)
    cells = [
        _cell(0, 0, col_span=2, text="spans two columns", bbox=bbox),
        _cell(0, 2, text="c2", bbox=bbox),
        _cell(0, 3, text="c3", bbox=bbox),
    ]
    item = _table_item(cells, num_rows=1, num_cols=4, table_bbox=_table_bbox_bottomleft())

    wire = geometry.map_table(item, _pages())

    spanning = next(c for c in wire["cells"] if c["col"] == 0)
    assert spanning["col_span"] == 2

    starts = [(c["row"], c["col"]) for c in wire["cells"]]
    assert len(starts) == len(set(starts)), f"duplicate (row,col) starts: {starts}"


def test_t04_13_a_none_bbox_cell_is_emitted_with_no_box_key():
    bbox = BoundingBox(l=50, t=650, r=175, b=600, coord_origin=CoordOrigin.TOPLEFT)
    cells = [
        _cell(0, 0, text="has a box", bbox=bbox),
        _cell(0, 1, text="empty cell", bbox=None),
    ]
    item = _table_item(cells, num_rows=1, num_cols=2, table_bbox=_table_bbox_bottomleft())

    wire = geometry.map_table(item, _pages())

    with_box = next(c for c in wire["cells"] if c["col"] == 0)
    empty = next(c for c in wire["cells"] if c["col"] == 1)
    assert {"x0", "y0", "x1", "y1"} <= with_box.keys()
    assert not ({"x0", "y0", "x1", "y1"} & empty.keys()), (
        f"empty cell carries box keys instead of omitting them: {empty!r}"
    )


@pytest.fixture
def client():
    return TestClient(app)


def test_t04_14_real_table_invoice_pdf_clears_the_coarse_floor(client):
    # TableFormer's exact row/column verdict on a synthetic fixture is not a contract this
    # story can hold (story sec. 4) -- only a floor: at least one table, >=4 cells with
    # both text and a box.
    # Single source of truth: the fixture lives only under internal/extraction/testdata/
    # (fxBuildTable), reached via the repo mount -- no duplicate Python-side copy.
    fixture = repo_root() / "internal" / "extraction" / "testdata" / "table_invoice.pdf"
    raw = fixture.read_bytes()
    resp = client.post("/v1/read", content=raw, headers={"content-type": PDF_CONTENT_TYPE})
    assert resp.status_code == 200

    tables = [t for page in resp.json()["pages"] for t in page.get("tables", [])]
    assert len(tables) >= 1, "table_invoice.pdf carries a ruled table; none was recognised"

    populated = [
        c
        for t in tables
        for c in t["cells"]
        if c.get("text") and {"x0", "y0", "x1", "y1"} <= c.keys()
    ]
    assert len(populated) >= 4, f"only {len(populated)} cell(s) carried text and a box, want >=4"
