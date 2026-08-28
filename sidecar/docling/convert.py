"""Docling conversion. Stub for EXTR-03-01; EXTR-03-03 replaces stub_read with the real DocumentConverter."""

_construction_count = 0


def construction_count() -> int:
    """How many times a real DocumentConverter has been built. Stays 0 until EXTR-03-03."""
    return _construction_count


def stub_read(body: bytes, content_type: str) -> dict:
    """Fixed, contract-shaped response — see EXTR-03 story sec. 3.

    docling_version is "stub": no real DoclingDocument exists yet (EXTR-03-03), and a
    real-looking value here would misrepresent what produced this response.
    """
    return {
        "reader": "docling",
        "version": "v1",
        "docling_version": "stub",
        "pages": [
            {
                "number": 1,
                "width_pt": 612.0,
                "height_pt": 792.0,
                "tokens": [
                    {"text": "STUB", "x0": 0.1, "y0": 0.1, "x1": 0.2, "y1": 0.15},
                ],
                "tables": [],
            }
        ],
    }
