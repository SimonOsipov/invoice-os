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


def normalise_box(box, page_width: float, page_height: float) -> dict[str, float] | None:
    """box.l/.t/.r/.b in PDF points, box.coord_origin BOTTOMLEFT or TOPLEFT.

    Returns {"x0","y0","x1","y1"} in [0,1], top-left origin, x0<=x1, y0<=y1, clamped to the
    page and logged when clamped (T-04-6). Returns None -- page skipped, logged -- when
    page_height is 0 (T-04-7). Raises UnknownCoordOrigin for anything else (T-04-3).

    The flip for BOTTOMLEFT is `page_height - t` / `page_height - b`, matching
    BoundingBox.to_top_left_origin -- asserted in tests against a hand-computed value, never
    by round-trip (T-04-1/2).
    """
    raise NotImplementedError


def normalise_prov(prov: list, pages: dict) -> list[dict]:
    """One normalised box per entry in `prov` -- a text item's prov list can carry several
    boxes (across columns or pages); each becomes its own token, never dropped or merged
    into a spanning box (T-04-8).

    `prov` is a list of objects with .page_no and .bbox (normalise_box()-shaped).
    `pages` maps page_no -> an object with .width/.height (DoclingDocument.pages[n].size).
    """
    raise NotImplementedError


def map_table(table_item, pages: dict) -> dict:
    """TableItem -> the wire table: {"rows", "cols", "cells": [...]}.

    Each cell carries row/col/row_span/col_span/text, plus x0/y0/x1/y1 only when
    table_cells[i].bbox is not None (T-04-13 -- an empty cell gets no box key, not a zero
    box). table_item.prov[0].bbox and each cell's own bbox are normalised by their OWN
    coord_origin (T-04-10..T-04-12): the table's prov is already flipped to BOTTOMLEFT by
    readingorder_model, its cells keep the table-structure model's own TOPLEFT.
    """
    raise NotImplementedError
