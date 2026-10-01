"""In-memory Sentry transport: what the HTTP transport would send, as parsed envelopes."""

import threading

import sentry_sdk
from sentry_sdk.transport import Transport


class CapturingTransport(Transport):
    def __init__(self, options=None):
        super().__init__(options)
        self._lock = threading.Lock()
        self._envelopes = []

    def capture_envelope(self, envelope):
        with self._lock:
            self._envelopes.append(envelope)

    def flush(self, timeout, callback=None):
        return None

    def _items(self, kind):
        sentry_sdk.flush()
        with self._lock:
            envelopes = list(self._envelopes)
        return [i for e in envelopes for i in e.items if i.type == kind and i.payload.json]

    def events(self):
        return [i.payload.json for i in self._items("event")]

    def transactions(self):
        return [i.payload.json for i in self._items("transaction")]

    def logs(self):
        return [log for i in self._items("log") for log in i.payload.json["items"]]

    def breadcrumbs(self):
        found = []
        for payload in self.events() + self.transactions():
            found.extend((payload.get("breadcrumbs") or {}).get("values") or [])
        return found

    def transaction_trace_headers(self):
        sentry_sdk.flush()
        with self._lock:
            envelopes = list(self._envelopes)
        return [
            e.headers["trace"]
            for e in envelopes
            if any(i.type == "transaction" for i in e.items) and "trace" in e.headers
        ]

    def raw(self):
        sentry_sdk.flush()
        with self._lock:
            return b"".join(e.serialize() for e in self._envelopes)
