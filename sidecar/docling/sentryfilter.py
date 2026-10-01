"""Scrub rules for the sidecar's Sentry hooks; mirrors internal/platform/sentryfilter.go."""

import re

SIDECAR_LOGGERS = frozenset(
    {"app", "convert", "geometry", "uvicorn", "uvicorn.error", "uvicorn.access"}
)

_HEADERS = frozenset(
    {"accept", "content-length", "content-type", "host", "user-agent", "x-request-id"}
)
_ID_TAGS = ("request_id", "tenant_id")
_QUERY_KEYS = ("http.query", "http.fragment")
_UPSTREAM_STATUS = re.compile(r"returned [0-9]{3}: ")
_REDACTED = "[redacted]"
_PARAM_PREFIX = "sentry.message.parameter."


def _strip_query(s):
    """Remove each "?" or "#" and the run of non-whitespace after it."""
    if "?" not in s and "#" not in s:
        return s
    out = []
    skipping = False
    for ch in s:
        if ch in "?#":
            skipping = True
        elif ch.isspace():
            skipping = False
            out.append(ch)
        elif not skipping:
            out.append(ch)
    return "".join(out)


def _redact_quoted(s):
    """Replace each '...' or "..." segment; an unterminated quote redacts to the end.

    ceiling: stray apostrophes in non-customer text redact the rest of the line; revisit when it hides a fault
    """
    out = []
    i, n = 0, len(s)
    while i < n:
        ch = s[i]
        if ch not in "'\"":
            out.append(ch)
            i += 1
            continue
        out.append(ch + _REDACTED)
        i += 1
        while i < n and s[i] != ch:
            i += 2 if s[i] == "\\" else 1
        if i >= n:
            return "".join(out)
        out.append(ch)
        i += 1
    return "".join(out)


def _redact_upstream_reason(s):
    m = _UPSTREAM_STATUS.search(s)
    return s if m is None else s[: m.end()] + _REDACTED


def scrub_text(s):
    """ceiling: covers quoted text and upstream reasons only; re-triage raise sites when an event shows customer text"""
    return _strip_query(_redact_upstream_reason(_redact_quoted(s)))


def _is_query_key(k):
    return k in _QUERY_KEYS


def _scrub_key(k):
    return scrub_text(k if isinstance(k, str) else str(k))


def _scrub_value(v):
    if v is None or isinstance(v, (bool, int, float)):
        return v
    if isinstance(v, str):
        return scrub_text(v)
    if isinstance(v, bytes):
        return scrub_text(v.decode("utf-8", "replace"))
    if isinstance(v, dict):
        return {_scrub_key(k): _scrub_value(e) for k, e in v.items()}
    if isinstance(v, (list, tuple, set, frozenset)):
        return [_scrub_value(e) for e in v]
    # An arbitrary object's repr may hold document text.
    return _REDACTED


def _scrub_data(d):
    """Copy of d without top-level query or fragment keys, every key and string scrubbed.

    ceiling: two keys that redact alike merge; revisit if a lost key hides a fault
    """
    if not isinstance(d, dict):
        return _scrub_value(d)
    out = {}
    for k, v in d.items():
        sk = _scrub_key(k)
        if _is_query_key(k) or _is_query_key(sk):
            continue
        out[sk] = _scrub_value(v)
    return out


def _scrub_request(r):
    if not isinstance(r, dict):
        return {}
    out = {k: v for k, v in r.items() if k not in ("query_string", "data", "cookies", "env")}
    if isinstance(out.get("url"), str):
        out["url"] = _strip_query(out["url"])
    headers = r.get("headers")
    if isinstance(headers, dict):
        out["headers"] = {
            "-".join(p.capitalize() for p in k.split("-")): v
            for k, v in headers.items()
            if isinstance(k, str) and k.lower() in _HEADERS
        }
    else:
        out.pop("headers", None)
    return out


def _scrub_tags(tags):
    if not isinstance(tags, dict):
        return _scrub_value(tags)
    out = {}
    for k, v in tags.items():
        if k in _ID_TAGS:
            out[k] = v
            continue
        sk = _scrub_key(k)
        if sk in _ID_TAGS and sk in tags:
            continue
        out[sk] = _scrub_value(v)
    return out


def _scrub_span(s):
    if not isinstance(s, dict):
        return _scrub_value(s)
    out = dict(s)
    for f in ("name", "description"):
        if isinstance(out.get(f), str):
            out[f] = scrub_text(out[f])
    if "tags" in out:
        out["tags"] = _scrub_tags(out["tags"])
    if "data" in out:
        out["data"] = _scrub_data(out["data"])
    return out


