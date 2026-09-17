#!/usr/bin/env bash
# tools/aimodeltest/docling-read.sh: Stage A -- read PDFs through a one-shot, network-disabled
# Docling image built from a source commit (D-A03, D-A04), and byte-compare committed goldens
# (D-A05). Never runs stamp-build-sha.sh; the image reports build=dev and provenance.txt below
# carries the real commit instead.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: docling-read.sh <commit> <out-dir> <pdf>... [--compare <golden>...]

Builds sidecar/docling's run stage from <commit> for linux/amd64 (git archive into a scratch
context, so .ralph/scratch/ invoices never enter the build), then reads each <pdf> in its own
"docker run --rm -i --network none" container calling convert.stub_read directly -- no
uvicorn, no server, no eth0. Writes <out-dir>/<pdf-stem>.docling.json per PDF in the canary
golden format (json.dumps(..., indent=2, sort_keys=True, ensure_ascii=False) + one newline).

  --compare <golden>...   cmp each golden against the read of the same stem; a mismatch is
                           recorded in provenance.txt, never fatal.

Writes <out-dir>/provenance.txt: the commit, the last sidecar/docling commit, the image ID,
the platform, installed docling/docling-parse/rapidocr/onnxruntime versions, and per-golden
match/mismatch.
EOF
}

case "${1:-}" in
--help | -h)
  usage
  exit 0
  ;;
esac
if [ $# -lt 3 ]; then
  usage >&2
  exit 1
fi

COMMIT="$1"
shift
OUT_DIR="$1"
shift

PDFS=()
GOLDENS=()
in_goldens=0
for arg in "$@"; do
  if [ "$arg" = "--compare" ]; then
    in_goldens=1
    continue
  fi
  if [ "$in_goldens" -eq 1 ]; then
    GOLDENS+=("$arg")
  else
    PDFS+=("$arg")
  fi
done
if [ "${#PDFS[@]}" -eq 0 ]; then
  echo "docling-read.sh: at least one <pdf> is required" >&2
  exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
SRC_DIR="$REPO_ROOT/.ralph/scratch/air01/docling-src"
rm -rf "$SRC_DIR"
mkdir -p "$SRC_DIR" "$OUT_DIR"

# git archive keeps the real invoices under .ralph/scratch/ out of the build context (P-17):
# only sidecar/docling and the buildsha placeholder are exported.
git -C "$REPO_ROOT" archive "$COMMIT" sidecar/docling internal/platform/buildsha.txt |
  tar -x -C "$SRC_DIR"

IMAGE_TAG="docling-read:${COMMIT}"
docker buildx build --platform linux/amd64 --load \
  --target run \
  -f "$SRC_DIR/sidecar/docling/Dockerfile" \
  -t "$IMAGE_TAG" \
  "$SRC_DIR"

IMAGE_ID="$(docker image inspect "$IMAGE_TAG" --format '{{.Id}}')"
PLATFORM="$(docker image inspect "$IMAGE_TAG" --format '{{.Os}}/{{.Architecture}}')"
VERSIONS="$(docker run --rm --network none --platform linux/amd64 "$IMAGE_TAG" python3 -c '
import importlib.metadata as m
for pkg in ("docling", "docling-parse", "rapidocr", "onnxruntime"):
    try:
        print(f"{pkg}=={m.version(pkg)}")
    except m.PackageNotFoundError:
        print(f"{pkg}==MISSING")
')"

# Runs off the event loop entirely: convert.stub_read is the function /v1/read calls (D-A04),
# never uvicorn. fd 1 is saved and redirected to fd 2 BEFORE `import convert`, so a library
# print cannot corrupt the json bytes cmp reads.
READ_SCRIPT='
import os, sys
out = os.fdopen(os.dup(1), "w", encoding="utf-8")
os.dup2(2, 1)
import json
import convert
body = sys.stdin.buffer.read()
payload = convert.stub_read(body, "application/pdf")
out.write(json.dumps(payload, indent=2, sort_keys=True, ensure_ascii=False) + "\n")
out.flush()
'

READ_FAILURES=()
for pdf in "${PDFS[@]}"; do
  stem="$(basename "$pdf")"
  stem="${stem%.*}"
  dest="$OUT_DIR/$stem.docling.json"
  tmp="$(mktemp)"
  if docker run --rm -i --network none --platform linux/amd64 "$IMAGE_TAG" \
    python3 -c "$READ_SCRIPT" <"$pdf" >"$tmp"; then
    mv "$tmp" "$dest"
    echo "read: $pdf -> $dest"
  else
    rm -f "$tmp"
    echo "FAILED: $pdf" >&2
    READ_FAILURES+=("$stem")
  fi
done

{
  echo "commit: $COMMIT"
  echo "sidecar/docling last commit: $(git -C "$REPO_ROOT" log -1 --format='%h %ad' --date=short -- sidecar/docling)"
  echo "image id: $IMAGE_ID"
  echo "platform: $PLATFORM"
  echo "installed versions:"
  echo "$VERSIONS" | sed 's/^/  /'
  if [ "${#READ_FAILURES[@]}" -gt 0 ]; then
    echo "read failures:"
    for stem in "${READ_FAILURES[@]}"; do
      echo "  $stem"
    done
  fi
  if [ "${#GOLDENS[@]}" -gt 0 ]; then
    echo "golden reproduction:"
    for golden in "${GOLDENS[@]}"; do
      gname="$(basename "$golden")"
      gstem="${gname%.docling.json}"
      candidate="$OUT_DIR/$gstem.docling.json"
      if [ ! -f "$candidate" ]; then
        echo "  $gname: no read"
      elif cmp -s "$golden" "$candidate"; then
        echo "  $gname: match"
      else
        echo "  $gname: MISMATCH"
      fi
    done
  fi
} >"$OUT_DIR/provenance.txt"

echo "provenance written: $OUT_DIR/provenance.txt"
if [ "${#READ_FAILURES[@]}" -gt 0 ]; then
  exit 1
fi
