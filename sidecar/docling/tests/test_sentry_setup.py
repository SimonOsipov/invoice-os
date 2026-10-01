"""SENTRY-05-02: Sentry starts from SENTRY_DSN with the Go labels (internal/platform/config.go)."""

import importlib

import pytest
import sentry_sdk
from sentry_capture import CapturingTransport

import app
import buildinfo
import convert
import sentry_setup

FAKE_DSN = "https://public@o0.ingest.sentry.io/1"


def _unbind():
    sentry_sdk.get_client().close()
    sentry_sdk.get_global_scope().set_client(None)


def test_no_dsn_means_off(monkeypatch):
    assert sentry_setup.sentry_options({}) is None
    assert sentry_setup.sentry_options({"SENTRY_DSN": ""}) is None
    assert sentry_setup.sentry_options({"SENTRY_DSN": FAKE_DSN}) is not None

    monkeypatch.delenv("SENTRY_DSN", raising=False)
    try:
        sentry_setup.init_sentry()
        assert sentry_setup.sentry_state() == "off"
        assert sentry_sdk.get_client().dsn is None
    finally:
        _unbind()


@pytest.mark.parametrize(
    ("env", "want"),
    [
        ({"RAILWAY_ENVIRONMENT_NAME": "production", "ENVIRONMENT": "development"}, "production"),
        ({"RAILWAY_ENVIRONMENT_NAME": "", "ENVIRONMENT": "staging"}, "staging"),
        ({"ENVIRONMENT": "staging"}, "staging"),
        ({"RAILWAY_ENVIRONMENT_NAME": "", "ENVIRONMENT": ""}, "development"),
        ({}, "development"),
    ],
)
def test_environment_from_railway_then_environment_then_default(env, want):
    opts = sentry_setup.sentry_options({"SENTRY_DSN": FAKE_DSN, **env})
    assert opts is not None
    assert opts.get("environment") == want


@pytest.mark.parametrize(
    ("build", "railway", "want"),
    [
        ("abc123", "", "abc123"),
        ("abc123", "f00", "abc123"),
        ("dev", "f00", "unstamped-f00"),
        ("", "f00", "unstamped-f00"),
        ("dev", "", "unstamped"),
        ("", "", "unstamped"),
    ],
)
def test_release_name(build, railway, want):
    assert sentry_setup.release_name(build, railway) == want


def test_release_comes_from_the_build_file(monkeypatch, tmp_path):
    build_file = tmp_path / "build.txt"
    build_file.write_text("abc123\n")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    opts = sentry_setup.sentry_options({"SENTRY_DSN": FAKE_DSN})
    assert opts is not None
    assert opts.get("release") == "abc123"

    monkeypatch.setattr(buildinfo, "BUILD_FILE", tmp_path / "missing.txt")
    opts = sentry_setup.sentry_options({"SENTRY_DSN": FAKE_DSN, "RAILWAY_GIT_COMMIT_SHA": "f00"})
    assert opts is not None
    assert opts.get("release") == "unstamped-f00"


def test_events_carry_the_go_labels(monkeypatch, tmp_path):
    build_file = tmp_path / "build.txt"
    build_file.write_text("abc123\n")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    transport = CapturingTransport()
    options = sentry_setup.sentry_options(
        {"SENTRY_DSN": FAKE_DSN, "RAILWAY_ENVIRONMENT_NAME": "production"}
    )
    assert options is not None
    options.setdefault("trace_propagation_targets", [])
    try:
        sentry_sdk.init(**options, transport=transport)
        sentry_sdk.capture_exception(RuntimeError("x"))
        events = transport.events()
    finally:
        _unbind()
    assert sentry_setup.sentry_state() == "off"
    assert len(events) == 1
    assert events[0].get("environment") == "production"
    assert events[0].get("release") == "abc123"
    assert events[0].get("server_name") == "docling"


def test_init_runs_before_warm_up(monkeypatch):
    calls = []
    monkeypatch.setattr(sentry_setup, "init_sentry", lambda: calls.append("init_sentry"))
    monkeypatch.setattr(convert, "start_warm_up", lambda: calls.append("start_warm_up"))
    try:
        importlib.reload(app)
        order = list(calls)
    finally:
        monkeypatch.undo()
        importlib.reload(app)
    assert order == ["init_sentry", "start_warm_up"]


def test_boot_failure_after_init_opens_one_issue_and_reraises(sentry_capture):
    def boom():
        raise RuntimeError("boot")

    try:
        with pytest.MonkeyPatch.context() as mp:
            mp.setattr(sentry_setup, "init_sentry", lambda: None)
            mp.setattr(convert, "start_warm_up", boom)
            with pytest.raises(RuntimeError):
                importlib.reload(app)
            events = sentry_capture.events()
    finally:
        importlib.reload(app)
    assert len(events) == 1
    assert events[0]["exception"]["values"][-1]["type"] == "RuntimeError"
