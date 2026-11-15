#!/usr/bin/env bash
# scripts/ci/caddy-headers.sh: runs the root Caddyfile in the SPA Dockerfiles' Caddy image and
# asserts every response carries the security headers and the per-image script policy, Cache-Control is right per response class, the
# host redirects answer as the LB did, and a site with /404.html serves only its built addresses. Needs docker and curl.
set -uo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
# ceiling: held at Caddy 2.11.7 (2.11.8 try_files matches a bare directory); move when a release passes this script.
img=caddy:2.11.7-alpine@sha256:e70b164da81ef698b3ad19af799ea6086ce6d9e48ed4079eb9fe26f6fbf14ee5 # pinned equal to frontend/*/Dockerfile by TestCaddyPins_RepoPinned
name="caddy-headers-$$"
pname="caddy-headers-policy-$$"
sname="caddy-headers-strict-$$"
site=$(mktemp -d)
ssite=$(mktemp -d)
policy=$(mktemp)
trap 'docker rm -f "$name" "$sname" "$pname" >/dev/null 2>&1; rm -rf "$site" "$ssite" "$policy"' EXIT INT TERM HUP

mkdir -p "$site/assets"
echo '<!doctype html><title>SPA-INDEX</title>' >"$site/index.html"
echo 'console.log(1)' >"$site/assets/index-abc123.js"

docker run -d --name "$name" -p 127.0.0.1::8080 \
  -v "$root/Caddyfile:/etc/caddy/Caddyfile:ro" -v "$site:/srv:ro" "$img" >/dev/null || exit 1
port=$(docker port "$name" 8080/tcp | head -n1 | sed 's/.*://')
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$port/health" && break; sleep 0.2; done

