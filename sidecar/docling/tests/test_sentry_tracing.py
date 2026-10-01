"""SENTRY-05-04 (Core AC 5, D-7, D-10, D-15): the sidecar continues the caller's trace.

The marker is joined at runtime so no source line, and so no source-context frame line, holds it.
"""

import logging
import re
import secrets

import httpx
import pytest
from fastapi.testclient import TestClient

import app as app_module
import convert

PDF = "application/pdf"
MARKER = "a3f7-50YRTNES-XQZ"[::-1]
T = secrets.token_hex(16)
S = secrets.token_hex(8)
WIRE = {"pages": []}


@pytest.fixture
def client(sentry_capture, monkeypatch):
    monkeypatch.setattr(convert, "stub_read", lambda body, content_type: WIRE)
    return TestClient(app_module.app, raise_server_exceptions=False)


def _read(client, headers=None, path="/v1/read"):
    return client.post(
        path, content=b"%PDF-1.4\nx", headers={"content-type": PDF, **(headers or {})}
    )


def _sentry_trace(flag):
    return {"sentry-trace": f"{T}-{S}-{flag}"}


def _only(items):
    assert len(items) == 1
    return items[0]


def test_read_continues_the_inbound_trace(client, sentry_capture):
    resp = _read(client, {**_sentry_trace(1), "baggage": f"sentry-trace_id={T}"})
    assert resp.status_code == 200
    txn = _only(sentry_capture.transactions())
    assert txn["contexts"]["trace"]["trace_id"] == T
    assert txn["contexts"]["trace"]["parent_span_id"] == S
    assert txn["transaction"] == "/v1/read"
    assert txn["contexts"]["trace"]["op"] == "http.server"


def test_inbound_unsampled_flag_is_ignored(client, sentry_capture):
    resp = _read(client, _sentry_trace(0))
    assert resp.status_code == 200
    txn = _only(sentry_capture.transactions())
    assert txn["contexts"]["trace"]["trace_id"] == T


def test_no_header_starts_a_new_trace(client, sentry_capture):
    resp = _read(client)
    assert resp.status_code == 200
    txn = _only(sentry_capture.transactions())
    trace_id = txn["contexts"]["trace"]["trace_id"]
    assert re.fullmatch(r"[0-9a-f]{32}", trace_id)
    assert trace_id != T
    assert txn["contexts"]["trace"].get("parent_span_id") is None


@pytest.mark.parametrize(
    "header",
    ["garbage", "zz-zz-1", f"{T}-{S}-2", f"{T[:-1]}-{S}-1", f"{T}-{S}-1-extra", ""],
)
def test_malformed_sentry_trace_starts_a_new_trace(client, sentry_capture, header):
    resp = _read(client, {"sentry-trace": header})
    assert resp.status_code == 200
    trace = _only(sentry_capture.transactions())["contexts"]["trace"]
    assert re.fullmatch(r"[0-9a-f]{32}", trace["trace_id"])
    assert trace["trace_id"] != T
    assert trace.get("parent_span_id") is None


def test_healthz_makes_no_transaction(client, sentry_capture):
    for headers in (None, _sentry_trace(1)):
        resp = client.get("/healthz", headers=headers)
        assert resp.status_code == 200
        assert resp.json()["sentry"] == "on"
    assert sentry_capture.transactions() == []
    assert _read(client).status_code == 200
    txn = _only(sentry_capture.transactions())  # control: transactions are being sent
    assert txn["transaction"] == "/v1/read"


def test_issue_and_log_carry_the_inbound_trace(sentry_capture, monkeypatch):
    def noisy_failure(body, content_type):
        logging.getLogger("convert").warning("stage note")
        raise RuntimeError("boom")

    monkeypatch.setattr(convert, "stub_read", noisy_failure)
    client = TestClient(app_module.app, raise_server_exceptions=False)
    assert _read(client, _sentry_trace(1)).status_code == 500
    event = _only(sentry_capture.events())
    assert event["exception"]["values"][-1]["type"] == "RuntimeError"
    assert event["contexts"]["trace"]["trace_id"] == T
    notes = [log for log in sentry_capture.logs() if log["body"] == "stage note"]
    assert _only(notes)["trace_id"] == T
    span_id = _only(sentry_capture.transactions())["contexts"]["trace"]["span_id"]
    assert event["contexts"]["trace"]["span_id"] == span_id
    assert _only(notes)["span_id"] == span_id


def test_transaction_carries_no_request_and_no_query(client, sentry_capture):
    resp = _read(client, {"authorization": f"Bearer {MARKER}"}, path=f"/v1/read?q={MARKER}")
    assert resp.status_code == 200
    txn = _only(sentry_capture.transactions())
    assert txn["transaction"] == "/v1/read"
    assert "request" not in txn
    assert MARKER.encode() not in sentry_capture.raw()


def test_outbound_httpx_carries_no_trace_headers(sentry_capture, monkeypatch):
    seen = []

    def record_headers(request):
        seen.append(dict(request.headers))
        return httpx.Response(200)

    def call_third_party(body, content_type):
        with httpx.Client(transport=httpx.MockTransport(record_headers)) as third_party:
            third_party.get("http://third.party/x")
        return WIRE

    monkeypatch.setattr(convert, "stub_read", call_third_party)
    client = TestClient(app_module.app, raise_server_exceptions=False)
    assert _read(client, {**_sentry_trace(1), "baggage": f"sentry-trace_id={T}"}).status_code == 200
    assert _only(sentry_capture.transactions())["contexts"]["trace"]["trace_id"] == T
    headers = _only(seen)
    assert headers["host"] == "third.party"
    assert "sentry-trace" not in headers
    assert "baggage" not in headers
