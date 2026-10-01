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
        # empty segments and adjacent segments
        ('field ""', 'field "[redacted]"'),
        ("field ''", "field '[redacted]'"),
        (f'"{M}""{M}2"', '"[redacted]""[redacted]"'),
        (f"'{M}' and \"{M}2\"", "'[redacted]' and \"[redacted]\""),
        # both kinds in one text; each kind ignores the other inside
        (f"x \"a'{M}\" 'c\"{M}'", "x \"[redacted]\" '[redacted]'"),
        # an escaped delimiter stays inside; an escaped backslash does not
        (f'x "a\\"{M}" y', 'x "[redacted]" y'),
        (f"x '{M}\\\\' y '{M}2'", "x '[redacted]' y '[redacted]'"),
        # D-4 drops Go's '"' rune-literal exception
        ("a '\"' b", "a '[redacted]' b"),
        # a segment may span lines and hold non-ASCII text
        (f"a '{M}\n{M}2' b", "a '[redacted]' b"),
        (f"Ọ̀yọ́ café '{M}' — 日本", "Ọ̀yọ́ café '[redacted]' — 日本"),
    ]
    assert cases
    for given, want in cases:
        got = scrub_text(given)
        assert got == want, f"{given!r} -> {got!r}"
        assert M not in got
        assert scrub_text(got) == got, f"not idempotent on {got!r}"


def test_scrub_text_unterminated_quote_redacts_to_end():
    assert scrub_text(f"can't open {M}") == "can'[redacted]"
    assert scrub_text(f'x "{M} and more') == 'x "[redacted]'
    # a quote as the last character, and a backslash as the last character
    assert scrub_text('trailing "') == 'trailing "[redacted]'
    assert scrub_text("trailing '") == "trailing '[redacted]"
    assert scrub_text(f"bad '{M}\\") == "bad '[redacted]"
    # an escaped closer does not close
    assert scrub_text(f'bad "{M}\\"') == 'bad "[redacted]'


def test_scrub_text_upstream_reason_and_query():
    assert scrub_text(f"docling: /v1/read returned 500: {M}") == (
        "docling: /v1/read returned 500: [redacted]"
    )
    assert scrub_text(f"GET /v1/read?q={M}#f") == "GET /v1/read"
    # needs three digits, exactly
    assert scrub_text("returned 5: x") == "returned 5: x"
    assert scrub_text("returned 4220: x") == "returned 4220: x"
    assert scrub_text("validation service returned status 502") == (
        "validation service returned status 502"
    )
    # the first match wins; the reason runs to the end, across lines
    prefix = "docling: /v1/read returned 422: "
    assert scrub_text(f"{prefix}{M}\nline 2 retry returned 500: {M}") == f"{prefix}[redacted]"
    # a quoted look-alike is redacted first, so it never takes the match
    assert scrub_text(f"import 'returned 500: x' failed: docling returned 422: {M}") == (
        "import '[redacted]' failed: docling returned 422: [redacted]"
    )
    assert scrub_text(f"docling: /v1/read returned 500: '{M}") == (
        "docling: /v1/read returned 500: [redacted]"
    )


def test_scrub_text_strips_query_and_fragment():
    cases = [
        ("https://h/p?q=1", "https://h/p"),
        ("GET /p?", "GET /p"),
        ("/p#", "/p"),
        ("/p#frag", "/p"),
        ("/p?a=1?b=2", "/p"),
        ("/p?a=1 then /q#f", "/p then /q"),
        ("row #5", "row "),
        ("a?x\tb", "a\tb"),
        ("/p?q=Ünï\nnext", "/p\nnext"),
        # a "?" inside a quoted segment is redacted before the query rule sees it
        (f"key '{M}?' header '{M}2'", "key '[redacted]' header '[redacted]'"),
        (f"'{M}'?b", "'[redacted]'"),
    ]
    assert cases
    for given, want in cases:
        got = scrub_text(given)
        assert got == want, f"{given!r} -> {got!r}"
        assert M not in got


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


