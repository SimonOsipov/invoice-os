#!/usr/bin/env bash
# scripts/ci/docling-bench.sh <repo-relative-fixture> [runs]
#
# EXTR-03-09's repeatable measurement: per-document conversion latency (p50/p95/max)
# and peak resident memory, against an already-running container. Mirrors
# docling-canary.sh: everything is asked of the container in python3 over
# localhost, so it works with --network none.
#
# The first conversion pays converter construction and model load. It is run once
# and reported SEPARATELY as the cold start; the p95 is over the warm runs that
# follow. Reporting a cold first request inside the percentile would describe a
# number no steady-state caller ever sees.
#
# Peak RSS comes from cgroup v2 memory.peak, which is the container's high-water
# mark since boot -- not a sample, so a spike between polls cannot hide from it.
set -euo pipefail

CONTAINER="${DOCLING_BENCH_CONTAINER:-docling-bench}"
PORT="${DOCLING_CANARY_PORT:-8080}"

fixture="${1:?usage: docling-bench.sh <repo-relative-fixture> [runs]}"
runs="${2:-20}"

# A non-positive count collects no samples, and the p95 index below then reads samples[-1].
if ! [[ "$runs" =~ ^[1-9][0-9]*$ ]]; then
  echo "::error::runs must be a positive integer, got '$runs'"
  exit 1
fi

if [ ! -f "$fixture" ]; then
  echo "::error::$fixture does not exist (path is relative to the repo root)"
  exit 1
fi

docker cp "$fixture" "$CONTAINER":/tmp/bench-fixture
before="$(docker exec "$CONTAINER" cat /sys/fs/cgroup/memory.peak)"

docker exec -e RUNS="$runs" "$CONTAINER" python3 -c "
import json, os, statistics, time, urllib.request

runs = int(os.environ['RUNS'])
with open('/tmp/bench-fixture', 'rb') as f:
    body = f.read()

def once():
    req = urllib.request.Request(
        'http://localhost:${PORT}/v1/read',
        data=body,
        headers={'Content-Type': 'application/pdf'},
        method='POST',
    )
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=900) as resp:
        payload = json.load(resp)
    return time.perf_counter() - t0, payload

cold, payload = once()
pages = len(payload['pages'])
tokens = sum(len(p['tokens']) for p in payload['pages'])
if pages == 0:
    raise SystemExit('the fixture converted to zero pages; a latency over nothing measures nothing')
floor = int(os.environ.get('MIN_TOKENS', '1'))
if tokens < floor:
    raise SystemExit(
        f'the fixture yielded {tokens} token(s), want at least {floor} -- timing a document the '
        'reader finds nothing in measures the failure path, not the work. Set MIN_TOKENS=0 to '
        'benchmark an empty document deliberately.'
    )

samples = [once()[0] for _ in range(runs)]
samples.sort()

def pct(p):
    # Nearest-rank on a sorted sample: index ceil(p/100 * n) - 1, no interpolation.
    k = max(1, int(-(-p * len(samples) // 100)))
    return samples[k - 1]

print(json.dumps({
    'runs': len(samples),
    'pages': pages,
    'tokens': tokens,
    'cold_start_s': round(cold, 3),
    'p50_s': round(pct(50), 3),
    'p95_s': round(pct(95), 3),
    'max_s': round(samples[-1], 3),
    'mean_s': round(statistics.fmean(samples), 3),
    'p95_s_per_page': round(pct(95) / pages, 3),
}, indent=2))
"

after="$(docker exec "$CONTAINER" cat /sys/fs/cgroup/memory.peak)"
echo "peak_rss_bytes_before=$before"
echo "peak_rss_bytes_after=$after"
awk -v b="$before" -v a="$after" 'BEGIN { printf "peak_rss: %.2f MiB before, %.2f MiB after\n", b/1048576, a/1048576 }'
