"""SENTRY-05-03 (Q12): a crash, a 5xx or a failed warm-up opens one issue; nothing else does."""

import asyncio
import contextlib
import logging

from fastapi.testclient import TestClient

import app as app_module
import convert

PDF = "application/pdf"


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
