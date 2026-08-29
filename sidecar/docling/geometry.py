"""bbox -> normalised [0,1] top-left; the only arithmetic in the service (EXTR-03-04).

Duck-typed on docling_core's BoundingBox/ProvenanceItem shape (l, t, r, b, coord_origin;
page_no, bbox, charspan) so the box-arithmetic functions stay importable, and unit-testable,
without docling installed. map_table takes real TableItem/TableData objects and therefore
needs docling importable, same as the rest of convert.py.

Two known origins, never assumed (story sec. 3):
  BOTTOMLEFT -- element provenance (readingorder_model.py's ~10 to_bottom_left_origin sites).
  TOPLEFT    -- a table's own cells (table_cells is passed into TableData unconverted).
A box carrying anything else is an error, never a guess.
"""

import logging

logger = logging.getLogger(__name__)

BOTTOMLEFT = "BOTTOMLEFT"
TOPLEFT = "TOPLEFT"


class UnknownCoordOrigin(ValueError):
    """A box's coord_origin is neither BOTTOMLEFT nor TOPLEFT -- never guessed (T-04-3)."""


def _origin_name(coord_origin) -> str:
    """Accepts a plain string or an enum member (docling_core.CoordOrigin) alike."""
    return getattr(coord_origin, "value", coord_origin)


def _clamp_pair(lo: float, hi: float, limit: float) -> tuple[float, float, bool]:
    """Clamp each bound independently to [0, limit] -- never reorders lo/hi against each
    other (only against the page edge), so an origin-correct but inverted input (T-04-2's
    deliberately unrealistic box) is not silently "fixed" into a different answer.
    """
    c_lo = max(0.0, min(limit, lo))
    c_hi = max(0.0, min(limit, hi))
    return c_lo, c_hi, (c_lo != lo or c_hi != hi)


def normalise_box(box, page_width: float, page_height: float) -> dict[str, float] | None:
    """box.l/.t/.r/.b in PDF points, box.coord_origin BOTTOMLEFT or TOPLEFT.

    Returns {"x0","y0","x1","y1"} in [0,1], top-left origin, x0<=x1, y0<=y1, clamped to the
    page and logged when clamped (T-04-6). Returns None -- page skipped, logged -- when
    page_height is 0 (T-04-7). Raises UnknownCoordOrigin for anything else (T-04-3).

    The flip for BOTTOMLEFT is `page_height - t` / `page_height - b`, matching
    BoundingBox.to_top_left_origin -- asserted in tests against a hand-computed value, never
    by round-trip (T-04-1/2). TOPLEFT is already top-left: t/b pass through unconverted,
    matching to_top_left_origin's no-op for a box already in that origin.
    """
    if page_width <= 0 or page_height <= 0:
        logger.warning(
            "normalise_box: skipping a box -- zero-size page (width=%s height=%s)",
            page_width,
            page_height,
        )
        return None

    origin = _origin_name(box.coord_origin)
    if origin == BOTTOMLEFT:
        y0_raw = page_height - box.t
        y1_raw = page_height - box.b
    elif origin == TOPLEFT:
        y0_raw = box.t
        y1_raw = box.b
    else:
        raise UnknownCoordOrigin(f"unrecognised coord_origin: {box.coord_origin!r}")

    # l/r don't carry a coord_origin (x is unaffected by the vertical flip) but can still
    # arrive reversed (T-04-5) -- reordered on value, unlike y0/y1 above.
    x0_raw, x1_raw = min(box.l, box.r), max(box.l, box.r)

    x0, x1, x_clamped = _clamp_pair(x0_raw, x1_raw, page_width)
    y0, y1, y_clamped = _clamp_pair(y0_raw, y1_raw, page_height)
    if x_clamped or y_clamped:
        logger.warning("normalise_box: clamped an out-of-page box to the page bounds: %r", box)

    return {
        "x0": x0 / page_width,
        "y0": y0 / page_height,
        "x1": x1 / page_width,
        "y1": y1 / page_height,
    }


def normalise_prov(prov: list, pages: dict) -> list[dict]:
    """One normalised box per entry in `prov` -- a text item's prov list can carry several
    boxes (across columns or pages); each becomes its own token, never dropped or merged
    into a spanning box (T-04-8).

    `prov` is a list of objects with .page_no and .bbox (normalise_box()-shaped).
    `pages` maps page_no -> an object with .width/.height (DoclingDocument.pages[n].size).
    """
    tokens = []
    for entry in prov:
        page = pages.get(entry.page_no)
        if page is None:
            logger.warning("normalise_prov: prov entry references unknown page %s", entry.page_no)
            continue
        box = normalise_box(entry.bbox, page.width, page.height)
        if box is not None:
            tokens.append(box)
    return tokens


def map_table(table_item, pages: dict) -> dict:
    """TableItem -> the wire table: {"rows", "cols", "cells": [...]}.

    Each cell carries row/col/row_span/col_span/text, plus x0/y0/x1/y1 only when
    table_cells[i].bbox is not None (T-04-13 -- an empty cell gets no box key, not a zero
    box). table_item.prov[0].bbox and each cell's own bbox are normalised by their OWN
    coord_origin (T-04-10..T-04-12): the table's prov is already flipped to BOTTOMLEFT by
    readingorder_model, its cells keep the table-structure model's own TOPLEFT.

    The table's own page is looked up lazily, only when a cell actually carries a box --
    on the DOCX path table_item.prov is [] (T-04-15/16) and every cell.bbox is None, so
    this never touches `pages` at all.
    """
    cells = []
    page = None
    for cell in table_item.data.table_cells:
        entry = {
            "row": cell.start_row_offset_idx,
            "col": cell.start_col_offset_idx,
            "row_span": cell.row_span,
            "col_span": cell.col_span,
            "text": cell.text,
        }
        if cell.bbox is not None:
            if page is None:
                page = pages[table_item.prov[0].page_no]
            box = normalise_box(cell.bbox, page.width, page.height)
            if box is not None:
                entry.update(box)
        cells.append(entry)
    return {
        "rows": table_item.data.num_rows,
        "cols": table_item.data.num_cols,
        "cells": cells,
    }
