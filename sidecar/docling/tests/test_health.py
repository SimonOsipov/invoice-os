"""T-01-1..T-01-3: /healthz's two-key body and its promise to never touch a model."""

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
