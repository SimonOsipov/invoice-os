"""SENTRY-05-01: the sidecar's scrub rules mirror internal/platform/sentryfilter.go.

M is a unique marker standing for document content; no output may contain it.
"""

import copy
import json

from sentryfilter import (
    SIDECAR_LOGGERS,
    keep_breadcrumb,
    scrub_event,
    scrub_log,
    scrub_text,
    scrub_transaction,
)

M = "MARKER-7f3a9c"


def test_scrub_text_redacts_quoted_segments():
    cases = [
        (f"bad cell '{M}'", "bad cell '[redacted]'"),
        (f'x "{M}" y', 'x "[redacted]" y'),
        (f"a '{M}\\'s' b", "a '[redacted]' b"),
        (f"b'%PDF {M}'", "b'[redacted]'"),
        (f"input_value='{M}', input_type=str", "input_value='[redacted]', input_type=str"),
        (f'"it\'s {M}"', '"[redacted]"'),
        (f"'say \"{M}\"'", "'[redacted]'"),
    ]
    for given, want in cases:
        got = scrub_text(given)
        assert got == want, f"{given!r} -> {got!r}"
        assert M not in got


def test_scrub_text_unterminated_quote_redacts_to_end():
    assert scrub_text(f"can't open {M}") == "can'[redacted]"
    assert scrub_text(f'x "{M} and more') == 'x "[redacted]'


def test_scrub_text_upstream_reason_and_query():
    assert scrub_text(f"docling: /v1/read returned 500: {M}") == (
        "docling: /v1/read returned 500: [redacted]"
    )
    assert scrub_text(f"GET /v1/read?q={M}#f") == "GET /v1/read"
    # needs three digits
    assert scrub_text("returned 5: x") == "returned 5: x"


def test_scrub_text_leaves_plain_text():
    for s in ["", "stage preprocess failed", "width=0 height=12"]:
        assert scrub_text(s) == s


def test_scrub_event_request_keeps_only_allowlisted_parts():
    event = {
        "user": {"id": M, "ip_address": M},
        "request": {
            "method": "POST",
            "url": f"http://h/v1/read?q={M}",
            "query_string": f"q={M}",
            "data": M,
            "cookies": {"s": M},
            "env": {"REMOTE_ADDR": M},
            "headers": {
                "Authorization": M,
                "cookie": M,
                "x-forwarded-for": M,
                "content-type": "application/pdf",
                "X-Request-Id": "r1",
                "user-agent": "Go-http-client/1.1",
            },
        },
    }
    assert M in json.dumps(event)

    out = scrub_event(event, None)

    assert "user" not in out
    req = out["request"]
    assert req["url"] == "http://h/v1/read"
    assert req["method"] == "POST"
    for key in ("query_string", "data", "cookies"):
        assert not req.get(key), key
    assert "env" not in req
    headers = {k.lower(): v for k, v in req["headers"].items()}
    assert headers == {
        "content-type": "application/pdf",
        "x-request-id": "r1",
        "user-agent": "Go-http-client/1.1",
    }
    assert M not in json.dumps(out)


def test_scrub_event_scrubs_every_text_field():
    q = f"'{M}'"
    event = {
        "message": f"msg {q}",
        "logentry": {
            "message": f"le %s {q}",
            "formatted": f"le {q}",
            "params": [f"p {q}", "plain"],
        },
        "exception": {
            "values": [
                {"type": "ValueError", "value": f"bad cell {q} here"},
                {"type": "OSError", "value": f'open failed for "{M}"'},
            ]
        },
        "transaction": f"/v1/read?q={M}",
        "tags": {f"tk {q}": f"tv {q}", "plain": "ok"},
        "contexts": {
            "app": {
                f"ck {q}": 1,
                "name": f"n {q}",
                "deep": {"a": [f"x {q}", {"b": f"y {q}"}]},
            },
            "trace": {
                "trace_id": "t1",
                "data": {"http.query": M, "http.fragment": M, "keep": f"d {q}"},
            },
        },
        "extra": {f"ek {q}": f"ev {q}", "nested": {"l": [f"z {q}"]}},
        "breadcrumbs": {
            "values": [
                {
                    "category": "httpx",
                    "message": f"bm {q}",
                    "data": {
                        "url": f"http://h/x?q={M}",
                        "http.query": M,
                        "http.fragment": M,
                        "note": f"bd {q}",
                    },
                }
            ]
        },
        "spans": [
            {
                "op": "http.client",
                "description": f"GET /x {q}",
                "data": {"http.query": M, "http.fragment": M, "sd": f"sd {q}"},
                "tags": {f"st {q}": f"sv {q}"},
            }
        ],
    }
    assert M in json.dumps(event)

    out = scrub_event(event, None)

    values = out["exception"]["values"]
    assert values[0]["value"] == "bad cell '[redacted]' here"
    assert values[1]["value"] == 'open failed for "[redacted]"'
    assert out["message"] == "msg '[redacted]'"
    assert out["logentry"]["formatted"] == "le '[redacted]'"
    assert out["logentry"]["params"] == ["p '[redacted]'", "plain"]
    assert out["transaction"] == "/v1/read"
    assert out["tags"]["plain"] == "ok"
    assert out["contexts"]["trace"]["data"]["keep"] == "d '[redacted]'"
    assert out["spans"][0]["data"]["sd"] == "sd '[redacted]'"
    assert out["breadcrumbs"]["values"][0]["data"]["url"] == "http://h/x"
    for bag in (
        out["contexts"]["trace"]["data"],
        out["spans"][0]["data"],
        out["breadcrumbs"]["values"][0]["data"],
    ):
        assert "http.query" not in bag
        assert "http.fragment" not in bag
    assert M not in json.dumps(out)


