"""Sentry start-up for the sidecar; mirrors internal/platform/sentry.go."""

import os
from contextlib import contextmanager

import sentry_sdk
from sentry_sdk.integrations.logging import LoggingIntegration
from starlette.requests import ClientDisconnect

import buildinfo
import sentryfilter


def release_name(build_sha, railway_sha):
    if build_sha not in ("", "dev"):
        return build_sha
    if railway_sha:
        return f"unstamped-{railway_sha}"
    return "unstamped"


def _first(env, *keys, default=""):
    for key in keys:
        if env.get(key):
            return env[key]
    return default


def _is_probe(path):
    return path in ("/healthz", "/readyz") or path.startswith("/healthz/")


def traces_sampler(sampling_context):
    # Ignores the inbound sampled flag, as Go resets it (D-7).
    scope = sampling_context.get("asgi_scope") or {}
    return 0 if _is_probe(scope.get("path", "")) else 1.0


def sentry_options(env):
    dsn = env.get("SENTRY_DSN", "")
    if not dsn:
        return None
    return {
        "dsn": dsn,
        "environment": _first(
            env, "RAILWAY_ENVIRONMENT_NAME", "ENVIRONMENT", default="development"
        ),
        "release": release_name(
            buildinfo.read_build_sha(buildinfo.BUILD_FILE),
            env.get("RAILWAY_GIT_COMMIT_SHA", ""),
        ),
        "server_name": "docling",
        "send_default_pii": False,
        "include_local_variables": False,
        "max_request_body_size": "never",
        "enable_logs": True,
        "integrations": [LoggingIntegration(event_level=None)],
        "ignore_errors": [ClientDisconnect],
        "trace_propagation_targets": [],
        "traces_sampler": traces_sampler,
        "before_send": sentryfilter.scrub_event,
        "before_send_transaction": sentryfilter.scrub_transaction,
        "before_send_log": sentryfilter.scrub_log,
        "before_breadcrumb": sentryfilter.keep_breadcrumb,
    }


def init_sentry():
    opts = sentry_options(os.environ)
    if opts is not None:
        sentry_sdk.init(**opts)


def sentry_state():
    return "on" if sentry_sdk.get_client().dsn else "off"


@contextmanager
def boot_guard():
    try:
        yield
    except Exception:
        sentry_sdk.capture_exception()
        sentry_sdk.flush()
        raise
