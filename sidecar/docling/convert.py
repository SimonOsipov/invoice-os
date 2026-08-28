"""Docling conversion. Stub for EXTR-03-01; EXTR-03-03 replaces stub_read with the real DocumentConverter."""

_construction_count = 0


def construction_count() -> int:
    """How many times a real DocumentConverter has been built. Stays 0 until EXTR-03-03."""
    return _construction_count


def stub_read(body: bytes, content_type: str) -> dict:
    """Fixed, contract-shaped response — see EXTR-03 story sec. 3."""
    raise NotImplementedError
