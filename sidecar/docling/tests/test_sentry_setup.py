"""Sentry starts from SENTRY_DSN with the Go labels (internal/platform/config.go)."""

import importlib
import logging
import uuid

import pytest
import sentry_sdk
from sentry_capture import CapturingTransport
from sentry_sdk.integrations.logging import LoggingIntegration
from sentry_sdk.utils import BadDsn
from starlette.requests import ClientDisconnect

import app
import buildinfo
import convert
import sentry_setup
import sentryfilter

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
        ({"RAILWAY_ENVIRONMENT_NAME": "production"}, "production"),
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

    # Stamped build wins over the Railway sha; a "dev" or empty file falls back to it.
    for stamp, railway, want in [
        ("abc123\n", "f00", "abc123"),
        ("dev\n", "f00", "unstamped-f00"),
        ("\n", "f00", "unstamped-f00"),
        ("dev\n", "", "unstamped"),
    ]:
        build_file.write_text(stamp)
        monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
        opts = sentry_setup.sentry_options(
            {"SENTRY_DSN": FAKE_DSN, "RAILWAY_GIT_COMMIT_SHA": railway}
        )
        assert opts is not None
        assert opts.get("release") == want


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


def test_options_carry_every_privacy_and_label_setting():
    opts = sentry_setup.sentry_options({"SENTRY_DSN": FAKE_DSN})
    assert opts is not None
    exact = {
        "dsn": FAKE_DSN,
        "server_name": "docling",
        "send_default_pii": False,
        "include_local_variables": False,
        "max_request_body_size": "never",
        "enable_logs": True,
        "trace_propagation_targets": [],
        "ignore_errors": [ClientDisconnect],
    }
    for key, want in exact.items():
        assert key in opts, key
        assert opts[key] == want and type(opts[key]) is type(want), key
    hooks = {
        "before_send": sentryfilter.scrub_event,
        "before_send_transaction": sentryfilter.scrub_transaction,
        "before_send_log": sentryfilter.scrub_log,
        "before_breadcrumb": sentryfilter.keep_breadcrumb,
        "traces_sampler": sentry_setup.traces_sampler,
    }
    for key, want in hooks.items():
        assert opts.get(key) is want, key
    assert len(opts["integrations"]) == 1
    assert isinstance(opts["integrations"][0], LoggingIntegration)
    assert set(opts) == set(exact) | set(hooks) | {"environment", "release", "integrations"}


def test_error_log_opens_no_issue_but_reaches_sentry_logs(sentry_capture):
    logging.getLogger("app").error("note")
    assert [log["body"] for log in sentry_capture.logs()] == ["note"]
    assert sentry_capture.events() == []
    sentry_sdk.capture_exception(RuntimeError("control"))
    assert len(sentry_capture.events()) == 1


def test_client_disconnect_is_ignored_but_other_errors_are_captured(sentry_capture):
    sentry_sdk.capture_exception(ClientDisconnect())
    assert sentry_capture.events() == []
    sentry_sdk.capture_exception(RuntimeError("control"))
    assert len(sentry_capture.events()) == 1


def test_frame_locals_are_not_attached(sentry_capture):
    marker = uuid.uuid4().hex  # runtime value: no source line of this test holds it

    def read_document():
        body = marker
        raise RuntimeError(len(body))

    try:
        read_document()
    except RuntimeError:
        sentry_sdk.capture_exception()
    assert len(sentry_capture.events()) == 1
    assert marker.encode() not in sentry_capture.raw()


def test_scrub_hooks_run_on_events_transactions_logs_and_breadcrumbs(sentry_capture):
    sentry_sdk.set_user({"id": "u-1"})
    logging.getLogger("thirdparty").warning("third")
    logging.getLogger("app").warning("own")
    sentry_sdk.capture_exception(RuntimeError("x"))
    with sentry_sdk.start_transaction(name="t"):
        pass
    events, transactions = sentry_capture.events(), sentry_capture.transactions()
    assert len(events) == 1 and len(transactions) == 1
    assert "user" not in events[0] and "user" not in transactions[0]
    assert [log["body"] for log in sentry_capture.logs()] == ["own"]
    categories = {c.get("category") for c in sentry_capture.breadcrumbs()}
    assert "app" in categories and "thirdparty" not in categories


