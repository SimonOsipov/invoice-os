#!/usr/bin/env bash
# scripts/ci/docling-canary.sh <healthz|buildsha|models|nonroot|ocr|convert> [args...]
#
# Assertion steps for ci.yml's docling-canary job (T-02-1..4). Every check execs
# into the already-running `docling-canary` container and asks it in python3:
# --network none has no eth0, so a curl from the runner or a `docker run -p`
# mapping can never reach it, and python3 is the one tool guaranteed present in
# a slim image (it is the app's own runtime) -- curl/find/grep may not be.
#
# DOCLING_CANARY_PORT defaults to 8080, the fleet's $PORT convention
# (internal/platform/config.go); confirm it matches whatever port subtask 02's
# Dockerfile actually binds and override the job's env if not.
set -euo pipefail

CONTAINER="${DOCLING_CANARY_CONTAINER:-docling-canary}"
PORT="${DOCLING_CANARY_PORT:-8080}"

check="${1:?usage: docling-canary.sh <healthz|buildsha|models|nonroot|ocr|convert> [args...]}"
shift

healthz_json() {
  docker exec "$CONTAINER" python3 -c "
import json, urllib.request
print(json.dumps(json.load(urllib.request.urlopen('http://localhost:${PORT}/healthz', timeout=2))))
"
}

case "$check" in
healthz)
  # T-02-2: 200 within 15s of container start.
  deadline=$((SECONDS + 15))
  until healthz_json >/tmp/docling-healthz.json 2>/dev/null; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "::error::/healthz did not answer within 15s of container start"
      exit 1
    fi
    sleep 1
  done
  echo "/healthz answered: $(cat /tmp/docling-healthz.json)"
  ;;

buildsha)
  # T-02-4: build equals the sha stamp-build-sha.sh wrote before this image was built.
  want="${1:?usage: docling-canary.sh buildsha <sha>}"
  got="$(healthz_json | python3 -c 'import json, sys; print(json.load(sys.stdin)["build"])')"
  if [ "$got" != "$want" ]; then
    echo "::error::/healthz build=$got, want $want"
    exit 1
  fi
  echo "/healthz build matches the stamped sha ($got)"
  ;;

models)
  # T-02-1: layout, TableFormer and all three RapidOCR-en checkpoints are present.
  # Substrings/names traced from the pinned docling-slim==2.123.1 source, not
  # guessed: model_downloader.download_models() names its HF snapshot dirs after
  # the repo_id (`docling-project--docling-layout-heron[-onnx]` for layout;
  # `docling-project--docling-models/model_artifacts/tableformer/...` for
  # TableFormer), and RapidOcrModel._model_repo_folder is literally "RapidOcr".
  # Layout/TableFormer are matched by a >1 MiB substring scan; RapidOCR's cls
  # checkpoint is only ~570 KiB (under that floor), so its three onnxruntime:en
  # files are asserted by exact name instead -- a >1 MiB scan alone would pass
  # with det/rec present and cls silently missing.
  docker exec "$CONTAINER" python3 -c "
import os, sys
root = os.environ.get('DOCLING_ARTIFACTS_PATH', '')
if not root:
    print('DOCLING_ARTIFACTS_PATH is unset', file=sys.stderr); sys.exit(1)
all_paths = []
big = []
for dirpath, _, files in os.walk(root):
    for name in files:
        p = os.path.join(dirpath, name)
        all_paths.append(p)
        if os.path.getsize(p) > 1024 * 1024:
            big.append(p)
if not big:
    print(f'no file over 1 MiB found under {root}', file=sys.stderr); sys.exit(1)
lower_big = [p.lower() for p in big]
big_checks = {
    'layout (docling-project/docling-layout-heron[-onnx])': lambda ps: any('docling-layout-heron' in p for p in ps),
    'tableformer (docling-project/docling-models/model_artifacts/tableformer)': lambda ps: any('tableformer' in p for p in ps),
}
missing_big = [name for name, ok in big_checks.items() if not ok(lower_big)]
if missing_big:
    print(f'no file over 1 MiB under {root} satisfies: {missing_big}', file=sys.stderr)
    print(chr(10).join(big), file=sys.stderr)
    sys.exit(1)
