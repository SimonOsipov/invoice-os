"""Regenerates scanned_ocr_fixture.pdf: run inside docling:canary (needs Pillow).

    docker run --rm -v "$(pwd)":/repo -w /repo docling:canary \\
        python3 sidecar/docling/tests/testdata/gen_scanned_ocr_fixture.py

Not part of the pytest run -- one-off, like fetch_rapidocr_models.py. Not a copy of
internal/extraction/testdata/scanned_invoice.pdf: that fixture is a 4x4 checkerboard with no
real glyphs (EXTR-02's AC-6 only needs "has no text layer", not "OCR reads this"), so it can't
serve T-03-6, which needs raster text a Chinese-model run would garble. Hand-builds a single-page
PDF the same way internal/extraction/fixtures_test.go does (uncompressed xref table, one Image
XObject) -- FlateDecode on the pixel stream is the one deviation, to keep this file small.

24 short lines (an invoice-shaped layout) so RapidOCR's line detector -> docling's per-line
TextCells clears T-03-6's >=20 token floor with margin; "ASCOMPLY" is the known string T-03-6
asserts verbatim. Confirmed 100% OCR-accurate and 24/24 lines recovered against this story's
pinned config (lang=["en"], backend="onnxruntime", num_threads=2) inside docling:canary.
"""

import sys
import zlib
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

OUT = Path(__file__).parent / "scanned_ocr_fixture.pdf"

W, H = 900, 1400
PAGE_W_PT, PAGE_H_PT = 612, 792  # US Letter, matching internal/extraction/fixtures_test.go

LINES = [
    "ASCOMPLY",
    "INVOICE",
    "Invoice No INV 9001",
    "Date 2026 08 29",
    "Bill To Acme Traders",
    "Ship To Lagos Warehouse",
    "Line Item One",
    "Line Item Two",
    "Line Item Three",
    "Line Item Four",
    "Line Item Five",
    "Quantity Ten Units",
    "Unit Price 1500",
    "Subtotal Amount",
    "NGN 39500.00",
    "VAT Seven Point Five",
    "NGN 2962.50",
    "Total Due Now",
    "NGN 42462.50",
    "Payment Terms Net 30",
    "Bank Account Number",
    "Reference Code Alpha",
    "Authorized Signature",
    "Thank You",
]


def _render_page() -> bytes:
    """Grayscale raster of LINES -- raw 8-bit DeviceGray pixels, row-major."""
    img = Image.new("L", (W, H), color=255)
    draw = ImageDraw.Draw(img)
    font_big = ImageFont.load_default(size=48)
    font = ImageFont.load_default(size=32)

    y = 20
    draw.text((30, y), LINES[0], fill=0, font=font_big)
    y += 70
    for line in LINES[1:]:
        draw.text((30, y), line, fill=0, font=font)
        y += 52

    return img.tobytes()


def _obj(n: int, body: bytes) -> bytes:
    return f"{n} 0 obj\n".encode() + body + b"\nendobj\n"


def build() -> bytes:
    pixels = zlib.compress(_render_page(), level=9)

    offsets: dict[int, int] = {}
    buf = bytearray()
    buf += b"%PDF-1.4\n"
    buf += bytes([0x25, 0xE2, 0xE3, 0xCF, 0xD3]) + b"\n"

    def add(n: int, body: bytes) -> None:
        offsets[n] = len(buf)
        buf.extend(_obj(n, body))

    add(1, b"<< /Type /Catalog /Pages 2 0 R >>")
    add(2, b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
    add(
        3,
        f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 {PAGE_W_PT} {PAGE_H_PT}] "
        f"/Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>".encode(),
    )
    content = f"q\n{PAGE_W_PT} 0 0 {PAGE_H_PT} 0 0 cm\n/Im0 Do\nQ\n".encode()
    add(4, f"<< /Length {len(content)} >>\nstream\n".encode() + content + b"\nendstream")
    img_dict = (
        f"<< /Type /XObject /Subtype /Image /Width {W} /Height {H} "
        f"/ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode "
        f"/Length {len(pixels)} >>\nstream\n".encode()
    )
    add(5, img_dict + pixels + b"\nendstream")

    xref_offset = len(buf)
    n_objs = 5
    buf += f"xref\n0 {n_objs + 1}\n0000000000 65535 f \n".encode()
    for i in range(1, n_objs + 1):
        buf += f"{offsets[i]:010d} 00000 n \n".encode()
    buf += (
        f"trailer\n<< /Size {n_objs + 1} /Root 1 0 R >>\nstartxref\n{xref_offset}\n%%EOF\n".encode()
    )
    return bytes(buf)


if __name__ == "__main__":
    OUT.write_bytes(build())
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes)", file=sys.stderr)
