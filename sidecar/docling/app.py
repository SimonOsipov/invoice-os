"""FastAPI app: GET /healthz, POST /v1/read. Wire contract: EXTR-03 story sec. 3."""

import asyncio
import logging

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

import buildinfo
import convert

logger = logging.getLogger(__name__)

app = FastAPI()

# Background warm-up (story sec. 4): starts at import ("boot"), off any request path.
# T-01-3 still sees construction_count() == 0 right after import -- a real model build
# takes far longer than one HTTP round trip, so the race is not observable in practice.
# The thread lives in convert.py (not started inline here) so tests can join it deterministically.
convert.start_warm_up()

# documents.size_bytes CHECKs <= this; matches maxDocumentBytes (cmd/submission/main.go:152).
MAX_DOCUMENT_BYTES = 15 * 1024 * 1024


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    """Body is exactly {"status": "ok", "build": "<sha>"} — internal/platform/health.go:62's
    shape. No lock, over a module-level string -- stays on the event loop (T-03-14).
    """
    return {"status": "ok", "build": buildinfo.read_build_sha(buildinfo.BUILD_FILE)}


@app.post("/v1/read")
async def read_document(request: Request) -> JSONResponse:
    """Raw document bytes in, the §3 wire contract out.

    convert.stub_read (the real converter, off-thread via asyncio.to_thread) never runs on
    the event loop, so a slow cold-start or a large convert can't starve /healthz (T-03-14).
    """
    body = await request.body()
    if len(body) > MAX_DOCUMENT_BYTES:
        return JSONResponse({"error": "document exceeds the 15 MiB limit"}, status_code=413)
    if not body:
        return JSONResponse({"error": "empty body"}, status_code=400)
    content_type = request.headers.get("content-type", "")
    try:
        result = await asyncio.to_thread(convert.stub_read, body, content_type)
    except convert.DocumentUnreadable as exc:
        return JSONResponse({"error": str(exc)}, status_code=422)
    except Exception:
        logger.exception("unexpected /v1/read failure")
        return JSONResponse({"error": "internal error"}, status_code=500)
    return JSONResponse(result)
