"""FastAPI app: GET /healthz, POST /v1/read. Wire contract: EXTR-03 story sec. 3.

Stub only — real handlers land in EXTR-03-01's implementation pass (this file) and
EXTR-03-03 (the real DocumentConverter behind /v1/read).
"""
from fastapi import FastAPI, Request

app = FastAPI()

# documents.size_bytes CHECKs <= this; matches maxDocumentBytes (cmd/submission/main.go:152).
MAX_DOCUMENT_BYTES = 15 * 1024 * 1024


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    """Body is exactly {"status": "ok", "build": "<sha>"} — internal/platform/health.go:62's shape."""
    raise NotImplementedError


@app.post("/v1/read")
async def read_document(request: Request) -> dict:
    """Raw document bytes in, the §3 wire contract out. Backed by convert.stub_read for now."""
    raise NotImplementedError
