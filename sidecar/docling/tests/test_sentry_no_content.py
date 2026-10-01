"""No document content reaches a serialized envelope.

The marker is joined at runtime so no source line, and so no source-context frame line, holds it.
"""

import contextlib
import importlib
import io
import json
import logging

import pytest
import sentry_sdk
from docx import Document
from fastapi.testclient import TestClient
from sentry_capture import CapturingTransport

import app as app_module
import convert
import sentry_setup

MARKER = "a3f7-50YRTNES-XQZ"[::-1]
PDF = "application/pdf"
DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
FAKE_DSN = "https://public@o0.ingest.sentry.io/1"


def _fail_read(monkeypatch, exc):
    def failing_read(body, content_type):
        raise exc

    monkeypatch.setattr(convert, "stub_read", failing_read)


def _post(content, content_type):
    # Routes built before sentry_sdk.init skip the FastAPI body hook, so rebuild them under it.
    importlib.reload(app_module)
    client = TestClient(app_module.app)
    return client.post("/v1/read", content=content, headers={"content-type": content_type})


def _pdf_with_text(text):
    """A one-page PDF whose only content is `text`."""
    stream = f"BT /F1 24 Tf 72 700 Td ({text}) Tj ET"
    objects = [
        "<< /Type /Catalog /Pages 2 0 R >>",
        "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        (
            "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R"
            " /Resources << /Font << /F1 5 0 R >> >> >>"
        ),
        f"<< /Length {len(stream)} >>\nstream\n{stream}\nendstream",
        "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for number, body in enumerate(objects, start=1):
        offsets.append(len(out))
        out += f"{number} 0 obj\n{body}\nendobj\n".encode()
    xref = len(out)
    out += f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode()
    for offset in offsets:
        out += f"{offset:010d} 00000 n \n".encode()
    out += (
        f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode()
    )
    return bytes(out)


def _docx_with_text(text):
    buffer = io.BytesIO()
    doc = Document()
    doc.add_paragraph(text)
    doc.save(buffer)
    return buffer.getvalue()


def _frame_functions(event):
    frames = event["exception"]["values"][-1]["stacktrace"]["frames"]
    return [f["function"] for f in frames]


def test_marker_in_raw_body_and_locals_never_leaves(sentry_capture, monkeypatch):
    _fail_read(monkeypatch, RuntimeError("boom"))
    resp = _post(b"%PDF-1.4\n" + MARKER.encode(), PDF)
    assert resp.status_code == 500
    events = sentry_capture.events()
    assert len(events) == 1
    assert "read_document" in _frame_functions(events[0])
    assert MARKER.encode() not in sentry_capture.raw()


def test_marker_in_json_body_never_leaves(sentry_capture, monkeypatch):
    _fail_read(monkeypatch, RuntimeError("boom"))
    resp = _post(json.dumps({"note": MARKER}).encode(), "application/json")
    assert resp.status_code == 500
    assert len(sentry_capture.events()) == 1
    assert MARKER.encode() not in sentry_capture.raw()


def test_json_body_is_not_collected_without_the_scrub_hooks(monkeypatch):
    options = sentry_setup.sentry_options(
        {"SENTRY_DSN": FAKE_DSN, "RAILWAY_ENVIRONMENT_NAME": "production"}
    )
    transport = CapturingTransport()
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    _fail_read(monkeypatch, RuntimeError("boom"))
    try:
        sentry_sdk.init(
            **{**options, "before_send": None, "before_send_transaction": None},
            transport=transport,
        )
        resp = _post(json.dumps({"note": MARKER}).encode(), "application/json")
        events, raw = transport.events(), transport.raw()
    finally:
        sentry_sdk.get_client().close()
        sentry_sdk.get_global_scope().set_client(None)
    assert resp.status_code == 500
    assert len(events) == 1
    assert MARKER.encode() not in raw


def test_marker_quoted_in_exception_text_never_leaves(sentry_capture, monkeypatch):
    _fail_read(monkeypatch, ValueError(f"bad cell {MARKER!r}"))
    resp = _post(b"%PDF-1.4\nx", PDF)
    assert resp.status_code == 500
    events = sentry_capture.events()
    assert len(events) == 1
    assert events[0]["exception"]["values"][-1]["value"] == "bad cell '[redacted]'"
    assert MARKER.encode() not in sentry_capture.raw()


