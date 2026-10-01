"""SENTRY-05-03 (Q12): a crash, a 5xx or a failed warm-up opens one issue; nothing else does."""

import asyncio
import contextlib
import logging

import pytest
from fastapi.testclient import TestClient
from starlette.requests import ClientDisconnect

import app as app_module
import convert
import sentry_setup

PDF = "application/pdf"
DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


def _exception_types(event):
    return [v["type"] for v in event["exception"]["values"]]


def test_unhandled_exception_opens_one_issue(sentry_capture, monkeypatch):
    async def explode(request):
        raise RuntimeError("escaped")

    monkeypatch.setattr(app_module, "_read_capped_body", explode)
    client = TestClient(app_module.app, raise_server_exceptions=False)
    resp = client.post("/v1/read", content=b"%PDF", headers={"content-type": PDF})
    assert resp.status_code == 500
    events = sentry_capture.events()
    assert len(events) == 1
    assert _exception_types(events[0]) == ["RuntimeError"]


def test_warm_up_failure_opens_one_issue(sentry_capture, monkeypatch):
    def fail():
        raise RuntimeError("model missing")

    monkeypatch.setattr(convert, "_converter", None)
    monkeypatch.setattr(convert, "_construct_converter", fail)
    assert convert.warm_up() is None
    events = sentry_capture.events()
    assert len(events) == 1
    assert _exception_types(events[0]) == ["RuntimeError"]
    assert convert._converter is None


def test_client_disconnect_opens_no_issue(sentry_capture):
    scope = {
        "type": "http",
        "asgi": {"version": "3.0"},
        "http_version": "1.1",
        "method": "POST",
        "scheme": "http",
        "path": "/v1/read",
        "raw_path": b"/v1/read",
        "query_string": b"",
        "headers": [(b"content-type", b"application/pdf")],
        "client": ("127.0.0.1", 1),
        "server": ("testserver", 80),
    }
    script = [
        {"type": "http.request", "body": b"%PDF", "more_body": True},
        {"type": "http.disconnect"},
    ]
    received = 0

    async def receive():
        nonlocal received
        received += 1
        return script[min(received, len(script)) - 1]

    async def send(message):
        pass

    async def call():
        with contextlib.suppress(Exception):
            await app_module.app(scope, receive, send)

    asyncio.run(call())
    assert received >= 2  # the handler read through to the disconnect
    assert sentry_capture.events() == []


def test_sidecar_error_log_is_a_log_not_an_issue(sentry_capture):
    value = "a3f7-50YRTNES-XQZ"[::-1]  # runtime value: no source line holds it
    logging.getLogger("convert").error("open failed for %r", value)
    assert sentry_capture.events() == []
    logs = sentry_capture.logs()
    assert [log["body"] for log in logs] == ["open failed for '[redacted]'"]
    assert value.encode() not in sentry_capture.raw()


def test_third_party_error_log_is_neither(sentry_capture):
    logging.getLogger("docling.datamodel.document").error("open failed")
    logging.getLogger("convert").error("control")  # proves log capture is live
    assert sentry_capture.events() == []
    assert [log["body"] for log in sentry_capture.logs()] == ["control"]


def _fail_read(monkeypatch, exc):
    def failing_read(body, content_type):
        raise exc

    monkeypatch.setattr(convert, "stub_read", failing_read)


def _join_boot_warm_up():
    # The boot warm-up would otherwise build a real converter under a test that expects none.
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()


def _fail_construct(monkeypatch, exc):
    def construct():
        raise exc

    monkeypatch.setattr(convert, "_converter", None)
    monkeypatch.setattr(convert, "_construct_converter", construct)


@pytest.mark.parametrize("content_type", [PDF, DOCX])
def test_one_failure_is_one_event_and_one_log_item(sentry_capture, monkeypatch, content_type):
    _fail_read(monkeypatch, RuntimeError("boom"))
    resp = TestClient(app_module.app).post(
        "/v1/read", content=b"x", headers={"content-type": content_type}
    )
    assert resp.status_code == 500
    assert resp.json() == {"error": "internal error"}
    events = sentry_capture.events()
    assert len(events) == 1
    assert [log["body"] for log in sentry_capture.logs()] == ["unexpected /v1/read failure"]


def test_two_failures_are_two_events(sentry_capture, monkeypatch):
    _fail_read(monkeypatch, RuntimeError("first"))
    client = TestClient(app_module.app)
    assert client.post("/v1/read", content=b"x", headers={"content-type": PDF}).status_code == 500
    _fail_read(monkeypatch, RuntimeError("second"))
    assert client.post("/v1/read", content=b"x", headers={"content-type": PDF}).status_code == 500
    assert len(sentry_capture.events()) == 2


