"""T-03-8, T-03-9: the converter is built on first use, once, and a second concurrent
caller waits on the lock rather than getting a 503 (D-17).

Needs docling importable -- run only via the Docker `test` stage. /v1/read now calls the real
convert.get_converter() (EXTR-03-03), so construction_count() reaches 1 on first use.
"""

import concurrent.futures
import time
from types import SimpleNamespace

import pytest
from fastapi.testclient import TestClient

import convert
from app import app

PDF_CONTENT_TYPE = "application/pdf"


def _fake_convert(stream):
    """Minimal stand-in for a real ConversionResult -- just enough shape for
    convert._to_wire_contract to build a response without needing the real (multi-second)
    DocumentConverter build. Used by T-03-9's mock so the test can assert its actual
    discriminator (both requests 200), not just "neither is a 503".
    """
    page = SimpleNamespace(page_no=1, parsed_page=None)
    document = SimpleNamespace(
        version="fake-1.0.0",
        pages={1: SimpleNamespace(size=SimpleNamespace(width=100.0, height=100.0))},
    )
    return SimpleNamespace(document=document, pages=[page])


@pytest.fixture
def client():
    return TestClient(app)


@pytest.fixture(autouse=True)
def reset_converter_state():
    # _converter/_construction_count are module-level; tests must not see each other's builds.
    # Join the boot-time warm-up thread first -- otherwise it can finish and increment the
    # counter *after* this reset, racing test_t03_8's "before == 0" non-deterministically.
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    convert._converter = None
    convert._construction_count = 0
    yield
    convert._converter = None
    convert._construction_count = 0


def test_t03_8_converter_built_once_lazily(client):
    assert convert.construction_count() == 0

    client.post("/v1/read", content=b"%PDF-1.4\nx", headers={"content-type": PDF_CONTENT_TYPE})
    assert convert.construction_count() == 1, "first /v1/read must trigger exactly one construction"

    client.post("/v1/read", content=b"%PDF-1.4\nx", headers={"content-type": PDF_CONTENT_TYPE})
    assert convert.construction_count() == 1, "second /v1/read must reuse the cached converter"


def test_t03_9_concurrent_first_requests_share_one_construction_and_both_succeed(
    client, monkeypatch
):
    def slow_construct():
        # A working fake, not a bare object(): the real discriminator is that BOTH concurrent
        # callers get 200 (a 500 would be just as wrong as a 503 here), so the mock must
        # support the same .convert() call read_document actually makes.
        time.sleep(2)
        return SimpleNamespace(convert=_fake_convert)

    monkeypatch.setattr(convert, "_construct_converter", slow_construct)

    def post():
        return client.post(
            "/v1/read", content=b"%PDF-1.4\nx", headers={"content-type": PDF_CONTENT_TYPE}
        )

    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        futures = [pool.submit(post), pool.submit(post)]
        results = [f.result(timeout=10) for f in futures]

    for resp in results:
        # The discriminator: a fail-fast 503 design would show one 200 and one 503. Both
        # 200 means the second request waited on the lock instead of being refused.
        assert resp.status_code == 200

    assert convert.construction_count() == 1, "two concurrent first callers must build exactly once"


def test_request_arriving_during_warmup_shares_the_warmup_construction(monkeypatch):
    # T-03-9 covers two concurrent /v1/read calls; this covers the pairing the boot-time
    # warm-up thread actually creates -- a request landing mid-warm-up, not mid-another-request.
    convert._warmup_thread = None  # force start_warm_up() to spawn a fresh thread

    def slow_construct():
        time.sleep(1)
        return object()

    monkeypatch.setattr(convert, "_construct_converter", slow_construct)

    warmup_thread = convert.start_warm_up()
    time.sleep(0.1)  # let warm-up acquire the lock first
    result = convert.get_converter()  # simulates a request landing mid-warm-up
    warmup_thread.join(timeout=5)

    assert convert.construction_count() == 1, "warm-up + a request during it must build once"
    assert result is convert._converter
