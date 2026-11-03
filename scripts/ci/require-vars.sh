#!/usr/bin/env bash
# Fails when a named environment variable is empty or blank; each step maps its names from `vars`.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: require-vars.sh NAME..." >&2
  exit 2
fi

missing=0
for name in "$@"; do
  value="${!name:-}"
  if [ -z "${value//[[:space:]]/}" ]; then
    echo "::error::${name} is not set. Add it under Settings → Secrets and variables → Actions → Variables."
    missing=1
  fi
done
exit "$missing"