def test_scrub_event_keeps_all_six_headers_in_any_case():
    event = {
        "request": {
            "url": "http://h/v1/read",
            "headers": {
                "ACCEPT": "application/json",
                "content-LENGTH": "42",
                "Content-type": "text/csv",
                "host": "invoice.internal",
                "user-agent": "ops-probe/1",
                "X-REQUEST-ID": "req-2",
                "x-S2S-TOKEN": M,
                "X-Buyer-Tin": M,
                "Authorization": M,
            },
        }
    }
    out = scrub_event(event, None)
    assert out["request"]["headers"] == {
        "Accept": "application/json",
        "Content-Length": "42",
        "Content-Type": "text/csv",
        "Host": "invoice.internal",
        "User-Agent": "ops-probe/1",
        "X-Request-Id": "req-2",
    }
    assert M not in json.dumps(out)


def test_scrub_event_request_odd_shapes():
    # a non-str header key is dropped without raising
    out = scrub_event({"request": {"headers": {5: M, "ACCEPT": "a"}}}, None)
    assert out["request"]["headers"] == {"Accept": "a"}

    # headers that are not a dict cannot be allowlisted, so none survive
    out = scrub_event({"request": {"headers": [["Authorization", M]]}}, None)
    assert "headers" not in out["request"]
    assert M not in json.dumps(out)

    # a request that is not a dict is replaced, not passed through
    out = scrub_event({"request": M}, None)
    assert out["request"] == {}

    # fragment and query both leave the url; a url without either is untouched
    out = scrub_event({"request": {"url": f"http://h/v1/read#frag?{M}"}}, None)
    assert out["request"]["url"] == "http://h/v1/read"
    out = scrub_event({"request": {"url": "http://h/v1/read", "method": "GET"}}, None)
    assert out["request"] == {"url": "http://h/v1/read", "method": "GET"}


def test_scrub_event_scrubs_contexts_and_extra_at_depth():
    q = f"'{M}'"
    deep = {"l1": [{"l2": {"l3": [f"d {q}", [f"e {q}", {f"k {q}": f"v {q}"}]]}}]}
    event = {
        "contexts": {
            f"ctx {q}": {"deep": deep, "n": 1},
            7: {"a": f"x {q}"},
        },
        "extra": {
            "deep": deep,
            3: f"x {q}",
            (f"t {q}", 1): "v",
            "raw": f"b {q}".encode(),
            "pair": (f"p {q}", 2),
        },
    }
    assert M in repr(event)

    out = scrub_event(event, None)

    assert set(out["contexts"]) == {"ctx '[redacted]'", "7"}
    want_deep = {
        "l1": [
            {
                "l2": {
                    "l3": [
                        "d '[redacted]'",
                        ["e '[redacted]'", {"k '[redacted]'": "v '[redacted]'"}],
                    ]
                }
            }
        ]
    }
    assert out["contexts"]["ctx '[redacted]'"]["deep"] == want_deep
    assert out["contexts"]["ctx '[redacted]'"]["n"] == 1
    assert out["extra"]["deep"] == want_deep
    assert out["extra"]["3"] == "x '[redacted]'"
    assert out["extra"]['("[redacted]", 1)'] == "v"
    assert out["extra"]["raw"] == "b '[redacted]'"
    assert out["extra"]["pair"] == ["p '[redacted]'", 2]
    assert M not in json.dumps(out)


def test_scrub_event_id_tags_stay_byte_identical():
    request_id = "req 'x'?y=1"
    tenant_id = "tnt #z"
    event = {
        "tags": {
            "request_id": request_id,
            "tenant_id": tenant_id,
            "other": "o?q",
            "request_id#x": f"look-alike {M}",
            "tenant_id?y": f"look-alike {M}",
        }
    }
    out = scrub_event(event, None)
    # a look-alike key never overwrites the real id tag
    assert out["tags"] == {"request_id": request_id, "tenant_id": tenant_id, "other": "o"}
    assert M not in json.dumps(out)


def test_scrub_event_query_keys_and_odd_key_text():
    q = f"'{M}'"
    bag = {
        f"k {q}": "top",
        f"q?tin={M}": "query",
        f"f#{M}": "fragment",
        "http.query?x=1": f"q={M}",
        "http.fragment#x": M,
        "http.query": M,
        "http.fragment": M,
        "nested": {
            f"k {q}": "v",
            "http.query": f"?tin={M}",
            "list": [{"http.query": f"'{M}'"}],
        },
    }
    event = {
        "extra": bag,
        "breadcrumbs": {"values": [{"data": bag}]},
        "spans": [{"data": bag}],
        "contexts": {"trace": {"data": bag}},
    }
    out = scrub_event(copy.deepcopy(event), None)

    assert M not in json.dumps(out)
    for got in (
        out["extra"],
        out["breadcrumbs"]["values"][0]["data"],
        out["spans"][0]["data"],
        out["contexts"]["trace"]["data"],
    ):
        assert set(got) == {"k '[redacted]'", "q", "f", "nested"}
        assert got["k '[redacted]'"] == "top"
        assert got["q"] == "query"
        # only the top-level query keys go; the nested one stays, its value scrubbed
        assert got["nested"]["http.query"] == ""
        assert got["nested"]["list"] == [{"http.query": "'[redacted]'"}]


