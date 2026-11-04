#!/usr/bin/env bash
# scripts/ci/caddy-headers.sh: runs the root Caddyfile in the SPA Dockerfiles' Caddy image and
# asserts every response forbids framing, the host redirects answer as the LB did, and a site with
# /404.html serves only its built addresses. Needs docker and curl.
set -uo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
img=caddy:2-alpine # same tag as frontend/*/Dockerfile
name="caddy-headers-$$"
sname="caddy-headers-strict-$$"
site=$(mktemp -d)
ssite=$(mktemp -d)
trap 'docker rm -f "$name" "$sname" >/dev/null 2>&1; rm -rf "$site" "$ssite"' EXIT INT TERM HUP

mkdir -p "$site/assets"
echo '<!doctype html><title>SPA-INDEX</title>' >"$site/index.html"
echo 'console.log(1)' >"$site/assets/index-abc123.js"

docker run -d --name "$name" -p 127.0.0.1::8080 \
  -v "$root/Caddyfile:/etc/caddy/Caddyfile:ro" -v "$site:/srv:ro" "$img" >/dev/null || exit 1
port=$(docker port "$name" 8080/tcp | head -n1 | sed 's/.*://')
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$port/health" && break; sleep 0.2; done

fail=0
for path in / /invoices/42 /.env /assets/index-abc123.js /health /library/health /assets/index-abc123.js.map; do
  hdrs=$(curl -s -o /dev/null -D - "http://127.0.0.1:$port$path" | tr -d '\r')
  code=$(printf '%s\n' "$hdrs" | head -n1)
  echo "$path -> $code"
  [ -n "$hdrs" ] || { echo "FAIL $path: no response"; fail=1; continue; }
  printf '%s\n' "$hdrs" | grep -qix "content-security-policy: frame-ancestors 'none'" \
    || { echo "FAIL $path: Content-Security-Policy: frame-ancestors 'none' missing"; fail=1; }
  printf '%s\n' "$hdrs" | grep -qix "x-frame-options: DENY" \
    || { echo "FAIL $path: X-Frame-Options: DENY missing"; fail=1; }
done

