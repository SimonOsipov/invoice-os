#!/usr/bin/env bash
# e2e-suite.sh <script> [arg...]: runs one e2e Playwright suite.
# With DRAFT=true it runs only the space-separated SPECS (paths under e2e/), once each, and skips the suite when SPECS is empty.
set -euo pipefail

if [ "${DRAFT:-}" != true ]; then
  exec pnpm --filter @invoice-os/e2e "$@"
fi
if [ -z "${SPECS// /}" ]; then
  echo "This draft changed no spec that $1 runs."
  exit 0
fi
read -ra specs <<<"$SPECS"
exec pnpm --filter @invoice-os/e2e "$@" --retries=0 "${specs[@]}"
