"""T-03-8, T-03-9: the converter is built on first use, once, and a second concurrent
caller waits on the lock rather than getting a 503 (D-17).

Needs docling importable -- run only via the Docker `test` stage. Currently red: /v1/read is
still convert.stub_read, which never calls convert.get_converter(), so construction_count()
stays 0 no matter how many requests land -- these assertions expect it to reach 1.
"""

import concurrent.futures
import time

import pytest
from fastapi.testclient import TestClient

import convert
from app import app

PDF_CONTENT_TYPE = "application/pdf"


@pytest.fixture
def client():
    return TestClient(app)


@pytest.fixture(autouse=True)
def reset_converter_state():
    # _converter/_construction_count are module-level; tests must not see each other's builds.
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
        time.sleep(2)
        return object()

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