@pytest.mark.parametrize("environment", ["pr-12", "staging"])
def test_event_environment_follows_railway_not_the_sdk_default(environment):
    transport = CapturingTransport()
    options = sentry_setup.sentry_options(
        {"SENTRY_DSN": FAKE_DSN, "RAILWAY_ENVIRONMENT_NAME": environment}
    )
    assert options is not None
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    try:
        sentry_sdk.init(**options, transport=transport)
        sentry_sdk.capture_exception(RuntimeError("x"))
        events = transport.events()
    finally:
        _unbind()
    assert [e.get("environment") for e in events] == [environment]


@pytest.mark.parametrize(
    ("path", "want"),
    [
        ("/healthz", 0),
        ("/readyz", 0),
        ("/healthz/deep", 0),
        ("/healthz/", 0),
        ("/v1/read", 1.0),
        ("/healthzx", 1.0),
        ("/readyz/x", 1.0),
        ("/", 1.0),
        ("", 1.0),
    ],
)
def test_traces_sampler_skips_probe_paths_whatever_the_inbound_flag(path, want):
    for inbound in (None, True, False):
        got = sentry_setup.traces_sampler({"asgi_scope": {"path": path}, "parent_sampled": inbound})
        assert got == want and not isinstance(got, bool)


def test_traces_sampler_without_an_asgi_scope_samples():
    assert sentry_setup.traces_sampler({}) == 1.0
    assert sentry_setup.traces_sampler({"asgi_scope": None}) == 1.0


def test_init_sentry_starts_the_client_from_the_environment(monkeypatch):
    started = []
    monkeypatch.setattr(sentry_sdk, "init", lambda **kw: started.append(kw))
    monkeypatch.setenv("SENTRY_DSN", FAKE_DSN)
    monkeypatch.setenv("RAILWAY_ENVIRONMENT_NAME", "production")
    sentry_setup.init_sentry()
    assert len(started) == 1
    assert started[0]["dsn"] == FAKE_DSN
    assert started[0]["environment"] == "production"
    assert started[0]["server_name"] == "docling"

    monkeypatch.setenv("SENTRY_DSN", "")
    sentry_setup.init_sentry()
    assert len(started) == 1


def test_malformed_dsn_raises_at_init(monkeypatch):
    monkeypatch.setenv("SENTRY_DSN", "not a dsn")
    try:
        with pytest.raises(BadDsn):
            sentry_setup.init_sentry()
        assert sentry_setup.sentry_state() == "off"
    finally:
        _unbind()


def test_state_is_off_for_a_bound_client_without_a_dsn():
    try:
        sentry_sdk.init(dsn="", transport=CapturingTransport())
        assert sentry_sdk.get_client().is_active()
        assert sentry_setup.sentry_state() == "off"
    finally:
        _unbind()


def test_state_is_on_for_a_client_with_a_dsn(sentry_capture):
    assert sentry_setup.sentry_state() == "on"


def _record_sdk_calls(monkeypatch):
    calls = []
    for name in ("capture_exception", "flush"):
        real = getattr(sentry_sdk, name)

        def wrapper(*a, _name=name, _real=real, **kw):
            calls.append(_name)
            return _real(*a, **kw)

        monkeypatch.setattr(sentry_sdk, name, wrapper)
    return calls


def test_boot_guard_captures_once_flushes_then_reraises_the_same_object(
    sentry_capture, monkeypatch
):
    calls = _record_sdk_calls(monkeypatch)
    exc = ValueError("v")
    with pytest.raises(ValueError) as caught, sentry_setup.boot_guard():
        raise exc
    assert caught.value is exc
    assert calls == ["capture_exception", "flush"]
    events = sentry_capture.events()
    assert len(events) == 1
    assert events[0]["exception"]["values"][-1]["type"] == "ValueError"


def test_boot_guard_is_silent_when_the_body_succeeds(sentry_capture, monkeypatch):
    calls = _record_sdk_calls(monkeypatch)
    ran = []
    with sentry_setup.boot_guard():
        ran.append(1)
    assert ran == [1]
    assert calls == []
    assert sentry_capture.events() == []


