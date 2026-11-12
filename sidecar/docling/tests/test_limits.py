"""SECAUD-04-04: page cap, DOCX XML bound, and the three ways a conversion is stopped
(caller deadline, disconnect, 240 s backstop). Needs docling: Docker `test` stage only.
"""

import contextvars
import importlib
import io
import socket
import threading
import time
import zipfile
from pathlib import Path
from types import SimpleNamespace

import pytest
import uvicorn
from fastapi.testclient import TestClient

import app as app_module
import convert

TESTDATA = Path(__file__).parent / "testdata"
FIXTURES = Path(__file__).parent / "fixtures"
PDF = {"content-type": "application/pdf"}
DOCX = {
    "content-type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
}
ANSWER_MARGIN_S = 60  # docling.go doclingAnswerMargin


def npage(n: int) -> bytes:
    """A PDF of n blank pages."""
    objs = ["<< /Type /Catalog /Pages 2 0 R >>", None]
    kids = []
    for i in range(n):
        kids.append(f"{i + 3} 0 R")
        objs.append("<< /Type /Page /Parent 2 0 R >>")
    objs[1] = f"<< /Type /Pages /Kids [{' '.join(kids)}] /Count {n} /MediaBox [0 0 612 792] >>"
    out = b"%PDF-1.4\n"
    offs = []
    for i, o in enumerate(objs):
        offs.append(len(out))
        out += f"{i + 1} 0 obj\n{o}\nendobj\n".encode()
    x = len(out)
    out += f"xref\n0 {len(objs) + 1}\n0000000000 65535 f \n".encode()
    for o in offs:
        out += f"{o:010d} 00000 n \n".encode()
    out += f"trailer\n<< /Size {len(objs) + 1} /Root 1 0 R >>\nstartxref\n{x}\n%%EOF\n".encode()
    return out


def _fake_result(timed_out=False):
    page = SimpleNamespace(page_no=1, parsed_page=None)
    document = SimpleNamespace(
        version="fake-1.0.0",
        pages={1: SimpleNamespace(size=SimpleNamespace(width=100.0, height=100.0))},
    )
    return SimpleNamespace(
        document=document, pages=[page], has_timeout_errors=lambda: timed_out
    )


def _fake_converter(convert_fn):
    return SimpleNamespace(convert=convert_fn)


_warmed: set[int] = set()


@pytest.fixture
def warm_converter():
    """The real shared converter with its pipeline loaded: building it does not load the
    models, the first conversion does (minutes cold), so convert once here, not in a test."""
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    converter = convert.get_converter()
    if id(converter) not in _warmed:
        try:
            convert.stub_read((TESTDATA / "native_3page.pdf").read_bytes(), "application/pdf")
        except convert.DocumentUnreadable:
            pass  # a time-limit under load still leaves the pipeline loaded
        _warmed.add(id(converter))
    return converter


@pytest.fixture
def client(sentry_capture):
    # Production inits Sentry before it builds the app; reload so zero-event rows run that app.
    importlib.reload(app_module)
    return TestClient(app_module.app)


def test_a_conversion_past_the_time_limit_is_422(client, sentry_capture, monkeypatch):
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    monkeypatch.setattr(convert, "DOCUMENT_TIMEOUT_S", 0.001)
    # monkeypatch restores the warm converter at teardown, so later files never see this one.
    monkeypatch.setattr(convert, "_converter", None)
    monkeypatch.setattr(convert, "_construction_count", 0)
    # Only docling's own document_timeout may stop this read.
    monkeypatch.setattr(convert, "_stop_check", lambda stop, started: (lambda event: None))

    resp = client.post("/v1/read", content=(TESTDATA / "native_3page.pdf").read_bytes(), headers=PDF)

    assert resp.status_code == 422
    assert resp.json()["reason"] == "time_limit"
    assert "limit" in resp.json()["error"]
    assert sentry_capture.events() == []


def test_stub_read_raises_on_a_timed_out_result(monkeypatch, caplog):
    monkeypatch.setattr(
        convert, "get_converter", lambda: _fake_converter(lambda s, **kw: _fake_result(True))
    )

    with caplog.at_level("WARNING", logger="convert"), pytest.raises(
        convert.DocumentUnreadable
    ) as err:
        convert.stub_read(b"%PDF-1.4\nx", "application/pdf")

    assert err.value.reason == "time_limit"
    # the 240 s PM limit is the one the stop reports
    assert any("document limit 240 s" in r.getMessage() for r in caplog.records)


