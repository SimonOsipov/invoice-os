#!/usr/bin/env bash
# sidecar/docling/lock.sh [--check | --upgrade | --upgrade-package NAME ...] -- regenerate the hashed locks from requirements*.txt.
# uv keeps the pins already in a lock, so a plain run never moves a transitive package and
# Dependabot does not read .lock files: --upgrade-package NAME (repeatable) moves just that
# package, --upgrade re-resolves everything to the newest allowed. Read the lock diff after.
# --check re-resolves into a temp dir seeded with the committed locks and fails when any
# name==version differs (hashes and header are not compared).
# Needs uv 0.11.7 (CI pins the same). A Dependabot PR on requirements.txt stays red at
# --check until this script runs on its branch; read the lock diff before merging.
# Local install, Linux x86_64 or aarch64, Python 3.12 (on macOS run the suite in docling:test):
#   pip install --require-hashes --no-deps --index-url https://download.pytorch.org/whl/cpu -r sidecar/docling/torch.lock && pip install --require-hashes --no-deps --index-url https://pypi.org/simple -r sidecar/docling/requirements-dev.lock
set -euo pipefail
cd "$(dirname "$0")"
common=(--torch-backend cpu --python-version 3.12 --python-platform x86_64-manylinux_2_28
  --generate-hashes --emit-index-annotation --quiet --custom-compile-command "sidecar/docling/lock.sh")
split=(--no-emit-package opencv-python --no-emit-package torch --no-emit-package torchvision)
up=()
check=0
while [ $# -gt 0 ]; do
  case "$1" in
    --check) check=1 ;;
    --upgrade) up+=(--upgrade) ;;
    --upgrade-package) [ $# -ge 2 ] || { echo "--upgrade-package needs a name" >&2; exit 2; }; up+=(--upgrade-package "$2"); shift ;;
    *) echo "usage: lock.sh [--check | --upgrade | --upgrade-package NAME ...]" >&2; exit 2 ;;
  esac
  shift
done
[ $check = 0 ] || [ "${#up[@]}" = 0 ] || { echo "--check takes no upgrade flag" >&2; exit 2; }
out=.
if [ $check = 1 ]; then
  out="$(mktemp -d)"
  trap 'rm -rf "$out"' EXIT
  cp requirements.lock requirements-dev.lock torch.lock "$out"/
fi
uv pip compile requirements.txt "${common[@]}" "${split[@]}" ${up[@]+"${up[@]}"} -o "$out/requirements.lock"
uv pip compile requirements-dev.txt "${common[@]}" "${split[@]}" ${up[@]+"${up[@]}"} -c "$out/requirements.lock" -o "$out/requirements-dev.lock"
grep -E '^torch(vision)?==' requirements.txt | uv pip compile - "${common[@]}" --no-deps ${up[@]+"${up[@]}"} -o "$out/torch.lock"
[ "$out" = . ] && exit 0
versions() { grep -E '^[a-z0-9]' "$1" | sed 's/ .*//' | sort; }
rc=0
for f in requirements.lock requirements-dev.lock torch.lock; do
  diff <(versions "$f") <(versions "$out/$f") >/dev/null || { echo "$f disagrees with requirements*.txt; run sidecar/docling/lock.sh"; diff <(versions "$f") <(versions "$out/$f") || true; rc=1; }
done
exit $rc
