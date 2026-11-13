#!/usr/bin/env bash
# prod-image.sh — production images, pushed by digest and deployed by digest (SECAUD-06). No subcommand reads a registry tag.
#
#   prod-image.sh push <dir> <image> <build args>...   build, push $REGISTRY/<image> untagged, record its digest in <dir>/<image>
#   prod-image.sh ref <dir> <image>                    print $REGISTRY/<image>@<digest> from <dir>/<image>
#   prod-image.sh refs <dir> <image>...                print "<image> <ref>" per image; all resolve or none print
#
# Needs REGISTRY. push needs docker buildx, and jq for the build's metadata.
set -euo pipefail

die() { echo "::error::$*" >&2; exit 1; }

# Same loop as nightly.yml's: 3 tries, 20 s apart.
retry() { for n in 1 2 3; do "$@" && return 0; if [ "$n" -lt 3 ]; then sleep 20; fi; done; return 1; }

digest_re='^sha256:[0-9a-f]{64}$'

check_image() { [[ "$1" =~ ^[a-z][a-z-]*$ ]] || die "image name '$1' is not lower-case letters and hyphens"; }

# Removes the half-made builder before each retry, not before the first try.
create_builder() {
  if [ "${tried:-0}" -gt 0 ]; then docker buildx rm prod-image >/dev/null 2>&1 || true; fi
  tried=$((${tried:-0} + 1))
  docker buildx create --name prod-image --driver docker-container \
    --driver-opt image=mirror.gcr.io/moby/buildkit:buildx-stable-1 --bootstrap >&2
}

# The file holds exactly the digest and a newline (72 bytes); its text goes in $digest.
read_digest() {
  local dir=$1 image=$2 f
  f=$dir/$image
  [ -f "$f" ] || die "$image: no digest recorded in $f"
  [ "$(wc -c <"$f" | tr -d ' ')" = 72 ] && [ "$(wc -l <"$f" | tr -d ' ')" = 1 ] || die "$image: $f is not one sha256 digest line"
  digest=$(<"$f")
  [[ "$digest" =~ $digest_re ]] || die "$image: $f does not hold a sha256 digest"
}

cmd_push() {
  [ $# -ge 2 ] || die "usage: prod-image.sh push <dir> <image> <build args>..."
  local dir=$1 image=$2
  shift 2
  check_image "$image"
  : "${REGISTRY:?}"
  if ! docker buildx inspect prod-image >/dev/null 2>&1; then
    retry create_builder || die "$image: could not create the prod-image builder"
  fi
  local meta
  meta=$(mktemp)
  docker buildx build --builder prod-image --provenance=false --sbom=false \
    --output "type=image,name=$REGISTRY/$image,push-by-digest=true,push=true" \
    --metadata-file "$meta" "$@" >&2 || die "$image: build or push failed"
  local digest name
  digest=$(jq -r '.["containerimage.digest"] // empty' "$meta" 2>/dev/null) || digest=
  name=$(jq -r '.["image.name"] // empty' "$meta" 2>/dev/null) || name=
  [[ "$digest" =~ $digest_re ]] || die "$image: the build metadata has no valid containerimage.digest"
  [ "$name" = "$REGISTRY/$image" ] || die "$image: the build metadata names '$name', want $REGISTRY/$image"
  mkdir -p "$dir"
  printf '%s\n' "$digest" >"$dir/$image.tmp"
  mv "$dir/$image.tmp" "$dir/$image"
  printf '%s\n' "$digest"
}

cmd_ref() {
  [ $# -eq 2 ] || die "usage: prod-image.sh ref <dir> <image>"
  check_image "$2"
  : "${REGISTRY:?}"
  local digest
  read_digest "$1" "$2"
  printf '%s/%s@%s\n' "$REGISTRY" "$2" "$digest"
}

cmd_refs() {
  [ $# -ge 2 ] || die "usage: prod-image.sh refs <dir> <image>..."
  local dir=$1 image digest out=
  shift
  for image in "$@"; do check_image "$image"; done
  : "${REGISTRY:?}"
  for image in "$@"; do
    read_digest "$dir" "$image"
    out+="$image $REGISTRY/$image@$digest"$'\n'
  done
  printf '%s' "$out"
}

[ $# -ge 1 ] || die "usage: prod-image.sh push|ref|refs ..."
sub=$1
shift
case "$sub" in
  push | ref | refs) [ -n "${REGISTRY:-}" ] || die "REGISTRY is not set"; "cmd_$sub" "$@" ;;
  *) die "unknown subcommand '$sub'" ;;
esac