_DOCUMENTS = {"docx": (_docx_with_text, DOCX), "pdf": (_pdf_with_text, PDF)}


@pytest.mark.parametrize("kind", sorted(_DOCUMENTS))
def test_marker_in_a_real_document_never_leaves(sentry_capture, monkeypatch, kind):
    build, content_type = _DOCUMENTS[kind]
    read_back = []

    def fail_after_conversion(result):
        read_back.append(any(MARKER in item.text for item in result.document.texts))
        raise RuntimeError("wire build failed")

    monkeypatch.setattr(convert, "_to_wire_contract", fail_after_conversion)
    resp = _post(build(MARKER), content_type)
    assert resp.status_code == 500
    assert read_back == [True]  # docling read the marker, so its records were in play
    assert len(sentry_capture.events()) == 1
    assert MARKER.encode() not in sentry_capture.raw()


def test_unquoted_marker_in_a_third_party_log_never_leaves(sentry_capture):
    logging.getLogger("app").warning("control")  # proves breadcrumb and log capture are live
    logging.getLogger("docling.pipeline.standard_pdf_pipeline").error("Stage x failed: %s", MARKER)
    sentry_sdk.capture_exception(RuntimeError("after"))
    events = sentry_capture.events()
    assert len(events) == 1
    categories = [c.get("category") for c in sentry_capture.breadcrumbs()]
    assert "app" in categories
    assert not [c for c in categories if str(c).startswith("docling")]
    assert [log["body"] for log in sentry_capture.logs()] == ["control"]
    assert MARKER.encode() not in sentry_capture.raw()


_LOG_BODIES = {
    "tuple": ("convert", "stage failed: %s", (MARKER,), "stage failed: [redacted]"),
    "mapping": (
        "convert",
        "stage failed: %(cell)s",
        ({"cell": MARKER},),
        "stage failed: [redacted]",
    ),
    "uvicorn.access": (
        "uvicorn.access",
        '%s - "%s %s HTTP/%s" %d',
        (MARKER, "GET", "/x", "1.1", 200),
        '%s - "[redacted]" %d',
    ),
}


@pytest.mark.parametrize("kind", sorted(_LOG_BODIES))
def test_unquoted_log_argument_in_a_sidecar_log_never_leaves(sentry_capture, kind):
    name, template, args, want = _LOG_BODIES[kind]
    logging.getLogger(name).warning(template, *args)
    bodies = [log["body"] for log in sentry_capture.logs()]
    assert len(bodies) == 1  # capture is live, so the absence below is not vacuous
    assert MARKER not in bodies[0]
    assert MARKER.encode() not in sentry_capture.raw()
    assert bodies == [want]


def test_unquoted_log_argument_never_leaves_through_a_later_event(sentry_capture):
    # The log breadcrumb's message is the rendered record, arguments included.
    logging.getLogger("convert").warning("stage failed: %s", MARKER)
    sentry_sdk.capture_exception(RuntimeError("after"))
    assert len(sentry_capture.events()) == 1  # capture is live, so the absence below is not vacuous
    assert MARKER.encode() not in sentry_capture.raw()


def _parse_cell(cell):
    secret = cell
    raise ValueError(f"bad cell {secret!r}")


def _chain_cause():
    try:
        _parse_cell(MARKER)
    except ValueError as inner:
        raise RuntimeError("outer") from inner


def _with_note():
    exc = RuntimeError("outer")
    exc.add_note(f"cell {MARKER!r}")
    raise exc


def _bytes_arg():
    raise RuntimeError(MARKER.encode())


def _tuple_arg():
    raise RuntimeError(("cell", MARKER))


def _dict_arg():
    raise RuntimeError({"cell": MARKER})


_CHAINS = {
    "cause": (_chain_cause, ["ValueError", "RuntimeError"]),
    "note": (_with_note, ["RuntimeError"]),
    "bytes": (_bytes_arg, ["RuntimeError"]),
    "tuple": (_tuple_arg, ["RuntimeError"]),
    "dict": (_dict_arg, ["RuntimeError"]),
}