def test_scrub_event_breadcrumb_message_and_odd_shapes():
    out = scrub_event(
        {
            "breadcrumbs": {
                "values": [
                    {"category": "httpx", "message": f"GET /x?q={M}", "level": "info"},
                    {"category": "db", "type": "query"},
                    "stray",
                ]
            }
        },
        None,
    )
    crumbs = out["breadcrumbs"]["values"]
    assert crumbs[0] == {"category": "httpx", "message": "GET /x", "level": "info"}
    assert crumbs[1] == {"category": "db", "type": "query"}
    assert crumbs[2] == "stray"

    assert scrub_event({"breadcrumbs": {"values": []}}, None)["breadcrumbs"] == {"values": []}


def test_scrub_event_span_name_description_and_other_fields():
    span = {
        "span_id": "s1",
        "trace_id": "t1",
        "op": "http.client",
        "status": "ok",
        "start_timestamp": 1.5,
        "timestamp": 2.5,
        "name": f"GET /x?q={M}",
        "description": f"GET /x?q={M} '{M}'",
        "tags": {f"k?{M}": f"v '{M}'"},
        "data": {"http.query": M, "n": 3},
    }
    out = scrub_event({"spans": [span, {"op": "bare"}]}, None)
    first, second = out["spans"]
    assert first["name"] == "GET /x"
    assert first["description"] == "GET /x '[redacted]'"
    assert first["tags"] == {"k": "v '[redacted]'"}
    assert first["data"] == {"n": 3}
    for f in ("span_id", "trace_id", "op", "status", "start_timestamp", "timestamp"):
        assert first[f] == span[f], f
    assert second == {"op": "bare"}
    assert M not in json.dumps(out)


def test_scrub_event_keeps_unrelated_fields_and_non_text_exception_parts():
    event = {
        "event_id": "e1",
        "level": "error",
        "platform": "python",
        "timestamp": 12.5,
        "server_name": "docling",
        "release": "r1",
        "exception": {
            "values": [
                {"type": "ValueError", "value": None, "module": "m"},
                {"type": "KeyError", "mechanism": {"handled": True}},
            ]
        },
    }
    want = copy.deepcopy(event)
    assert scrub_event(event, None) == want


def test_scrub_event_never_returns_none_on_odd_shapes():
    shapes = [
        {},
        {"request": None},
        {"breadcrumbs": None},
        {"breadcrumbs": [f"x '{M}'"]},
        {"spans": []},
        {"exception": {"values": []}},
        {"tags": None},
        {"contexts": {}},
        {"extra": None},
    ]
    for event in shapes:
        out = scrub_event(copy.deepcopy(event), None)
        assert isinstance(out, dict), event
        assert M not in json.dumps(out), event


def test_scrub_transaction_equals_scrub_event_minus_request():
    event = {
        "type": "transaction",
        "transaction": f"/v1/read?q={M}",
        "user": {"id": M},
        "request": {"url": f"http://h/v1/read?q={M}"},
        "tags": {"t": f"v '{M}'"},
        "extra": {"e": f"v '{M}'"},
        "contexts": {"trace": {"trace_id": "t1", "data": {"http.query": M}}},
        "spans": [{"op": "x", "description": f"d '{M}'"}],
        "breadcrumbs": {"values": [{"message": f"m '{M}'"}]},
    }
    want = scrub_event(copy.deepcopy(event), None)
    assert "request" in want
    del want["request"]

    got = scrub_transaction(copy.deepcopy(event), None)

    assert want["tags"] and want["spans"] and want["contexts"]
    assert got == want
    assert M not in json.dumps(got)


_SIDECAR_NAMES = ["app", "convert", "geometry", "uvicorn", "uvicorn.error", "uvicorn.access"]
_OTHER_NAMES = [
    "docling",
    "docling.datamodel.document",
    "torch",
    "App",
    "app.sub",
    "convert.x",
    "uvicorn.errors",
    "",
    5,
]


