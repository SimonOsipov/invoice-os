#!/usr/bin/env bash
# prod-image.sh — production images, pushed by digest and deployed by digest (SECAUD-06).
#
#   prod-image.sh push <dir> <image> <build args>...   build, push $REGISTRY/<image> untagged, record its digest in <dir>/<image>
#   prod-image.sh ref <dir> <image>                    print $REGISTRY/<image>@<digest> from <dir>/<image>
#   prod-image.sh refs <dir> <image>...                print "<image> <ref>" per image; all resolve or none print
#   prod-image.sh pin <image>                          tag keep-<hex> on a serving or rollback version near the cleanup limit
#
# Needs REGISTRY; pin also PROJECT and REGION. push needs docker buildx, and jq for the build's metadata.
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

# Equals keep_count of the keep-recent policy on images (invoice-os-infra terraform/registry.tf).
KEEP_VERSIONS=50
# Ten legs of margin, so a failed pin never decides alone.
PIN_RANK=$((KEEP_VERSIONS - 10))

# Sets $json to the output of a retried read; a read that fails 3 times, or prints other than $shape
# (a jq test: object, or array of objects), ends pin with exit 0 (fail open).
pin_read() {
  local shape=$1
  shift
  if ! json=$(retry "$@"); then
    echo "::error::pin $pin_image: $* failed 3 times; nothing pinned, the build goes on" >&2
    exit 0
  fi
  if ! jq -e "$shape" >/dev/null 2>&1 <<<"$json"; then
    echo "::error::pin $pin_image: $* printed no usable JSON; nothing pinned, the build goes on" >&2
    exit 0
  fi
}

pin_object='type == "object"'
pin_array='type == "array" and all(type == "object")'

# Adds the digests the jobs or service use to $protected, one per line.
pin_job_image() {
  pin_read "$pin_object" gcloud run jobs describe "$1" --project "$PROJECT" --region "$REGION" --format json
  protected+=$(jq -r '.spec.template.spec.template.spec.containers[0].image // empty' <<<"$json")$'\n'
}

pin_service_images() {
  pin_read "$pin_object" gcloud run services describe "$1" --project "$PROJECT" --region "$REGION" --format json
  protected+=$(jq -r '.spec.template.spec.containers[0].image // empty' <<<"$json")$'\n'
  local revs rev
  revs=$(jq -r '([.status.traffic[]? | select((.percent // 0) > 0) | .revisionName]
    + [.spec.traffic[]? | select(.tag == "rollback") | .revisionName]) | unique | map(select(. != null))[]' <<<"$json")
  for rev in $revs; do
    pin_read "$pin_object" gcloud run revisions describe "$rev" --project "$PROJECT" --region "$REGION" --format json
    protected+=$(jq -r '.status.imageDigest // empty' <<<"$json")$'\n'
  done
}

cmd_pin() {
  [ $# -eq 1 ] || die "usage: prod-image.sh pin <image>"
  pin_image=$1
  check_image "$pin_image"
  : "${REGISTRY:?}"
  [ -n "${PROJECT:-}" ] || die "PROJECT is not set"
  [ -n "${REGION:-}" ] || die "REGION is not set"
  local protected="" json ref d
  case "$pin_image" in
    migrate) pin_job_image migrate; pin_job_image migrate-bootstrap ;;
    deploycheck) pin_job_image deploy-check ;;
    *) pin_service_images "$pin_image" ;;
  esac
  # Only $REGISTRY/<image>@sha256:...; a tag reference is never deleted and another registry is not ours.
  local digests=
  while read -r ref; do
    [[ "$ref" == "$REGISTRY/$pin_image@"* ]] || continue
    d=${ref#*@}
    [[ "$d" =~ $digest_re ]] && digests+="$d"$'\n'
  done <<<"$protected"
  digests=$(printf '%s' "$digests" | sort -u)
  [ -n "$digests" ] || return 0

  pin_read "$pin_array" gcloud artifacts docker images list "$REGISTRY/$pin_image" --include-tags --format json
  local listing=$json state tag_cmd
  while read -r d; do
    [ -n "$d" ] || continue
    # missing | tagged | the rank: the larger of the rows at least as new by createTime and by updateTime.
    state=$(jq -r --arg d "$d" '(map(select(.version == $d)) | first) as $r
      | if $r == null then "missing" elif (($r.tags // []) | length) > 0 then "tagged"
        else [(map(select(.createTime >= $r.createTime)) | length), (map(select(.updateTime >= $r.updateTime)) | length)] | max end' <<<"$listing")
    case "$state" in
      missing) echo "::warning::$pin_image@$d is not in the registry" >&2; continue ;;
      tagged) continue ;;
    esac
    [ "$state" -ge "$PIN_RANK" ] || continue
    tag_cmd=(gcloud artifacts docker tags add "$REGISTRY/$pin_image@$d" "$REGISTRY/$pin_image:keep-${d#sha256:}")
    if "${tag_cmd[@]}" >&2; then
      echo "kept $pin_image@$d: rank $state, pinned at $PIN_RANK of $KEEP_VERSIONS"
    elif [ "$state" -ge $((KEEP_VERSIONS - 1)) ]; then
      die "pin $pin_image: tags add for @$d at rank $state failed; it would be deleted by this push; the leg stops. Run: ${tag_cmd[*]}"
    else
      echo "::error::pin $pin_image: tags add for @$d at rank $state failed; the build goes on. Run: ${tag_cmd[*]}" >&2
    fi
  done <<<"$digests"
}

[ $# -ge 1 ] || die "usage: prod-image.sh push|ref|refs|pin ..."
sub=$1
shift
case "$sub" in
  push | ref | refs | pin) [ -n "${REGISTRY:-}" ] || die "REGISTRY is not set"; "cmd_$sub" "$@" ;;
  *) die "unknown subcommand '$sub'" ;;
esac