rapidocr_dir = os.path.join(root, 'RapidOcr') + os.sep
want_rapidocr = {'PP-OCRv6_det_small.onnx', 'PP-OCRv6_rec_small.onnx', 'ch_ppocr_mobile_v2.0_cls_mobile.onnx'}
have_rapidocr = {os.path.basename(p) for p in all_paths if p.startswith(rapidocr_dir)}
missing_rapidocr = sorted(want_rapidocr - have_rapidocr)
if missing_rapidocr:
    print(f'RapidOCR-en checkpoint(s) missing under {rapidocr_dir}: {missing_rapidocr}', file=sys.stderr)
    sys.exit(1)
print(f'layout, TableFormer (>1 MiB) and all three RapidOCR-en checkpoints present under {root}')
"
  ;;

ocr)
  # Would have caught EXTR-03-02's shipped defect: every checkpoint was baked and
  # T-02-1 passed, but the run stage couldn't `import cv2` (libxcb.so.1 missing), so
  # RapidOcrModel could never construct. Goes past the bare import to build the real
  # model against the baked artifacts_path -- proves the ONNX session loads offline,
  # not just that the import line succeeds.
  docker exec "$CONTAINER" python3 -c "
import os
import cv2
print(f'cv2 {cv2.__version__} imports OK')

from pathlib import Path
from docling.datamodel.accelerator_options import AcceleratorOptions
from docling.datamodel.pipeline_options import RapidOcrOptions
from docling.models.stages.ocr.rapid_ocr_model import RapidOcrModel

artifacts_path = Path(os.environ['DOCLING_ARTIFACTS_PATH'])
model = RapidOcrModel(
    enabled=True,
    artifacts_path=artifacts_path,
    options=RapidOcrOptions(lang=['english']),
    accelerator_options=AcceleratorOptions(),
)
assert model.reader is not None, 'RapidOcrModel.reader was not constructed'
print('RapidOcrModel constructed offline against', artifacts_path, '-- ONNX session ready')
"
  ;;

convert)
  # T-03-12: the baked models convert the fixture with --network none already in effect --
  # proves the offline artifacts are real, not just present (T-02-1 only checked size).
  docker cp sidecar/docling/tests/testdata/scanned_ocr_fixture.pdf "$CONTAINER":/tmp/scanned_ocr_fixture.pdf
  docker exec "$CONTAINER" python3 -c "
import json
import urllib.error
import urllib.request

with open('/tmp/scanned_ocr_fixture.pdf', 'rb') as f:
    body = f.read()
req = urllib.request.Request(
    'http://localhost:${PORT}/v1/read',
    data=body,
    headers={'Content-Type': 'application/pdf'},
    method='POST',
)
try:
    with urllib.request.urlopen(req, timeout=120) as resp:
        status = resp.status
        payload = json.load(resp)
except urllib.error.HTTPError as err:
    raise SystemExit(f'/v1/read returned {err.code}: {err.read().decode()}')
if status != 200:
    raise SystemExit(f'/v1/read returned {status}: {payload}')
tokens = [tok for page in payload['pages'] for tok in page['tokens']]
if not tokens:
    raise SystemExit('offline convert produced zero tokens')
print(f'/v1/read converted the fixture offline: {len(tokens)} token(s)')
"
  ;;

nonroot)
  # T-02-3: id -u is non-zero.
  uid="$(docker exec "$CONTAINER" id -u)"
  if [ "$uid" = "0" ]; then
    echo "::error::container runs as root (uid 0)"
    exit 1
  fi
  echo "container runs as uid $uid (non-root)"
  ;;

*)
  echo "::error::unknown check '$check'"
  exit 1
  ;;
esac
