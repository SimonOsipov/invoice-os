"""Sentry start-up for the sidecar; mirrors internal/platform/sentry.go. Stubs until SENTRY-05-02."""

from contextlib import contextmanager


def release_name(build_sha, railway_sha):
    return ""


def sentry_options(env):
    return {"dsn": env.get("SENTRY_DSN", "")}


def init_sentry():
    return None


def sentry_state():
    return "off"


@contextmanager
def boot_guard():
    yield
