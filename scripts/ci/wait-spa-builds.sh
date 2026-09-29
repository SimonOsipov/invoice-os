#!/usr/bin/env bash
# wait-spa-builds.sh <expected-sha> <url>...
# Waits until each SPA serves /health and its /build.txt names <expected-sha>. GET only.
# /health is answered from memory, so only /build.txt proves which commit is live.
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "::error::usage: wait-spa-builds.sh <expected-sha> <url>..." >&2
  exit 2
fi
expected="$1"
shift
if [ -z "$expected" ]; then
  echo "::error::wait-spa-builds.sh: the expected sha (argument 1) is empty." >&2
  exit 2
fi

pos=1
for url in "$@"; do
  pos=$((pos + 1))
  if [ -z "$url" ]; then
    echo "::error::wait-spa-builds.sh: argument $pos (a url) is empty; the SPA URL output was not set." >&2
    exit 1
  fi
done

for url in "$@"; do
  echo "Waiting for $url on build $expected ..."
  ok=0
  seen=""
  # 600s: a cold build takes that long.
  for _ in $(seq 1 120); do
    code=$(curl -fsS -o /dev/null -w '%{http_code}' "$url/health" 2>/dev/null || echo 000)
    if [ "$code" = "200" ]; then
      seen=$(curl -fsS --max-time 10 "$url/build.txt" 2>/dev/null | tr -d '[:space:]' || echo '')
      if [ "$seen" = "$expected" ]; then echo "  healthy on $seen"; ok=1; break; fi
    fi
    sleep 5
  done
  if [ "$ok" != "1" ]; then
    echo "::error::$url did not serve build $expected within 600s (last seen: '${seen:-none}'). 'none' means /build.txt is absent -- the image predates the stamped Dockerfile layer or the stamp step did not run; any other value means the new image never replaced the old one. Running E2E here would drive the wrong frontend."
    exit 1
  fi
done