@pytest.mark.parametrize("exc_type", [KeyboardInterrupt, SystemExit])
def test_boot_guard_lets_base_exceptions_pass_uncaptured(sentry_capture, exc_type):
    with pytest.raises(exc_type), sentry_setup.boot_guard():
        raise exc_type()
    assert sentry_capture.events() == []
    with pytest.raises(RuntimeError), sentry_setup.boot_guard():
        raise RuntimeError("control")
    assert len(sentry_capture.events()) == 1


def test_boot_guard_reraises_when_no_client_is_bound():
    assert sentry_setup.sentry_state() == "off"
    exc = RuntimeError("no client")
    with pytest.raises(RuntimeError) as caught, sentry_setup.boot_guard():
        raise exc
    assert caught.value is exc


@pytest.fixture
def boot(monkeypatch, tmp_path):
    """init_sentry() from the environment, with a CapturingTransport injected into sentry_sdk.init."""
    build_file = tmp_path / "build.txt"
    build_file.write_text("abc123\n")
    monkeypatch.setattr(buildinfo, "BUILD_FILE", build_file)
    for key in ("ENVIRONMENT", "RAILWAY_GIT_COMMIT_SHA"):
        monkeypatch.delenv(key, raising=False)
    monkeypatch.setenv("RAILWAY_ENVIRONMENT_NAME", "production")
    if convert._warmup_thread is not None:
        convert._warmup_thread.join()
    real_init = sentry_sdk.init
    init_calls = []

    def run(dsn=FAKE_DSN, flag=None):
        monkeypatch.setenv("SENTRY_DSN", dsn)
        if flag is None:
            monkeypatch.delenv("SENTRY_TEST_EVENT", raising=False)
        else:
            monkeypatch.setenv("SENTRY_TEST_EVENT", flag)
        transport = CapturingTransport()

        def init_with_transport(**kw):
            init_calls.append(kw)
            return real_init(**kw, transport=transport)

        monkeypatch.setattr(sentry_sdk, "init", init_with_transport)
        sentry_setup.init_sentry()
        return transport

    run.init_calls = init_calls
    try:
        yield run
    finally:
        _unbind()
        sentry_sdk.get_isolation_scope().clear()
        sentry_sdk.get_current_scope().clear()


def test_test_event_sends_one_labelled_event(boot):
    events = boot(flag="true").events()
    assert len(events) == 1
    event = events[0]
    assert event.get("server_name") == "docling"
    assert event.get("environment") == "production"
    assert event.get("release") == "abc123"
    assert event.get("fingerprint") == ["sentry-test-event", "docling"]
    # Boot has no request in flight; Go's test event carries none either.
    assert not event.get("request")


def test_test_event_has_a_readable_stack_without_locals(boot):
    events = boot(flag="true").events()
    assert len(events) == 1
    exceptions = events[0]["exception"]["values"]
    assert len(exceptions) >= 1
    exc = exceptions[-1]
    assert exc["type"] == "RuntimeError"
    assert exc["value"] == "sentry test event"
    frames = exc["stacktrace"]["frames"]
    assert len(frames) >= 1
    assert any(f.get("filename", "").endswith("sentry_setup.py") for f in frames)
    assert all("vars" not in f for f in frames)


@pytest.mark.parametrize("flag", [None, "", "false", "1", "True", " true"])
def test_test_event_fires_only_on_exact_true(boot, flag):
    assert boot(flag=flag).events() == []
    _unbind()
    # Control: the same boot with exactly "true" sends one, so the zero above is not vacuous.
    assert len(boot(flag="true").events()) == 1


def test_test_event_without_dsn_is_silent(boot):
    boot(dsn="", flag="true")
    assert sentry_setup.sentry_state() == "off"
    assert boot.init_calls == []
    # Control: the same flag with a DSN sends one.
    assert len(boot(flag="true").events()) == 1
    assert len(boot.init_calls) == 1


def test_test_event_fingerprint_does_not_stick(boot):
    transport = boot(flag="true")
    sentry_sdk.capture_exception(ValueError("later"))
    events = transport.events()
    assert len(events) == 2
    tagged = [e for e in events if "sentry-test-event" in (e.get("fingerprint") or [])]
    assert len(tagged) == 1
    later = [e for e in events if e["exception"]["values"][-1]["value"] == "later"]
    assert len(later) == 1
    assert "sentry-test-event" not in (later[0].get("fingerprint") or [])
