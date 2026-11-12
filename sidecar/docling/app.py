"""FastAPI app: GET /healthz, POST /v1/read. Wire contract: EXTR-03 story sec. 3."""

import asyncio
import contextlib
import logging
import threading
import time

import sentry_sdk

import sentry_setup

# First, so a boot failure below has a client to report it.
sentry_setup.init_sentry()

with sentry_setup.boot_guard():
    from fastapi import FastAPI, Request
    from fastapi.responses import JSONResponse

    import buildinfo
    import convert

    logger = logging.getLogger(__name__)

    app = FastAPI()

    # Background warm-up: starts at import, off any request path.
    # test_t01_3_healthz_never_constructs_converter relies on a model build outlasting one round trip.
    # The thread lives in convert.py (not started inline here) so tests can join it deterministically.
    convert.start_warm_up()

# documents.size_bytes CHECKs <= this; matches maxDocumentBytes (cmd/submission/main.go:152).
MAX_DOCUMENT_BYTES = 15 * 1024 * 1024


@app.get("/healthz")
@app.get("/health")  # Cloud Run's front end answers /healthz itself on a run.app host.
async def healthz() -> dict[str, str]:
    """Body is exactly {"status": "ok", "build": "<sha>", "sentry": "on"|"off"} --
    internal/platform/health.go's shape. No lock, no I/O beyond the build file -- stays on the
    event loop (test_t03_14_healthz_stays_fast_while_a_read_is_in_flight).
    """
    return {
        "status": "ok",
        "build": buildinfo.read_build_sha(buildinfo.BUILD_FILE),
        "sentry": sentry_setup.sentry_state(),
    }


async def _read_capped_body(request: Request) -> bytes | None:
    """The body, or None once it exceeds the cap.

    Streamed rather than `await request.body()`: that buffers the whole payload before any
    check, so a chunked upload with no Content-Length costs the memory before the 413.
    """
    declared = request.headers.get("content-length")
    if declared is not None and declared.isdigit() and int(declared) > MAX_DOCUMENT_BYTES:
        return None

    chunks: list[bytes] = []
    total = 0
    async for chunk in request.stream():
        total += len(chunk)
        if total > MAX_DOCUMENT_BYTES:
            return None
        chunks.append(chunk)
    return b"".join(chunks)


def _read_deadline(request: Request) -> float | None:
    """time.monotonic() deadline from X-Read-Deadline-Ms; a missing or bad value is ignored."""
    raw = request.headers.get("x-read-deadline-ms", "")
    if not (raw.isascii() and raw.isdigit()) or int(raw) <= 0:
        return None
    return time.monotonic() + int(raw) / 1000


async def _watch_disconnect(request: Request, event: threading.Event) -> None:
    while not await request.is_disconnected():
        await asyncio.sleep(0.25)
    event.set()


@app.post("/v1/read")
async def read_document(request: Request) -> JSONResponse:
    """Raw document bytes in, the §3 wire contract out.

    convert.stub_read (the real converter, off-thread via asyncio.to_thread) never runs on
    the event loop, so a slow cold-start or a large convert can't starve /healthz (T-03-14).
    """
    body = await _read_capped_body(request)
    if body is None:
        return JSONResponse({"error": "document exceeds the 15 MiB limit"}, status_code=413)
    if not body:
        return JSONResponse({"error": "empty body"}, status_code=400)
    content_type = request.headers.get("content-type", "")
    disconnected = threading.Event()
    convert.read_stop.set(convert.ReadStop(_read_deadline(request), disconnected))
    watcher = asyncio.create_task(_watch_disconnect(request, disconnected))
    try:
        result = await asyncio.to_thread(convert.stub_read, body, content_type)
    except convert.DocumentUnreadable as exc:
        content = {"error": str(exc)}
        if exc.reason:
            content["reason"] = exc.reason
        return JSONResponse(content, status_code=422)
    except Exception:
        logger.exception("unexpected /v1/read failure")
        sentry_sdk.capture_exception()
        return JSONResponse({"error": "internal error"}, status_code=500)
    finally:
        watcher.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await watcher
    return JSONResponse(result)
