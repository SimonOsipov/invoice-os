"""FastAPI app: GET /healthz, POST /v1/read. Wire contract: EXTR-03 story sec. 3.

/v1/read is backed by convert.stub_read; EXTR-03-03 swaps in the real DocumentConverter.
"""

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

import buildinfo
import convert

app = FastAPI()

# documents.size_bytes CHECKs <= this; matches maxDocumentBytes (cmd/submission/main.go:152).
MAX_DOCUMENT_BYTES = 15 * 1024 * 1024


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    """Body is exactly {"status": "ok", "build": "<sha>"} — internal/platform/health.go:62's shape."""
    return {"status": "ok", "build": buildinfo.read_build_sha(buildinfo.BUILD_FILE)}


@app.post("/v1/read")
async def read_document(request: Request) -> JSONResponse:
    """Raw document bytes in, the §3 wire contract out. Backed by convert.stub_read for now."""
    body = await request.body()
    if len(body) > MAX_DOCUMENT_BYTES:
        return JSONResponse({"error": "document exceeds the 15 MiB limit"}, status_code=413)
    if not body:
        return JSONResponse({"error": "empty body"}, status_code=400)
    content_type = request.headers.get("content-type", "")
    return JSONResponse(convert.stub_read(body, content_type))
