#!/usr/bin/env bash
# scripts/ci/caddy-headers.sh: runs the root Caddyfile in the SPA Dockerfiles' Caddy image and
# asserts every response forbids framing. Needs docker and curl.
set -uo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
img=caddy:2-alpine # same tag as frontend/*/Dockerfile
name="caddy-headers-$$"
site=$(mktemp -d)
trap 'docker rm -f "$name" >/dev/null 2>&1; rm -rf "$site"' EXIT INT TERM HUP

mkdir -p "$site/assets"
echo '<!doctype html><title>x</title>' >"$site/index.html"
echo 'console.log(1)' >"$site/assets/index-abc123.js"

docker run -d --name "$name" -p 127.0.0.1::8080 \
  -v "$root/Caddyfile:/etc/caddy/Caddyfile:ro" -v "$site:/srv:ro" "$img" >/dev/null || exit 1
port=$(docker port "$name" 8080/tcp | head -n1 | sed 's/.*://')
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$port/health" && break; sleep 0.2; done

fail=0
for path in / /invoices/42 /assets/index-abc123.js /health /assets/index-abc123.js.map; do
  hdrs=$(curl -s -o /dev/null -D - "http://127.0.0.1:$port$path" | tr -d '\r')
  code=$(printf '%s\n' "$hdrs" | head -n1)
  echo "$path -> $code"
  [ -n "$hdrs" ] || { echo "FAIL $path: no response"; fail=1; continue; }
  printf '%s\n' "$hdrs" | grep -qix "content-security-policy: frame-ancestors 'none'" \
    || { echo "FAIL $path: Content-Security-Policy: frame-ancestors 'none' missing"; fail=1; }
  printf '%s\n' "$hdrs" | grep -qix "x-frame-options: DENY" \
    || { echo "FAIL $path: X-Frame-Options: DENY missing"; fail=1; }
done
# The 404 must really be the maps rule, or the check above proves nothing.
curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/assets/index-abc123.js.map" | grep -qx 404 \
  || { echo "FAIL: *.map did not answer 404"; fail=1; }
exit "$fail"