def test_a_stop_logs_no_sentry_event(client, sentry_capture, monkeypatch):
    import logging

    def convert_fn(stream, progress_callback=None, **kw):
        logging.getLogger("docling.pipeline.standard_pdf_pipeline").error(
            "Stage layout stopped during its model call"
        )
        time.sleep(0.05)
        progress_callback(None)
        return _fake_result()

    monkeypatch.setattr(convert, "get_converter", lambda: _fake_converter(convert_fn))

    resp = client.post(
        "/v1/read", content=b"%PDF-1.4\nx", headers={**PDF, "X-Read-Deadline-Ms": "1"}
    )

    assert resp.status_code == 422
    assert resp.json()["reason"] == "time_limit"
    assert sentry_capture.events() == []
    assert not [l for l in sentry_capture.logs() if "model call" in str(l.get("body"))]


def test_a_passed_caller_deadline_stops_the_real_conversion(client, warm_converter):
    body = (TESTDATA / "native_3page.pdf").read_bytes()

    stopped = client.post("/v1/read", content=body, headers={**PDF, "X-Read-Deadline-Ms": "1"})
    after = client.post("/v1/read", content=body, headers=PDF)

    assert stopped.status_code == 422
    assert stopped.json()["reason"] == "time_limit"
    assert after.status_code == 200
    assert len(after.json()["pages"]) == 3


def test_a_stop_on_a_cold_pipeline_leaves_the_converter_usable(client, monkeypatch):
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    # A fresh converter has loaded no models; monkeypatch restores the shared one at teardown.
    monkeypatch.setattr(convert, "_converter", None)
    monkeypatch.setattr(convert, "_construction_count", 0)
    body = (TESTDATA / "native_3page.pdf").read_bytes()

    stopped = client.post("/v1/read", content=body, headers={**PDF, "X-Read-Deadline-Ms": "1"})
    after = client.post("/v1/read", content=body, headers=PDF)

    assert stopped.status_code == 422
    assert stopped.json()["reason"] == "time_limit"
    assert after.status_code == 200
    assert len(after.json()["pages"]) == 3
    assert convert.construction_count() == 1, "the stop rebuilt the converter"


def test_a_deadline_stop_answers_inside_the_margin(client, warm_converter):
    started = time.monotonic()
    resp = client.post(
        "/v1/read", content=npage(800), headers={**PDF, "X-Read-Deadline-Ms": "5000"}
    )
    elapsed = time.monotonic() - started

    assert resp.status_code == 422
    assert resp.json()["reason"] == "time_limit"
    assert elapsed < 5 + ANSWER_MARGIN_S


def test_stopping_one_conversion_leaves_a_concurrent_one_intact(warm_converter):
    runs = {}

    def run(label, data, deadline):
        def body():
            convert.read_stop.set(convert.ReadStop(deadline, threading.Event()))
            runs[label] = {"start": time.monotonic()}
            try:
                runs[label]["result"] = convert.stub_read(data, "application/pdf")
            except convert.DocumentUnreadable as exc:
                runs[label]["error"] = exc
            runs[label]["end"] = time.monotonic()

        contextvars.copy_context().run(body)

    a = threading.Thread(target=run, args=("A", npage(800), time.monotonic() + 3))
    b = threading.Thread(target=run, args=("B", npage(20), None))
    a.start()
    b.start()
    a.join(300)
    b.join(300)

    assert runs["A"]["error"].reason == "time_limit"
    assert "error" not in runs["B"]
    assert len(runs["B"]["result"]["pages"]) == 20
    assert runs["B"]["start"] < runs["A"]["end"] < runs["B"]["end"], "the runs did not overlap"
    after = convert.stub_read((TESTDATA / "native_3page.pdf").read_bytes(), "application/pdf")
    assert len(after["pages"]) == 3


