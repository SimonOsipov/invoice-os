#!/usr/bin/env bash
# suite-summary.sh <log-dir> <suite>...: names each failure of the failed suites. Always exits 0.
set -uo pipefail
L=$1; shift
CAP=25

# list <suite> <kind>: items on stdin, deduped, capped
list() {
  awk -v s="$1" -v k="$2" -v cap="$CAP" -v L="$L" '
    !seen[$0]++ { n++; if (n <= cap) print s " " k ": " $0 }
    END { if (n > cap) print s " " k ": +" (n - cap) " more, see " L "/" s ".log" }'
}

# Awk string forms only: BSD awk does not match octal escapes in a regex literal.
# An unprefixed line is the single-package e2e run of suites.sh.
# pnpm writes U+2009 where vitest writes ASCII spaces.
extract_unit() {
  awk '
    { gsub("\033\\[[0-9;]*m", ""); gsub("\r", ""); gsub("\342\200\211", " "); l = $0; p = ""
      if (match(l, /^[^ ]+ test:([A-Za-z0-9_-]+(:[A-Za-z0-9_-]+)*:)?/)) { p = substr(l, 1, index(l, " test:") - 1); l = substr(l, RLENGTH + 1) }
      else p = "e2e"
      if (l ~ /^ +FAIL  /) { sub(/^ +FAIL  /, "", l); split(l, a, " "); if (a[1] ~ /\.[cm]?[jt]sx?$/) print "file: " p " " a[1] }
      else if (l ~ /^ +❯ [^ ]+ \([0-9]+ tests?[ |)]/) { sub(/^ +❯ /, "", l); split(l, a, " "); print "file: " p " " a[1] }
      else if ($0 ~ /^ ERROR  [^ ]+@[^ ]+ test:/) { split($0, a, " "); print "package: " a[2] }
      else if ($0 ~ /^ ERR_PNPM_RECURSIVE_RUN_FIRST_FAIL  [^ ]+@/) { split($0, a, " "); print "package: " a[2] }
    }' "$1"
}

extract_go() {
  awk -F'\t' '
    /^FAIL\t/ { print "package: " $2 }
    /^--- FAIL: / { split($0, a, " "); print "test: " a[3] }' "$1"
}

for s in "$@"; do
  f=$L/$s.log
  [ -f "$f" ] || continue
  # Functions, not an inline $( case ): bash 3.2 cannot parse a ) inside $( ).
  case $s in
    unit) out=$(extract_unit "$f") ;;
    go | db) out=$(extract_go "$f") ;;
    *) out= ;;
  esac
  if [ -z "$out" ]; then
    echo "$s: no failing file, package or test named, see $L/$s.log"
    continue
  fi
  for k in package file test; do
    sed -n "s/^$k: //p" <<< "$out" | list "$s" "$k"
  done
done
exit 0