# row <port> <path> <want-status> <marker>: asserts status, body marker (- = empty body) and both frame headers.
# Optional 5th arg: -sI for a HEAD row (no body to match).
row() {
  local p=$1 path=$2 want=$3 marker=$4 flag=${5:--s} hdrs body code
  hdrs=$(mktemp "$ssite/h.XXXXXX")
  body=$(curl -s${flag#-s} -o - -D "$hdrs" "http://127.0.0.1:$p$path" | tr -d '\r')
  code=$(head -n1 "$hdrs" | cut -d' ' -f2)
  echo "$path -> $code"
  [ "$code" = "$want" ] || { echo "FAIL $path: want $want, got $code"; fail=1; }
  if [ "$flag" = "-sI" ]; then :
  elif [ "$marker" = "-" ]; then [ -z "$body" ] || { echo "FAIL $path: body not empty"; fail=1; }
  else printf '%s' "$body" | grep -q -- "$marker" || { echo "FAIL $path: body lacks $marker"; fail=1; }; fi
  tr -d '\r' <"$hdrs" | grep -qix "content-security-policy: frame-ancestors 'none'" \
    || { echo "FAIL $path: Content-Security-Policy: frame-ancestors 'none' missing"; fail=1; }
  tr -d '\r' <"$hdrs" | grep -qix "x-frame-options: DENY" \
    || { echo "FAIL $path: X-Frame-Options: DENY missing"; fail=1; }
  rm -f "$hdrs"
}

# SPA fixture (app, consoles): every path keeps the index.html fallback.
row "$port" /invoices/42 200 'SPA-INDEX'
row "$port" /.env 200 'SPA-INDEX'
row "$port" /library/health 200 '^OK$'
row "$port" /assets/index-abc123.js.map 404 -

# Strict fixture (landing, library): a site that ships /404.html. docker cp, not a bind mount:
# a macOS bind mount is case-insensitive and would serve /Privacy.
mkdir -p "$ssite/fx/privacy" "$ssite/fx/invite" "$ssite/fx/assets" "$ssite/fx/library/invoices/import-files" "$ssite/fx/library/_landing/assets"
echo '<!doctype html><title>STRICT-INDEX</title>' >"$ssite/fx/index.html"
echo '<!doctype html><meta name="robots" content="noindex"><title>NOTFOUND-PAGE</title>' >"$ssite/fx/404.html"
echo '<!doctype html><title>PRIVACY-PAGE</title>' >"$ssite/fx/privacy/index.html"
echo '<!doctype html><title>INVITE-PAGE</title>' >"$ssite/fx/invite/index.html"
echo '<!doctype html><title>LIBRARY-HOME</title>' >"$ssite/fx/library/index.html"
echo '<!doctype html><title>LIBRARY-GROUP</title>' >"$ssite/fx/library/invoices/index.html"
echo '<!doctype html><title>LIBRARY-FEATURE</title>' >"$ssite/fx/library/invoices/import-files/index.html"
echo 'console.log(2)' >"$ssite/fx/library/_landing/assets/index-abc123.js"
echo 'console.log(1)' >"$ssite/fx/assets/index-abc123.js"
echo 'User-agent: *' >"$ssite/fx/robots.txt"
echo 'BUILD-SHA' >"$ssite/fx/build.txt"
docker create --name "$sname" -p 127.0.0.1::8080 -v "$root/Caddyfile:/etc/caddy/Caddyfile:ro" "$img" >/dev/null \
  && docker cp "$ssite/fx/." "$sname:/srv" && docker start "$sname" >/dev/null || exit 1
sport=$(docker port "$sname" 8080/tcp | head -n1 | sed 's/.*://')
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$sport/health" && break; sleep 0.2; done

# Wrong addresses: 404 with the 404.html body, never the index.html one.
for path in /.env /nope-xyz /Privacy /sitemap.xml /404.html /index.html /privacy/index.html /privacy/x \
  /library/nope /library/invoices/nope /library/Invoices /library/index.html /library/invoices/index.html /library/_landing/ \
  /PRIVACY /INDEX.HTML '/404.html?x=1' /library/404.html /library/INVOICES /library/invoices/import-files/x /assets/; do
  row "$sport" "$path" 404 NOTFOUND-PAGE
done
row "$sport" /nope-xyz 404 NOTFOUND-PAGE -sI
row "$sport" /library/index.html 404 NOTFOUND-PAGE -sI
# Real addresses: 200 with their own body.
for r in '/|STRICT-INDEX' '/?signin=ready|STRICT-INDEX' '/?verified=1|STRICT-INDEX' '/?verify=failed|STRICT-INDEX' \
  '/?reset=1|STRICT-INDEX' '/?confirm=1|STRICT-INDEX' '/?confirm=invite|STRICT-INDEX' '/?forgot|STRICT-INDEX' \
  '/privacy|PRIVACY-PAGE' '/privacy/|PRIVACY-PAGE' '/invite|INVITE-PAGE' '/robots.txt|User-agent' '/build.txt|BUILD-SHA' \
  '/assets/index-abc123.js|console.log(1)' '/library/_landing/assets/index-abc123.js|console.log(2)' \
  '/library|LIBRARY-HOME' '/library/|LIBRARY-HOME' '/library/invoices|LIBRARY-GROUP' '/library/invoices/|LIBRARY-GROUP' \
  '/library/invoices/import-files|LIBRARY-FEATURE' '/library/invoices/import-files/|LIBRARY-FEATURE' \
  '/library//invoices|LIBRARY-GROUP' '//privacy|PRIVACY-PAGE' '/privacy?x=1|PRIVACY-PAGE' '/health|^OK$' '/library/health|^OK$'; do
  row "$sport" "${r%%|*}" 200 "${r#*|}"
done
row "$sport" /privacy 200 PRIVACY-PAGE -sI
row "$sport" /library/invoices 200 LIBRARY-GROUP -sI
row "$sport" /assets/index-abc123.js.map 404 -
row "$sport" /library/_landing/assets/index-abc123.js.map 404 -
row "$sport" /privacy.map 404 -
curl -s -o /dev/null -w '%{content_type}' "http://127.0.0.1:$sport/library/_landing/assets/index-abc123.js" | grep -qi javascript \
  || { echo "FAIL: /library/_landing/assets js is not javascript"; fail=1; }

# Host redirects: "host path want-location"; wants are newAddressOf (e2e/smoke/oldAddresses.ts) on https.
redirects='ascomply.com / https://www.ascomply.com/
ascomply.com /blog/x?q=1 https://www.ascomply.com/blog/x?q=1
blog.ascomply.com / https://www.ascomply.com/blog/
blog.ascomply.com /blog https://www.ascomply.com/blog
blog.ascomply.com /blog/ https://www.ascomply.com/blog/
blog.ascomply.com /blogger https://www.ascomply.com/blog/blogger
blog.ascomply.com /subscribe?t=1 https://www.ascomply.com/blog/subscribe?t=1
blog.ascomply.com /Blog/x https://www.ascomply.com/blog/Blog/x
library.ascomply.com / https://www.ascomply.com/library/
library.ascomply.com /library https://www.ascomply.com/library
library.ascomply.com /library/invoices https://www.ascomply.com/library/invoices
library.ascomply.com /libraryx https://www.ascomply.com/library/libraryx
library.ascomply.com /invoices/x?q=1 https://www.ascomply.com/library/invoices/x?q=1
library.ascomply.com /Library/x https://www.ascomply.com/library/Library/x'
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