def _scrub_crumb(b):
    if not isinstance(b, dict):
        return _scrub_value(b)
    out = dict(b)
    if isinstance(out.get("message"), str):
        out["message"] = scrub_text(out["message"])
    if "data" in out:
        out["data"] = _scrub_data(out["data"])
    return out


def scrub_event(event, hint=None):
    """Remove request data, queries, user identity and document text; never drops the event."""
    event.pop("user", None)
    if "request" in event:
        event["request"] = _scrub_request(event["request"])

    for f in ("message", "transaction"):
        if isinstance(event.get(f), str):
            event[f] = scrub_text(event[f])

    le = event.get("logentry")
    if isinstance(le, dict):
        event["logentry"] = _scrub_value(le)

    exc = event.get("exception")
    if isinstance(exc, dict) and isinstance(exc.get("values"), list):
        values = []
        for ev in exc["values"]:
            if isinstance(ev, dict) and isinstance(ev.get("value"), str):
                ev = {**ev, "value": scrub_text(ev["value"])}
            values.append(ev)
        event["exception"] = {**exc, "values": values}

    if "tags" in event:
        event["tags"] = _scrub_tags(event["tags"])

    ctxs = event.get("contexts")
    if isinstance(ctxs, dict):
        out = {}
        for k, c in ctxs.items():
            c = _scrub_data(c)
            if k == "trace" and isinstance(c, dict) and isinstance(c.get("data"), dict):
                c["data"] = _scrub_data(c["data"])
            out[_scrub_key(k)] = c
        event["contexts"] = out

    if "extra" in event:
        event["extra"] = _scrub_data(event["extra"])

    if isinstance(event.get("spans"), list):
        event["spans"] = [_scrub_span(s) for s in event["spans"]]

    # Breadcrumbs are shared with the scope, so each is replaced, not edited.
    crumbs = event.get("breadcrumbs")
    if isinstance(crumbs, dict) and isinstance(crumbs.get("values"), list):
        event["breadcrumbs"] = {**crumbs, "values": [_scrub_crumb(b) for b in crumbs["values"]]}
    elif crumbs is not None:
        event["breadcrumbs"] = _scrub_value(crumbs)
    return event


def scrub_transaction(event, hint=None):
    event = scrub_event(event, hint)
    event.pop("request", None)
    return event


def _scrub_log_value(v):
    """Only str, int, float, bool or a list of str survives; all else is redacted."""
    if isinstance(v, list) and all(isinstance(e, str) for e in v):
        return _scrub_value(v)
    if isinstance(v, (str, int, float)):
        return _scrub_value(v)
    return _REDACTED


def _redacted_message(template, args):
    """Render template with every argument replaced; the scrubbed template itself if it cannot format."""
    if isinstance(args, dict):
        redacted = {k: _REDACTED for k in args}
    else:
        redacted = (_REDACTED,) * len(args)
    try:
        return scrub_text(template % redacted)
    except (TypeError, ValueError, KeyError):
        return scrub_text(template)


def _template_args(attrs):
    """SDK log parameters: tuple args key by index, mapping args by name."""
    keys = [k[len(_PARAM_PREFIX) :] for k in attrs if k.startswith(_PARAM_PREFIX)]
    if all(k.isdigit() for k in keys):
        return (_REDACTED,) * len(keys)
    return dict.fromkeys(keys)


def scrub_log(log, hint=None):
    """Return the scrubbed record, or None for a record from a non-sidecar logger."""
    attrs = log.get("attributes") or {}
    if attrs.get("logger.name") not in SIDECAR_LOGGERS:
        return None
    template = attrs.get("sentry.message.template")
    args = _template_args(attrs)
    if isinstance(template, str) and len(args):
        log["body"] = _redacted_message(template, args)
    elif isinstance(log.get("body"), str):
        log["body"] = scrub_text(log["body"])
    out = {}
    for k, v in attrs.items():
        # Parameters are raw, unquoted values that scrub_text cannot see.
        if k.startswith(("sentry.message.parameter", "user.")):
            continue
        sk = _scrub_key(k)
        if _is_query_key(k) or _is_query_key(sk):
            continue
        out[sk] = _scrub_log_value(v)
    log["attributes"] = out
    return log


def keep_breadcrumb(crumb, hint=None):
    """Drop log breadcrumbs from third-party loggers; a kept one is re-rendered without its arguments."""
    if crumb.get("type") != "log":
        return crumb
    if crumb.get("category") not in SIDECAR_LOGGERS:
        return None
    record = (hint or {}).get("log_record")
    if record is not None and record.args and isinstance(record.msg, str):
        crumb = {**crumb, "message": _redacted_message(record.msg, record.args)}
    return crumb