def test_converter_construction_failure_on_a_request_opens_one_issue(sentry_capture, monkeypatch):
    _fail_construct(monkeypatch, RuntimeError("model missing"))
    resp = TestClient(app_module.app).post("/v1/read", content=b"x", headers={"content-type": DOCX})
    assert resp.status_code == 500
    assert resp.json() == {"error": "internal error"}
    events = sentry_capture.events()
    assert len(events) == 1
    assert _exception_types(events[0]) == ["RuntimeError"]
    assert convert._converter is None


def test_warm_up_thread_failure_opens_one_issue(sentry_capture, monkeypatch):
    _fail_construct(monkeypatch, RuntimeError("model missing"))
    monkeypatch.setattr(convert, "_warmup_thread", None)
    thread = convert.start_warm_up()
    thread.join()
    assert not thread.is_alive()
    events = sentry_capture.events()
    assert len(events) == 1
    assert _exception_types(events[0]) == ["RuntimeError"]


def _asgi_post(script, extra_headers=()):
    """Drive the app with raw ASGI messages; returns (sent messages, receive calls)."""
    scope = {
        "type": "http",
        "asgi": {"version": "3.0"},
        "http_version": "1.1",
        "method": "POST",
        "scheme": "http",
        "path": "/v1/read",
        "raw_path": b"/v1/read",
        "query_string": b"",
        "headers": [(b"content-type", b"application/pdf"), *extra_headers],
        "client": ("127.0.0.1", 1),
        "server": ("testserver", 80),
    }
    sent, calls = [], []

    async def receive():
        calls.append(1)
        return script[min(len(calls), len(script)) - 1]

    async def send(message):
        sent.append(message)

    async def call():
        with contextlib.suppress(Exception):
            await app_module.app(scope, receive, send)

    asyncio.run(call())
    return sent, calls


def test_over_cap_content_length_header_is_413_and_opens_no_issue(sentry_capture):
    declared = str(app_module.MAX_DOCUMENT_BYTES + 1).encode()
    sent, calls = _asgi_post(
        [{"type": "http.request", "body": b"%PDF", "more_body": False}],
        [(b"content-length", declared)],
    )
    assert [m["status"] for m in sent if m["type"] == "http.response.start"] == [413]
    assert calls == []  # refused from the header, the body was never read
    assert sentry_capture.events() == []


def test_a_raised_client_disconnect_is_ignored_and_another_error_is_not(
    sentry_capture, monkeypatch
):
    async def disconnect(request):
        raise ClientDisconnect()

    monkeypatch.setattr(app_module, "_read_capped_body", disconnect)
    client = TestClient(app_module.app, raise_server_exceptions=False)
    client.post("/v1/read", content=b"%PDF", headers={"content-type": PDF})
    assert sentry_capture.events() == []

    async def crash(request):
        raise RuntimeError("escaped")

    monkeypatch.setattr(app_module, "_read_capped_body", crash)
    client.post("/v1/read", content=b"%PDF", headers={"content-type": PDF})
    assert len(sentry_capture.events()) == 1


def test_a_disconnect_before_any_body_opens_no_issue(sentry_capture):
    _, calls = _asgi_post([{"type": "http.disconnect"}])
    assert calls  # the handler read the stream
    assert sentry_capture.events() == []


def test_capture_with_no_client_still_answers_500(monkeypatch):
    _join_boot_warm_up()
    assert sentry_setup.sentry_state() == "off"
    _fail_read(monkeypatch, RuntimeError("boom"))
    resp = TestClient(app_module.app).post("/v1/read", content=b"x", headers={"content-type": PDF})
    assert resp.status_code == 500
    assert resp.json() == {"error": "internal error"}


def test_warm_up_with_no_client_returns_normally(monkeypatch):
    _join_boot_warm_up()
    assert sentry_setup.sentry_state() == "off"
    _fail_construct(monkeypatch, RuntimeError("model missing"))
    assert convert.warm_up() is None
    assert convert._converter is None


class _FailingHandler(logging.Handler):
    def emit(self, record):
        raise OSError("log sink down")


def test_a_failing_log_sink_still_gives_one_issue_and_a_500(sentry_capture, monkeypatch):
    _fail_read(monkeypatch, ValueError("boom"))
    handler = _FailingHandler()
    logging.getLogger("app").addHandler(handler)
    try:
        client = TestClient(app_module.app, raise_server_exceptions=False)
        resp = client.post("/v1/read", content=b"x", headers={"content-type": PDF})
    finally:
        logging.getLogger("app").removeHandler(handler)
    assert resp.status_code == 500
    events = sentry_capture.events()
    assert len(events) == 1
    assert set(_exception_types(events[0])) >= {"OSError", "ValueError"}
