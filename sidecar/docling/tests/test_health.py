"""T-01-1..T-01-3: /healthz's two-key body and its promise to never touch a model.
T-03-14: /healthz stays fast while a conversion (or the converter's own lazy build) is in flight.
"""

import asyncio
import threading
import time

import httpx
import pytest
from fastapi.testclient import TestClient

import buildinfo
import convert
from app import app


@pytest.fixture
def client():
    return TestClient(app)


def test_t01_1_healthz_reports_dev_when_no_build_file(client, monkeypatch, tmp_path):
    missing = tmp_path / "build.txt"
    monkeypatch.setattr(buildinfo, "BUILD_FILE", missing)
    resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok", "build": "dev"}
    assert set(resp.json().keys()) == {"status", "build"}  # exactly two keys


def test_t01_2_healthz_reports_stripped_build_sha(client, monkeypatch, tmp_path):
    build_file = tmp_path / "build.txt"
    build_file.write_text("abc123\n")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json()["build"] == "abc123"


def test_t01_3_healthz_never_constructs_converter(client):
    before = convert.construction_count()
    client.get("/healthz")
    assert convert.construction_count() == before == 0


def test_healthz_key_set_is_exact_when_build_file_present(client, monkeypatch, tmp_path):
    # T-01-1 only checks the exact-two-keys case for an absent file; this covers present.
    build_file = tmp_path / "build.txt"
    build_file.write_text("abc123\n")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    resp = client.get("/healthz")
    assert set(resp.json().keys()) == {"status", "build"}


def test_healthz_reports_empty_build_when_file_is_empty(client, monkeypatch, tmp_path):
    # An empty (not absent) file is a third case the AC text doesn't name.
    build_file = tmp_path / "build.txt"
    build_file.write_text("")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json()["build"] == ""


def test_healthz_reports_empty_build_when_file_is_whitespace_only(client, monkeypatch, tmp_path):
    build_file = tmp_path / "build.txt"
    build_file.write_text("   \n\t  ")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    resp = client.get("/healthz")
    assert resp.json()["build"] == ""


def test_healthz_strips_crlf_build_sha(client, monkeypatch, tmp_path):
    # A Windows-authored stamp file uses \r\n; strip() must eat the \r too.
    build_file = tmp_path / "build.txt"
    build_file.write_bytes(b"abc123\r\n")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    resp = client.get("/healthz")
    assert resp.json()["build"] == "abc123"


def test_healthz_reports_dev_when_build_file_is_unreadable(client, monkeypatch, tmp_path):
    # Carried over from EXTR-03-01 QA: an unreadable file must not 500 the liveness probe.
    build_file = tmp_path / "build.txt"
    build_file.write_text("abc123\n")
    build_file.chmod(0o000)
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    try:
        resp = client.get("/healthz")
        assert resp.status_code == 200
        assert resp.json()["build"] == "dev"
    finally:
        build_file.chmod(0o644)  # tmp_path cleanup needs read/write back


def test_t03_14_healthz_stays_fast_while_a_read_is_in_flight(monkeypatch):
    # Real concurrency, not a sleep standing in for it: both requests run as asyncio Tasks
    # on the SAME loop via httpx.AsyncClient(ASGITransport), gathered together, and each
    # measures its own completion time from one shared start. A synchronous, blocking call
    # inside read_document's `async def` -- today's shape, convert.stub_read called inline --
    # starves the loop, so /healthz's elapsed time reveals it whether or not it touches
    # anything docling-specific.
    orig_stub_read = convert.stub_read

    def slow_stub_read(body, content_type):
        time.sleep(2)
        return orig_stub_read(body, content_type)

    monkeypatch.setattr(convert, "stub_read", slow_stub_read)

    async def scenario():
        transport = httpx.ASGITransport(app=app)
        async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
            t_start = time.time()

            async def call_read():
                resp = await client.post(
                    "/v1/read", content=b"%PDF-1.4\nx", headers={"content-type": "application/pdf"}
                )
                return resp, time.time() - t_start

            async def call_healthz():
                resp = await client.get("/healthz")
                return resp, time.time() - t_start

            return await asyncio.gather(call_read(), call_healthz())

    (read_resp, read_elapsed), (healthz_resp, healthz_elapsed) = asyncio.run(scenario())

    assert read_resp.status_code == 200
    assert read_elapsed >= 1.9, (
        f"the stubbed read only took {read_elapsed:.2f}s -- the slow path was never exercised"
    )
    assert healthz_resp.status_code == 200
    assert healthz_elapsed < 1.0, (
        f"/healthz took {healthz_elapsed:.2f}s while a read was in flight, want <1s "
        f"(probeTimeout = 3s, internal/gateway/fleet.go:22)"
    )


def test_healthz_independent_of_the_converter_construction_lock(monkeypatch):
    # The other half of T-03-14's Given: a cold converter still loading. get_converter()'s
    # lock is real (wired in EXTR-03-03's Mode-A scaffolding); /healthz never acquires it,
    # by construction -- this guards that independence directly, with a genuine background
    # thread holding the lock for 2 real seconds, not a mocked delay.
    convert._converter = None
    convert._construction_count = 0

    def slow_construct():
        time.sleep(2)
        return object()

    monkeypatch.setattr(convert, "_construct_converter", slow_construct)

    builder = threading.Thread(target=convert.get_converter)
    builder.start()
    time.sleep(0.1)  # let the builder thread acquire the lock before measuring

    client = TestClient(app)
    t0 = time.time()
    resp = client.get("/healthz")
    elapsed = time.time() - t0
    builder.join(timeout=5)

    assert resp.status_code == 200
    assert elapsed < 1.0, f"/healthz took {elapsed:.2f}s while the converter lock was held"