@pytest.mark.parametrize("kind", sorted(_CHAINS))
def test_marker_in_exception_chain_notes_and_args_never_leaves(sentry_capture, monkeypatch, kind):
    raiser, types = _CHAINS[kind]

    def failing_read(body, content_type):
        raiser()

    monkeypatch.setattr(convert, "stub_read", failing_read)
    resp = _post(b"%PDF-1.4\nx", PDF)
    assert resp.status_code == 500
    events = sentry_capture.events()
    assert len(events) == 1
    assert [v["type"] for v in events[0]["exception"]["values"]] == types
    assert MARKER.encode() not in sentry_capture.raw()


def test_marker_in_an_implicit_context_never_leaves(sentry_capture, monkeypatch):
    def failing_read(body, content_type):
        try:
            _parse_cell(MARKER)
        except ValueError:
            raise RuntimeError("outer")

    monkeypatch.setattr(convert, "stub_read", failing_read)
    resp = _post(b"%PDF-1.4\nx", PDF)
    assert resp.status_code == 500
    events = sentry_capture.events()
    assert len(events) == 1
    assert [v["type"] for v in events[0]["exception"]["values"]] == ["ValueError", "RuntimeError"]
    assert MARKER.encode() not in sentry_capture.raw()


def test_marker_in_a_warm_up_failure_never_leaves(sentry_capture, monkeypatch):
    def fail():
        raise FileNotFoundError(2, "No such file or directory", f"/models/{MARKER}")

    monkeypatch.setattr(convert, "_converter", None)
    monkeypatch.setattr(convert, "_construct_converter", fail)
    assert convert.warm_up() is None
    events = sentry_capture.events()
    assert len(events) == 1
    assert events[0]["exception"]["values"][-1]["value"] == (
        "[Errno 2] No such file or directory: '[redacted]'"
    )
    assert [log["body"] for log in sentry_capture.logs()] == [
        "docling warm-up failed; construction will retry on first /v1/read"
    ]
    assert MARKER.encode() not in sentry_capture.raw()


def _truncated_pdf():
    return b"%PDF-1.4\n1 0 obj\n<< /Title (" + MARKER.encode() + b") /Type /Catalog"


def test_an_unreadable_document_sends_nothing(sentry_capture):
    resp = _post(_truncated_pdf(), PDF)
    assert resp.status_code == 422
    assert sentry_capture.events() == []
    assert MARKER.encode() not in sentry_capture.raw()


def test_docling_records_never_become_breadcrumbs(sentry_capture, monkeypatch, caplog):
    real_read = convert.stub_read

    def read_then_fail(body, content_type):
        with contextlib.suppress(convert.DocumentUnreadable):
            real_read(body, content_type)
        raise RuntimeError("after")

    monkeypatch.setattr(convert, "stub_read", read_then_fail)
    with caplog.at_level(logging.INFO):
        resp = _post(_truncated_pdf(), PDF)
    assert resp.status_code == 500
    assert [r for r in caplog.records if r.name.startswith("docling")]  # docling spoke
    events = sentry_capture.events()
    assert len(events) == 1
    categories = [c.get("category") for c in events[0]["breadcrumbs"]["values"]]
    assert not [c for c in categories if str(c).startswith("docling")]
    assert MARKER.encode() not in sentry_capture.raw()


def test_unreadable_reason_is_returned_but_never_sent(sentry_capture, monkeypatch):
    def unreadable(body, content_type):
        raise convert.DocumentUnreadable(f"cannot read {MARKER}")

    monkeypatch.setattr(convert, "stub_read", unreadable)
    resp = _post(b"%PDF-1.4\nx", PDF)
    assert resp.status_code == 422
    assert MARKER in resp.json()["error"]
    assert sentry_capture.events() == []
    sentry_sdk.capture_exception(RuntimeError("after"))
    assert len(sentry_capture.events()) == 1
    assert MARKER.encode() not in sentry_capture.raw()


def test_marker_in_query_headers_and_cookies_never_leaves(sentry_capture, monkeypatch):
    _fail_read(monkeypatch, RuntimeError("boom"))
    importlib.reload(app_module)
    client = TestClient(app_module.app)
    resp = client.post(
        f"/v1/read?note={MARKER}",
        content=b"%PDF-1.4\nx",
        headers={
            "content-type": PDF,
            "x-note": MARKER,
            "cookie": f"s={MARKER}",
            "authorization": f"Bearer {MARKER}",
        },
    )
    assert resp.status_code == 500
    events = sentry_capture.events()
    assert len(events) == 1
    assert events[0]["request"]["method"] == "POST"
    assert MARKER.encode() not in sentry_capture.raw()
