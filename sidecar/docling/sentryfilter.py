"""Scrub rules for the sidecar's Sentry hooks; mirrors internal/platform/sentryfilter.go."""

SIDECAR_LOGGERS = frozenset(
    {"app", "convert", "geometry", "uvicorn", "uvicorn.error", "uvicorn.access"}
)


def scrub_text(s):
    return s


def scrub_event(event, hint=None):
    return event


def scrub_transaction(event, hint=None):
    return event


def scrub_log(log, hint=None):
    return log


def keep_breadcrumb(crumb, hint=None):
    return crumb