def test_scrub_event_never_drops_and_keeps_scalars():
    assert isinstance(scrub_event({}, None), dict)

    event = {"contexts": {"c": {"i": 7, "f": 1.5, "t": True, "n": None, "z": 0}}}
    out = scrub_event(copy.deepcopy(event), None)
    assert isinstance(out, dict)
    c = out["contexts"]["c"]
    assert c["i"] == 7 and c["f"] == 1.5 and c["z"] == 0
    assert c["t"] is True
    assert c["n"] is None


def test_scrub_transaction_drops_request():
    event = {
        "type": "transaction",
        "transaction": "/v1/read",
        "request": {"url": f"http://h/v1/read?q={M}", "headers": {"Authorization": M}},
        "spans": [{"op": "x", "description": "d", "data": {"http.query": M, "keep": "v"}}],
    }
    assert M in json.dumps(event)

    out = scrub_transaction(event, None)

    assert out["transaction"] == "/v1/read"
    assert "request" not in out
    assert "http.query" not in out["spans"][0]["data"]
    assert out["spans"][0]["data"]["keep"] == "v"
    assert M not in json.dumps(out)


class _Holder:
    """An object whose repr holds document text, as an `extra=` value could."""

    def __repr__(self):
        return f"<Holder {M}>"


def _log(logger_name):
    attrs = {
        "sentry.message.parameter.0": M,
        "sentry.message.template": "open failed for %r",
        "user.id": M,
        "http.query": M,
        "code.line.number": 12,
        "ratio": 0.5,
        "ok": True,
        "tags": [f"x '{M}'"],
        "obj": _Holder(),
    }
    if logger_name is not None:
        attrs["logger.name"] = logger_name
    return {"body": f"open failed for '{M}'", "attributes": attrs}


def test_scrub_log_drops_parameters_and_scrubs_body():
    log = _log("convert")
    assert M in repr(log)

    out = scrub_log(log, None)

    assert out is not None
    assert out["body"] == "open failed for '[redacted]'"
    attrs = out["attributes"]
    for gone in ("sentry.message.parameter.0", "user.id", "http.query"):
        assert gone not in attrs
    assert attrs["sentry.message.template"] == "open failed for %r"
    assert attrs["code.line.number"] == 12
    assert attrs["ratio"] == 0.5
    assert attrs["ok"] is True
    assert attrs["tags"] == ["x '[redacted]'"]
    assert attrs["obj"] == "[redacted]"
    assert M not in repr(out)


def test_scrub_log_drops_third_party_loggers():
    for name in ["docling.pipeline.standard_pdf_pipeline", "torch", None]:
        assert scrub_log(_log(name), None) is None, name

    kept = scrub_log(_log("uvicorn.access"), None)
    assert kept is not None
    assert kept["body"] == "open failed for '[redacted]'"


def test_keep_breadcrumb_only_sidecar_log_crumbs():
    assert keep_breadcrumb({"type": "log", "category": "docling.datamodel.document"}, None) is None

    for name in sorted(SIDECAR_LOGGERS):
        crumb = {"type": "log", "category": name}
        assert keep_breadcrumb(crumb, None) == crumb, name

    http = {"type": "http", "category": "httpx"}
    assert keep_breadcrumb(http, None) == http
