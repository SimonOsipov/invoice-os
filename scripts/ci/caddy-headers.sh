#!/usr/bin/env bash
# scripts/ci/caddy-headers.sh: runs the root Caddyfile in the SPA Dockerfiles' Caddy image and
# asserts every response forbids framing and the host redirects answer as the LB did. Needs docker and curl.
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

# Host redirects: "host path want-location"; wants are newAddressOf (e2e/smoke/oldAddresses.ts) on https.
redirects='ascomply.com / https://www.ascomply.com/
ascomply.com /blog/x?q=1 https://www.ascomply.com/blog/x?q=1
blog.ascomply.com / https://www.ascomply.com/blog/
blog.ascomply.com /blog https://www.ascomply.com/blog
blog.ascomply.com /blog/ https://www.ascomply.com/blog/
blog.ascomply.com /blogger https://www.ascomply.com/blog/blogger
blog.ascomply.com /subscribe?t=1 https://www.ascomply.com/blog/subscribe?t=1
library.ascomply.com / https://www.ascomply.com/library/
library.ascomply.com /library https://www.ascomply.com/library
library.ascomply.com /library/invoices https://www.ascomply.com/library/invoices
library.ascomply.com /libraryx https://www.ascomply.com/library/libraryx
library.ascomply.com /invoices/x?q=1 https://www.ascomply.com/library/invoices/x?q=1'
nrows=0
while read -r h path want; do
  nrows=$((nrows + 1))
  hdrs=$(curl -s -o /dev/null -D - -H "Host: $h" "http://127.0.0.1:$port$path" | tr -d '\r')
  code=$(printf '%s\n' "$hdrs" | head -n1 | cut -d' ' -f2)
  loc=$(printf '%s\n' "$hdrs" | sed -n 's/^[Ll]ocation: //p')
  echo "$h$path -> $code $loc"
  { [ "$code" = 301 ] && [ "$loc" = "$want" ]; } || { echo "FAIL $h$path: want 301 $want"; fail=1; }
done <<<"$redirects"
[ "$nrows" -gt 0 ] || { echo "FAIL: no redirect rows"; fail=1; }
# A host that is not an old address must serve, not redirect.
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Host: www.ascomply.com" "http://127.0.0.1:$port/blog/x")
[ "$code" = 200 ] || { echo "FAIL: www.ascomply.com/blog/x answered $code, want 200"; fail=1; }
# The 404 must really be the maps rule, or the check above proves nothing.
curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/assets/index-abc123.js.map" | grep -qx 404 \
  || { echo "FAIL: *.map did not answer 404"; fail=1; }
exit "$fail"
