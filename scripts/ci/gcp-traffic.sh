#!/usr/bin/env bash
# gcp-traffic.sh — traffic moves of the production Cloud Run fleet (GKE-09). Needs gcloud signed in
# as deploy-prod, PROJECT and REGION.
#
#   gcp-traffic.sh snapshot <file>        write "<service> <revision>" for the revision serving each service
#   gcp-traffic.sh promote <file>         send 100 % to the latest revision; tag each revision in <file> `rollback`
#   gcp-traffic.sh restore <file>         send 100 % of each service in <file> to its revision
#   gcp-traffic.sh untag <tag>            remove a deploy tag from every service
#   gcp-traffic.sh rollback <file>        send 100 % to the revision tagged `rollback`; write "<service> <from> <to>"
set -euo pipefail

SERVICES=(gateway auth tenancy portfolio invoice validation dashboard submission notifications worker docling
  app landing library blog ops-console support-console)

: "${PROJECT:?}" "${REGION:?}"
gc=(--project "$PROJECT" --region "$REGION" --quiet)

# serving <service>: the revision that holds 100 % of the traffic.
serving() {
  local json rev
  json=$(gcloud run services describe "$1" "${gc[@]}" --format=json)
  rev=$(jq -r '[.status.traffic[] | select(.percent == 100)][0] | if . == null then "" elif .latestRevision then "latest" else .revisionName end' <<<"$json")
  if [ "$rev" = "latest" ]; then
    rev=$(jq -r '.status.latestReadyRevisionName // ""' <<<"$json")
  fi
  if [ -z "$rev" ]; then
    echo "::error::$1 has no revision at 100 % traffic." >&2
    return 1
  fi
  printf '%s\n' "$rev"
}

# tagged <service> <tag>: the revision that carries <tag>, or nothing.
tagged() {
  gcloud run services describe "$1" "${gc[@]}" --format=json |
    jq -r --arg t "$2" '[.status.traffic[] | select(.tag == $t)][0].revisionName // ""'
}

case "${1:-}" in
  snapshot)
    out="${2:?usage: gcp-traffic.sh snapshot <file>}"
    : >"$out"
    for svc in "${SERVICES[@]}"; do
      printf '%s %s\n' "$svc" "$(serving "$svc")" >>"$out"
    done
    ;;
  promote)
    in="${2:?usage: gcp-traffic.sh promote <file>}"
    while read -r svc rev; do
      gcloud run services update-traffic "$svc" "${gc[@]}" --to-latest --update-tags "rollback=$rev" >/dev/null
      echo "$svc: 100 % to latest; rollback = $rev"
    done <"$in"
    ;;
  restore)
    in="${2:?usage: gcp-traffic.sh restore <file>}"
    failed=0
    while read -r svc rev; do
      gcloud run services update-traffic "$svc" "${gc[@]}" --to-revisions "$rev=100" >/dev/null ||
        { echo "::error::$svc: traffic did not move back to $rev." >&2; failed=1; }
    done <"$in"
    exit "$failed"
    ;;
  untag)
    tag="${2:?usage: gcp-traffic.sh untag <tag>}"
    for svc in "${SERVICES[@]}"; do
      if [ -n "$(tagged "$svc" "$tag")" ]; then
        gcloud run services update-traffic "$svc" "${gc[@]}" --remove-tags "$tag" >/dev/null
      fi
    done
    ;;
  rollback)
    out="${2:?usage: gcp-traffic.sh rollback <file>}"
    : >"$out"
    plan=()
    for svc in "${SERVICES[@]}"; do
      prev=$(tagged "$svc" rollback)
      if [ -z "$prev" ]; then
        echo "::error::$svc has no revision tagged rollback; nothing moved." >&2
        exit 1
      fi
      plan+=("$svc $(serving "$svc") $prev")
    done
    # Every service has a target before the first one moves; the gateway and the services it guards move together.
    for line in "${plan[@]}"; do
      read -r svc cur prev <<<"$line"
      gcloud run services update-traffic "$svc" "${gc[@]}" --to-revisions "$prev=100" >/dev/null
      printf '%s %s %s\n' "$svc" "$cur" "$prev" | tee -a "$out"
    done
    ;;
  *)
    echo "usage: gcp-traffic.sh snapshot|promote|restore|untag|rollback <arg>" >&2
    exit 2
    ;;
esac