def test_the_stop_check_raises_only_once_due():
    now = time.monotonic()

    convert._stop_check(convert.ReadStop(now + 60, threading.Event()), now)(None)
    with pytest.raises(convert._Stopped):
        convert._stop_check(convert.ReadStop(now - 1, threading.Event()), now)(None)
    gone = threading.Event()
    gone.set()
    with pytest.raises(convert._Stopped):
        convert._stop_check(convert.ReadStop(None, gone), now)(None)
    with pytest.raises(convert._Stopped):
        convert._stop_check(
            convert.ReadStop(None, threading.Event()), now - convert.DOCUMENT_TIMEOUT_S - 1
        )(None)


def test_a_disconnected_caller_stops_the_conversion(monkeypatch):
    stopped = threading.Event()
    running = threading.Event()

    def convert_fn(stream, progress_callback=None, **kw):
        running.set()
        end = time.monotonic() + 20
        while time.monotonic() < end:
            try:
                progress_callback(None)
            except convert._Stopped:
                stopped.set()
                raise
            time.sleep(0.05)
        return _fake_result()

    monkeypatch.setattr(convert, "get_converter", lambda: _fake_converter(convert_fn))
    server = uvicorn.Server(uvicorn.Config(app_module.app, host="127.0.0.1", port=0, log_level="warning"))
    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()
    try:
        deadline = time.monotonic() + 10
        while not server.started and time.monotonic() < deadline:
            time.sleep(0.05)
        port = server.servers[0].sockets[0].getsockname()[1]
        body = b"%PDF-1.4\nx"
        with socket.create_connection(("127.0.0.1", port)) as sock:
            sock.sendall(
                b"POST /v1/read HTTP/1.1\r\nHost: x\r\nContent-Type: application/pdf\r\n"
                + f"Content-Length: {len(body)}\r\n\r\n".encode()
                + body
            )
            assert running.wait(5)
            time.sleep(0.5)
        assert stopped.wait(3)
    finally:
        server.should_exit = True
        thread.join(10)


@pytest.mark.parametrize("value", ["abc", "0", "-5", None])
def test_a_bad_deadline_header_is_ignored(client, monkeypatch, value):
    seen = []

    def convert_fn(stream, **kw):
        seen.append(convert.read_stop.get())
        return _fake_result()

    monkeypatch.setattr(convert, "get_converter", lambda: _fake_converter(convert_fn))
    headers = dict(PDF) if value is None else {**PDF, "X-Read-Deadline-Ms": value}

    resp = client.post("/v1/read", content=b"%PDF-1.4\nx", headers=headers)

    assert resp.status_code == 200
    assert seen[0].deadline is None


def test_a_pdf_over_the_page_limit_is_422(client, warm_converter, monkeypatch):
    body = (TESTDATA / "native_3page.pdf").read_bytes()

    monkeypatch.setattr(convert, "MAX_PAGES", 2)
    over = client.post("/v1/read", content=body, headers=PDF)
    monkeypatch.setattr(convert, "MAX_PAGES", 3)
    at = client.post("/v1/read", content=body, headers=PDF)

    assert over.status_code == 422
    assert at.status_code == 200
    assert len(at.json()["pages"]) == 3


def _spy_converter(monkeypatch):
    calls = []
    real = convert.get_converter

    def spy():
        calls.append(1)
        return real()

    monkeypatch.setattr(convert, "get_converter", spy)
    return calls


def test_a_docx_over_the_xml_bound_is_422_before_conversion(client, warm_converter, monkeypatch):
    body = (FIXTURES / "table_invoice.docx").read_bytes()
    calls = _spy_converter(monkeypatch)

    monkeypatch.setattr(convert, "DOCX_XML_MAX_BYTES", 1000)
    over = client.post("/v1/read", content=body, headers=DOCX)
    assert over.status_code == 422
    assert "limit" in over.json()["error"]
    assert calls == []

    monkeypatch.undo()
    assert client.post("/v1/read", content=body, headers=DOCX).status_code == 200


def test_a_declared_xml_size_counts_every_xml_part(client, monkeypatch):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        zf.writestr("word/document.xml", b" " * (5 * 1024 * 1024))
        zf.writestr("word/styles.xml", b" " * (4 * 1024 * 1024))
    calls = _spy_converter(monkeypatch)

    resp = client.post("/v1/read", content=buf.getvalue(), headers=DOCX)

    assert resp.status_code == 422
    assert calls == []
