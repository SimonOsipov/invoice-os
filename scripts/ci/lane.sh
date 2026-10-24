#!/usr/bin/env bash
# lane.sh - decides the lane (library, blog or full) of a PR (pr, pr-e2e, classify). A SHARED_WITH_BLOG library file is full.
# Writes the verdict to stdout and $GITHUB_OUTPUT. Exit 0, 1 on a bad FILTER_E2E, 2 on a usage error.
set -euo pipefail

LIBRARY_PREFIX='frontend/library/'
BLOG_PREFIX='frontend/blog/'
# library files the blog compiles; frontend/blog/src/package.test.ts pins the list
SHARED_WITH_BLOG=(
  frontend/library/src/components/SceneView.tsx
  frontend/library/src/icons.tsx
  frontend/library/src/player.ts
  frontend/library/src/scene.ts
  frontend/library/src/timing.ts
  frontend/library/src/types.ts
)
# Inputs of cmd/blog: a change needs the fork build and the crawler e2e, so it takes the full lane.
BLOG_SERVER_INPUTS=(frontend/blog/Dockerfile frontend/blog/Dockerfile.dockerignore frontend/blog/index.html)

usage() {
  echo "::error::usage: lane.sh classify | pr | pr-e2e" >&2
  exit 2
}

# emit <key=value>...: stdout first, $GITHUB_OUTPUT as the last action.
emit() {
  printf '%s\n' "$@"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf '%s\n' "$@" >> "$GITHUB_OUTPUT"
  fi
}

# classify: paths on stdin; a small lane only when a path is read, every path starts with that one lane's prefix and none is in SHARED_WITH_BLOG.
classify() {
  local path shared this seen=0 lane=""
  while IFS= read -r path || [ -n "$path" ]; do
    [ -n "$path" ] || continue
    seen=1
    for shared in "${SHARED_WITH_BLOG[@]}" "${BLOG_SERVER_INPUTS[@]}"; do
      if [ "$path" = "$shared" ]; then echo full; return 0; fi
    done
    case "$path" in
      "$LIBRARY_PREFIX"*) this=library ;;
      "$BLOG_PREFIX"*) this=blog ;;
      *) echo full; return 0 ;;
    esac
    if [ -n "$lane" ] && [ "$lane" != "$this" ]; then echo full; return 0; fi
    lane=$this
  done
  if [ "$seen" = 1 ]; then echo "$lane"; else echo full; fi
}

# pr_lane: the lane of the merge commit's diff against its first parent; any git failure is full.
pr_lane() {
  local paths
  if ! paths=$(git diff --no-renames --name-only HEAD^1 HEAD 2>/dev/null); then
    echo full
    return 0
  fi
  printf '%s\n' "$paths" | classify
}

cmd=${1:-}
case "$cmd" in
  classify)
    classify
    ;;
  pr)
    emit "lane=$(pr_lane)"
    ;;
  pr-e2e)
    lane=$(pr_lane)
    case "${FILTER_E2E-}" in
      true) if [ "$lane" != full ]; then e2e=false; else e2e=true; fi ;;
      false) e2e=false ;;
      *)
        echo "::error::pr-e2e: FILTER_E2E must be true or false, got '${FILTER_E2E-}'" >&2
        exit 1
        ;;
    esac
    emit "lane=$lane" "e2e=$e2e"
    ;;
  *) usage ;;
esac
