"""SENTRY-05-03 (Core AC 4, D-18): no document content reaches a serialized envelope.

The marker is joined at runtime so no source line, and so no source-context frame line, holds it.
"""

import importlib
import io
import json
import logging

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
    options.setdefault("trace_propagation_targets", [])
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


def test_marker_in_a_real_document_never_leaves(sentry_capture, monkeypatch):
    buffer = io.BytesIO()
    doc = Document()
    doc.add_paragraph(MARKER)
    doc.save(buffer)
    read_back = []

    def fail_after_conversion(result):
        read_back.append(any(MARKER in item.text for item in result.document.texts))
        raise RuntimeError("wire build failed")

    monkeypatch.setattr(convert, "_to_wire_contract", fail_after_conversion)
    resp = _post(buffer.getvalue(), DOCX)
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