fail=0
# Copied from the Caddyfile's first header block; the CSP line is $csp.
want_hdrs=(
  "x-frame-options: DENY"
  "x-content-type-options: nosniff"
  "referrer-policy: strict-origin-when-cross-origin"
  "permissions-policy: camera=(), microphone=(), geolocation=(), payment=(), usb=()"
)
csp="content-security-policy: frame-ancestors 'none'"
# sec <label> <headers-text>: each security header and the CSP line appears exactly once.
sec() {
  local l=$1 h=$2 w n
  for w in "$csp" "${want_hdrs[@]}"; do
    n=$(printf '%s\n' "$h" | tr -d '\r' | grep -Fxic -- "$w")
    [ "$n" = 1 ] || { echo "FAIL $l: '$w' x$n, want 1"; fail=1; }
  done
}
# cc <port> <path> <want> [-sI]: want no-cache, no-store or - (no Cache-Control line). Status must be 200, or 404 for no-store.
cc() {
  local flag=${4:--s} h n code want=200
  [ "$3" = no-store ] && want=404
  h=$(curl -s${flag#-s} -o /dev/null -D - "http://127.0.0.1:$1$2" | tr -d '\r')
  code=$(printf '%s\n' "$h" | head -n1 | cut -d' ' -f2)
  [ "$code" = "$want" ] || { echo "FAIL $2: want $want, got '$code'"; fail=1; }
  n=$(printf '%s\n' "$h" | grep -ic '^cache-control:')
  if [ "$3" = - ]; then
    [ "$n" = 0 ] || { echo "FAIL $2: Cache-Control present, want none"; fail=1; }
  else
    [ "$n" = 1 ] && printf '%s\n' "$h" | grep -qix "cache-control: $3" || { echo "FAIL $2: want one Cache-Control: $3"; fail=1; }
  fi
}
# cond <port> <path> <req-header> <validator-header> <cache>: 304 on revalidation, with the security headers and
# the Cache-Control a 200 would carry (no-cache or - for none).
cond() {
  local v h code n
  v=$(curl -s -o /dev/null -D - "http://127.0.0.1:$1$2" | tr -d '\r' | sed -n "s/^$4: //Ip")
  [ -n "$v" ] || { echo "FAIL $2: no $4 to revalidate with"; fail=1; return; }
  h=$(curl -s -o /dev/null -D - -H "$3: $v" "http://127.0.0.1:$1$2" | tr -d '\r')
  code=$(printf '%s\n' "$h" | head -n1 | cut -d' ' -f2)
  echo "$2 ($3) -> $code"
  [ "$code" = 304 ] || { echo "FAIL $2 with $3: want 304, got $code"; fail=1; }
  sec "$2 $3" "$h"
  n=$(printf '%s\n' "$h" | grep -ic '^cache-control:')
  if [ "$5" = - ]; then [ "$n" = 0 ] || { echo "FAIL $2 304: Cache-Control present, want none"; fail=1; }
  else [ "$n" = 1 ] && printf '%s\n' "$h" | grep -qix "cache-control: $5" || { echo "FAIL $2 304: want one Cache-Control: $5"; fail=1; }; fi
}

for path in / /invoices/42 /.env /assets/index-abc123.js /health /library/health /check/health /assets/index-abc123.js.map; do
  hdrs=$(curl -s -o /dev/null -D - "http://127.0.0.1:$port$path" | tr -d '\r')
  code=$(printf '%s\n' "$hdrs" | head -n1)
  echo "$path -> $code"
  [ -n "$hdrs" ] || { echo "FAIL $path: no response"; fail=1; continue; }
  sec "$path" "$hdrs"
done

# Policy container: the SPA fixture with a build's csp.txt mounted; every response carries that text.
printf '%s' "script-src 'self' https://sentinel.example; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'" >"$policy"
docker run -d --name "$pname" -p 127.0.0.1::8080 -v "$root/Caddyfile:/etc/caddy/Caddyfile:ro" \
  -v "$policy:/etc/caddy/csp.txt:ro" -v "$site:/srv:ro" "$img" >/dev/null || exit 1
pport=$(docker port "$pname" 8080/tcp | head -n1 | sed 's/.*://')
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$pport/health" && break; sleep 0.2; done
pcsp="content-security-policy: $(cat "$policy")"
csp=$pcsp
for path in / /invoices/42 /.env /assets/index-abc123.js /health /library/health /check/health /assets/index-abc123.js.map; do
  hdrs=$(curl -s -o /dev/null -D - "http://127.0.0.1:$pport$path" | tr -d '\r')
  echo "policy $path -> $(printf '%s\n' "$hdrs" | head -n1)"
  [ -n "$hdrs" ] || { echo "FAIL policy $path: no response"; fail=1; continue; }
  sec "policy $path" "$hdrs"
done
cond "$pport" / If-None-Match etag no-cache
csp="content-security-policy: frame-ancestors 'none'"

# row <port> <path> <want-status> <marker>: asserts status, body marker (- = empty body) and the security headers.
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
  sec "$path" "$(cat "$hdrs")"
  rm -f "$hdrs"
}

# SPA fixture (app, consoles): every path keeps the index.html fallback.
row "$port" /invoices/42 200 'SPA-INDEX'
row "$port" /.env 200 'SPA-INDEX'
row "$port" /library/health 200 '^OK$'
row "$port" /check/health 200 '^OK$'
row "$port" /assets/index-abc123.js.map 404 -
cc "$port" / no-cache
cc "$port" /invoices/42 no-cache
cc "$port" /invoices/42 no-cache -sI
cc "$port" /assets/index-abc123.js -
cond "$port" / If-None-Match etag no-cache
cond "$port" /assets/index-abc123.js If-Modified-Since last-modified -

# Strict fixture (landing, library): a site that ships /404.html. docker cp, not a bind mount:
# a macOS bind mount is case-insensitive and would serve /Privacy.
mkdir -p "$ssite/fx/privacy" "$ssite/fx/invite" "$ssite/fx/assets" "$ssite/fx/library/invoices/import-files" "$ssite/fx/library/_landing/assets" "$ssite/fx/check"
echo '<!doctype html><title>STRICT-INDEX</title>' >"$ssite/fx/index.html"
echo '<!doctype html><meta name="robots" content="noindex"><title>NOTFOUND-PAGE</title>' >"$ssite/fx/404.html"
echo '<!doctype html><title>PRIVACY-PAGE</title>' >"$ssite/fx/privacy/index.html"
echo '<!doctype html><title>INVITE-PAGE</title>' >"$ssite/fx/invite/index.html"
echo '<!doctype html><title>LIBRARY-HOME</title>' >"$ssite/fx/library/index.html"
echo '<!doctype html><title>LIBRARY-GROUP</title>' >"$ssite/fx/library/invoices/index.html"
echo '<!doctype html><title>LIBRARY-FEATURE</title>' >"$ssite/fx/library/invoices/import-files/index.html"
echo '<!doctype html><title>CHECK-HOME</title>' >"$ssite/fx/check/index.html"
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
  /PRIVACY /INDEX.HTML '/404.html?x=1' /library/404.html /library/INVOICES /library/invoices/import-files/x /assets/ /check/nope; do
  row "$sport" "$path" 404 NOTFOUND-PAGE
done
row "$sport" /nope-xyz 404 NOTFOUND-PAGE -sI
# The 404 sends no validators and answers conditionals with the full page.
nfh=$(curl -s -o /dev/null -D - "http://127.0.0.1:$sport/nope-xyz" | tr -d '\r')
printf '%s\n' "$nfh" | grep -qi '^etag:\|^last-modified:' && { echo "FAIL /nope-xyz: 404 carries a validator"; fail=1; }
for cond in 'If-None-Match: *' 'If-Modified-Since: Fri, 01 Jan 2100 00:00:00 GMT'; do
  cb=$(curl -s -H "$cond" -w '|%{http_code}' "http://127.0.0.1:$sport/nope-xyz")
  { [ "${cb##*|}" = 404 ] && printf '%s' "$cb" | grep -q NOTFOUND-PAGE; } || { echo "FAIL /nope-xyz with $cond: want 404 with the not-found body"; fail=1; }
done
petag=$(curl -s -o /dev/null -D - "http://127.0.0.1:$sport/privacy" | tr -d '\r' | sed -n 's/^[Ee][Tt]ag: //p')
cb=$(curl -s -H "If-None-Match: $petag" -w '|%{http_code}' "http://127.0.0.1:$sport/nope-xyz")
{ [ "${cb##*|}" = 404 ] && printf '%s' "$cb" | grep -q NOTFOUND-PAGE; } || { echo "FAIL /nope-xyz with a page ETag: want 404 with the not-found body"; fail=1; }
row "$sport" /library/index.html 404 NOTFOUND-PAGE -sI
# Real addresses: 200 with their own body.
for r in '/|STRICT-INDEX' '/?signin=ready|STRICT-INDEX' '/?verified=1|STRICT-INDEX' '/?verify=failed|STRICT-INDEX' \
  '/?reset=1|STRICT-INDEX' '/?confirm=1|STRICT-INDEX' '/?confirm=invite|STRICT-INDEX' '/?forgot|STRICT-INDEX' \
  '/privacy|PRIVACY-PAGE' '/privacy/|PRIVACY-PAGE' '/invite|INVITE-PAGE' '/robots.txt|User-agent' '/build.txt|BUILD-SHA' \
  '/assets/index-abc123.js|console.log(1)' '/library/_landing/assets/index-abc123.js|console.log(2)' \
  '/library|LIBRARY-HOME' '/library/|LIBRARY-HOME' '/library/invoices|LIBRARY-GROUP' '/library/invoices/|LIBRARY-GROUP' \
  '/library/invoices/import-files|LIBRARY-FEATURE' '/library/invoices/import-files/|LIBRARY-FEATURE' \
  '/library//invoices|LIBRARY-GROUP' '//privacy|PRIVACY-PAGE' '/privacy?x=1|PRIVACY-PAGE' '/health|^OK$' '/library/health|^OK$' \
  '/check|CHECK-HOME' '/check/|CHECK-HOME' '/check/health|^OK$'; do
  row "$sport" "${r%%|*}" 200 "${r#*|}"
done
row "$sport" /privacy 200 PRIVACY-PAGE -sI
row "$sport" /library/invoices 200 LIBRARY-GROUP -sI
row "$sport" /assets/index-abc123.js.map 404 -
row "$sport" /library/_landing/assets/index-abc123.js.map 404 -
row "$sport" /privacy.map 404 -
for p in / /privacy /privacy/ /library /library/invoices/import-files /check/; do cc "$sport" "$p" no-cache; done
for p in /assets/index-abc123.js /library/_landing/assets/index-abc123.js /robots.txt; do cc "$sport" "$p" -; done
for p in /nope-xyz /index.html; do cc "$sport" "$p" no-store; done
cc "$sport" /nope-xyz no-store -sI
cond "$sport" /privacy If-None-Match etag no-cache
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
for rp in "$port" "$pport"; do
[ "$rp" = "$pport" ] && csp=$pcsp
while read -r h path want; do
  nrows=$((nrows + 1))
  hdrs=$(curl -s -o /dev/null -D - -H "Host: $h" "http://127.0.0.1:$rp$path" | tr -d '\r')
  code=$(printf '%s\n' "$hdrs" | head -n1 | cut -d' ' -f2)
  loc=$(printf '%s\n' "$hdrs" | sed -n 's/^[Ll]ocation: //p')
  sec "$h$path" "$hdrs"
  echo "$h$path -> $code $loc"
  { [ "$code" = 301 ] && [ "$loc" = "$want" ]; } || { echo "FAIL $h$path: want 301 $want"; fail=1; }
done <<<"$redirects"
done
csp="content-security-policy: frame-ancestors 'none'"
[ "$nrows" -gt 0 ] || { echo "FAIL: no redirect rows"; fail=1; }
# A host that is not an old address must serve, not redirect.
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Host: www.ascomply.com" "http://127.0.0.1:$port/blog/x")
[ "$code" = 200 ] || { echo "FAIL: www.ascomply.com/blog/x answered $code, want 200"; fail=1; }
# The 404 must really be the maps rule, or the check above proves nothing.
curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/assets/index-abc123.js.map" | grep -qx 404 \
  || { echo "FAIL: *.map did not answer 404"; fail=1; }
exit "$fail"
