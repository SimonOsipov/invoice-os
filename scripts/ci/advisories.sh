#!/usr/bin/env bash
# Bump version and sha256 together: gh release view <v> -R google/osv-scanner --json assets.
# The gate still needs the scanner log lines "Scanned <manifest> file and found N packages".
set -euo pipefail
version=v2.7.0
sha256=160c6253e2922e9e0a6a9771c5c4c40a4b3c6090b34a3cdd5e80c96cc2e4218b  # osv-scanner_linux_amd64
bin="${RUNNER_TEMP:-$(mktemp -d)}/osv-scanner"
curl -fsSL --retry 3 -o "$bin" "https://github.com/google/osv-scanner/releases/download/${version}/osv-scanner_linux_amd64"
echo "${sha256}  ${bin}" | sha256sum -c -
chmod +x "$bin"
exec go run ./internal/tools/advisorygate -osv "$bin"