def _sidecar_log(attrs=None, body=None):
    log = {"attributes": {"logger.name": "app", **(attrs or {})}}
    if body is not None:
        log["body"] = body
    return log


def test_scrub_log_keeps_each_of_the_six_sidecar_loggers():
    for name in _SIDECAR_NAMES:
        out = scrub_log({"body": f"b '{M}'", "attributes": {"logger.name": name}}, None)
        assert out is not None, name
        assert out["body"] == "b '[redacted]'", name
        assert out["attributes"]["logger.name"] == name


def test_scrub_log_drops_near_miss_logger_names():
    assert _OTHER_NAMES
    for name in _OTHER_NAMES:
        log = {"body": f"b '{M}'", "attributes": {"logger.name": name}}
        assert scrub_log(log, None) is None, repr(name)
    for log in [{"body": "b"}, {"body": "b", "attributes": None}, {"body": "b", "attributes": {}}]:
        assert scrub_log(log, None) is None, log


def test_scrub_log_never_returns_none_for_a_sidecar_logger():
    out = scrub_log({"attributes": {"logger.name": "app"}}, None)
    assert out == {"attributes": {"logger.name": "app"}}
    out = scrub_log({"body": None, "attributes": {"logger.name": "app"}}, None)
    assert out is not None


def test_scrub_log_attribute_key_rules():
    attrs = {
        "sentry.message.parameter.0": M,
        "sentry.message.parameter.1": M,
        "sentry.message.parameter.12": M,
        "user.id": M,
        "user.email": M,
        "http.query": M,
        "http.fragment": M,
        "http.query?x=1": M,
        "http.fragment#x": M,
        "sentry.message.template": "t %s",
        "sentry.origin": "auto.log.python",
        "username": "kept",
        "user": "kept",
        f"k '{M}'": "v",
        f"q?tin={M}": "w",
        "plain": "x",
    }
    out = scrub_log(_sidecar_log(attrs, body="b"), None)
    assert out is not None
    assert out["attributes"] == {
        "logger.name": "app",
        "sentry.message.template": "t %s",
        "sentry.origin": "auto.log.python",
        "username": "kept",
        "user": "kept",
        "k '[redacted]'": "v",
        "q": "w",
        "plain": "x",
    }
    assert M not in repr(out)


def test_scrub_log_value_rules():
    attrs = {
        "i0": 0,
        "ineg": -3,
        "ibig": 2**70,
        "f": 0.25,
        "t": True,
        "fl": False,
        "s": f"v '{M}' ?q={M}",
        "empty": [],
        "strs": [f"a '{M}'", "plain", f"b?{M}"],
        "obj": _Holder(),
        "objs": [_Holder(), "ok"],
    }
    out = scrub_log(_sidecar_log(attrs), None)
    assert out is not None
    got = out["attributes"]
    assert got["i0"] == 0 and got["i0"] is not False
    assert got["ineg"] == -3
    assert got["ibig"] == 2**70
    assert got["f"] == 0.25
    assert got["t"] is True
    assert got["fl"] is False
    assert got["s"] == "v '[redacted]' "
    assert got["empty"] == []
    assert got["strs"] == ["a '[redacted]'", "plain", "b"]
    assert got["obj"] == "[redacted]"
    assert got["objs"] == ["[redacted]", "ok"]
    assert M not in repr(out)


def test_keep_breadcrumb_keeps_each_sidecar_log_crumb():
    for name in _SIDECAR_NAMES:
        crumb = {"type": "log", "category": name, "message": "m"}
        assert keep_breadcrumb(crumb, None) == crumb, name


def test_keep_breadcrumb_drops_every_other_log_crumb():
    assert _OTHER_NAMES
    for name in _OTHER_NAMES:
        assert keep_breadcrumb({"type": "log", "category": name}, None) is None, repr(name)
    assert keep_breadcrumb({"type": "log"}, None) is None
    assert keep_breadcrumb({"type": "log", "category": None}, None) is None


def test_keep_breadcrumb_keeps_non_log_crumbs_whatever_the_category():
    crumbs = [
        {"type": "http", "category": "httpx"},
        {"type": "default", "category": "docling.datamodel.document"},
        {"type": "query", "category": "torch"},
        {"category": "docling.x"},
        {},
    ]
    for crumb in crumbs:
        assert keep_breadcrumb(crumb, None) == crumb, crumb
