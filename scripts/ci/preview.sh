#!/usr/bin/env bash
# preview.sh — one preview (a PR's, or the nightly's) in ascomply-preview (GKE-11). Needs gcloud signed in as deploy-preview.
# The shared resources (network, Cloud SQL, preview-db job, shared secrets) are invoice-os-infra preview.tf.
#
#   preview.sh urls <pr|nightly>                  print <name>_url=<address> for each public site
#   preview.sh image <pr|nightly> <image> <sha>   print the image reference
#   preview.sh secrets <pr|nightly>               create this preview's secrets that do not exist yet
#   preview.sh bucket <pr|nightly>                create this preview's documents bucket if it does not exist
#   preview.sh database <pr|nightly>              drop and recreate its database (pr_<pr>, pr_0 for nightly)
#   preview.sh migrate <pr|nightly> <sha>         deploy the migrate job and run it
#   preview.sh grant <pr|nightly>                 run the migrate job for the auth.users grant only
#   preview.sh deploy <pr|nightly> <sha> <svc>... deploy services, four at a time
#   preview.sh token <pr|nightly>                 print the preview's gateway token
#   preview.sh teardown <pr|nightly>              delete everything this preview owns
#   preview.sh prs                        print "<pr|nightly> <created>" for each preview that owns a resource
#   preview.sh cap                        print open=<n> and full=true|false (full at 3 previews or more)
#   preview.sh invariants                 fail when preview holds a production secret or reaches prod
#   preview.sh budget                     print the connections one PR fleet can hold at peak
set -euo pipefail

PROJECT=ascomply-preview
PROJECT_NUMBER=944095690109
REGION=africa-south1
REGISTRY=africa-south1-docker.pkg.dev/ascomply-preview/images/pr
RUNTIME=run-preview@ascomply-preview.iam.gserviceaccount.com
LABEL=preview-pr
gc=(--project "$PROJECT" --region "$REGION" --quiet)

BACKENDS=(gateway auth tenancy portfolio invoice validation dashboard submission notifications worker docling)
SITES=(app landing library blog ops-console support-console)
# The secrets each PR generates or composes; preview.tf holds the shared ones.
PR_SECRETS=(gateway-token s2s-token gotrue-jwt-keys gotrue-jwt-secret database-superuser-url database-migration-url
  database-app-url database-worker-url database-reader-url database-gateway-url database-auth-url mock-issuer-key)

die() {
  echo "::error::$*" >&2
  exit 1
}

# name_arg: a PR number without a leading zero, or exactly nightly.
name_arg() {
  [[ "${1:-}" =~ ^[1-9][0-9]*$ || "${1:-}" == nightly ]] || die "a PR number or nightly is required, got '${1:-}'"
}

# suffix <name>: the resource-name part (pr-7, nightly). dbname <name>: preview-db accepts only pr_<N>, so nightly is pr_0.
suffix() { if [ "$1" = nightly ]; then echo nightly; else echo "pr-$1"; fi; }
dbname() { if [ "$1" = nightly ]; then echo pr_0; else echo "pr_$1"; fi; }

url() { printf 'https://%s-%s-%s.%s.run.app' "$1" "$(suffix "$2")" "$PROJECT_NUMBER" "$REGION"; }

cmd_urls() {
  local pr=$1
  echo "gateway_url=$(url gateway "$pr")"
  echo "app_url=$(url app "$pr")"
  echo "landing_url=$(url landing "$pr")"
  echo "library_url=$(url library "$pr")/library"
  echo "blog_url=$(url blog "$pr")/blog"
  echo "ops_console_url=$(url ops-console "$pr")"
  echo "support_console_url=$(url support-console "$pr")"
}

image() { printf '%s/%s:%s-%s' "$REGISTRY" "$2" "$(suffix "$1")" "$3"; }

secret() { printf '%s-%s' "$(suffix "$1")" "$2"; }

# The tables are case functions: bash 3.2 has no associative arrays, and only a function crosses export -f.
max_instances() {
  case "$1" in
    auth | worker) echo 1 ;;
    portfolio | dashboard | notifications | validation | submission) echo 2 ;;
    *) echo 3 ;;
  esac
}

# pool_conns <secret>: pool_max_conns of a pooled DSN secret; empty for any other secret.
pool_conns() {
  case "$1" in
    database-app-url | database-reader-url | database-gateway-url) echo 1 ;;
    database-worker-url) echo 6 ;;
  esac
}

gotrue_pool() { echo 3; }

shared() { gcloud secrets versions access latest --secret "$1" --project "$PROJECT"; }

# put <name> <pr>: stdin is the value; argv never carries it.
# put stores stdin without its trailing newline: a service reads the secret byte for byte.
put() {
  local value
  value=$(cat)
  printf '%s' "$value" | gcloud secrets create "$1" --project "$PROJECT" --replication-policy user-managed --locations "$REGION" \
    --labels "$LABEL=$2" --data-file=- >/dev/null
  echo "created $1"
}

# repool <secret> <n>: add a version when the latest DSN's pool_max_conns is not n.
repool() {
  local cur
  cur=$(gcloud secrets versions access latest --secret "$1" --project "$PROJECT")
  [[ "$cur" == *"pool_max_conns=$2" ]] && return 0
  printf '%s' "$cur" | sed "s/pool_max_conns=[0-9]*/pool_max_conns=$2/" |
    gcloud secrets versions add "$1" --project "$PROJECT" --data-file=- >/dev/null
  echo "updated $1"
}

cmd_secrets() {
  local pr=$1 db have name ip pool
  db=$(dbname "$pr")
  have=$(gcloud secrets list --project "$PROJECT" --filter "labels.$LABEL=$pr" --format 'value(name)')
  ip=$(gcloud sql instances describe preview --project "$PROJECT" --format 'value(ipAddresses[0].ipAddress)')
  [ -n "$ip" ] || die "the preview Cloud SQL instance has no private IP"
  dsn() { printf 'postgres://%s:%s@%s:5432/%s?sslmode=require%s' "$1" "$2" "$ip" "$db" "$3"; }

  for name in "${PR_SECRETS[@]}"; do
    if grep -qx "$(secret "$pr" "$name")" <<<"$have"; then
      pool=$(pool_conns "$name")
      [ -z "$pool" ] || repool "$(secret "$pr" "$name")" "$pool"
      continue
    fi
    case "$name" in
      gateway-token | s2s-token | gotrue-jwt-secret) openssl rand -hex 32 ;;
      gotrue-jwt-keys | mock-issuer-key) go run ./tools/prenv jwk-es256 ;;
      database-superuser-url) dsn postgres "$(shared postgres-password)" '' ;;
      database-migration-url) dsn invoice_migrator "$(shared migrator-password)" '' ;;
      database-app-url) dsn invoice_app "$(shared app-password)" "&pool_max_conns=$(pool_conns database-app-url)" ;;
      database-worker-url) dsn invoice_app "$(shared app-password)" "&pool_max_conns=$(pool_conns database-worker-url)" ;;
      database-reader-url) dsn invoice_tenant_reader "$(shared reader-password)" "&pool_max_conns=$(pool_conns database-reader-url)" ;;
      database-gateway-url) dsn invoice_gateway "$(shared gateway-password)" "&pool_max_conns=$(pool_conns database-gateway-url)" ;;
      database-auth-url) dsn supabase_auth_admin "$(shared auth-admin-password)" '' ;;
    esac | put "$(secret "$pr" "$name")" "$pr"
  done
}

bucket() { printf 'gs://ascomply-preview-%s' "$(suffix "$1")"; }

cmd_bucket() {
  local pr=$1
  if gcloud storage buckets describe "$(bucket "$pr")" --project "$PROJECT" >/dev/null 2>&1; then
    return
  fi
  gcloud storage buckets create "$(bucket "$pr")" --project "$PROJECT" --location "$REGION" \
    --uniform-bucket-level-access --public-access-prevention --soft-delete-duration 0 >/dev/null
  gcloud storage buckets update "$(bucket "$pr")" --project "$PROJECT" --update-labels "$LABEL=$pr" >/dev/null
  echo "created $(bucket "$pr")"
}

# database <name> <action>: the preview-db job drops the database WITH (FORCE), then creates it for recreate.
database() {
  gcloud run jobs execute preview-db "${gc[@]}" --wait --update-env-vars "PR_DB=$(dbname "$1"),ACTION=$2"
}

# env_file <pr> <svc> <file>: the plain environment of one service, as gcloud --env-vars-file YAML.
env_file() {
  local pr=$1 svc=$2 file=$3 issuers
  {
    echo "DEPLOY_ENV: $(suffix "$pr")"
    echo "ENVIRONMENT: development"
    case "$svc" in
      gateway)
        issuers=$(jq -cn --arg i "urn:ascomply:auth:$(suffix "$pr")" --arg u "$(url auth "$pr")/.well-known/jwks.json" \
          '[{issuer: $i, jwks_url: $u}]')
        # The PR build serves a mock issuer on its own port; its own GoTrue is the second issuer.
        echo "GATEWAY_MOCK_ISSUER: 'true'"
        echo "AUTH_ISSUER: https://mock.ascomply.dev"
        echo "AUTH_JWKS_URL: http://127.0.0.1:8080/.well-known/jwks.json"
        echo "AUTH_ADDITIONAL_ISSUERS: '$issuers'"
        echo "AUTH_SITE_URL: $(url landing "$pr")"
        echo "CORS_ALLOWED_ORIGINS: $(url app "$pr"),$(url landing "$pr"),$(url ops-console "$pr"),$(url support-console "$pr"),$(url blog "$pr")"
        for callee in tenancy portfolio invoice validation submission dashboard notifications docling auth worker; do
          echo "$(tr 'a-z-' 'A-Z_' <<<"$callee")_URL: $(url "$callee" "$pr")"
        done
        ;;
      auth)
        echo "API_EXTERNAL_URL: $(url auth "$pr")"
        echo "GOTRUE_JWT_ISSUER: urn:ascomply:auth:$(suffix "$pr")"
        echo "GOTRUE_SITE_URL: $(url landing "$pr")"
        echo "GOTRUE_DISABLE_SIGNUP: 'false'"
        # No mail: a preview confirms a sign-up at once.
        echo "GOTRUE_MAILER_AUTOCONFIRM: 'true'"
        echo "GOTRUE_DB_MAX_POOL_SIZE: '$(gotrue_pool)'"
        ;;
      invoice) echo "VALIDATION_URL: $(url validation "$pr")" ;;
      submission) echo "GATEWAY_URL: $(url gateway "$pr")" ;;
      worker)
        echo "APP_ADAPTER: mock"
        echo "EXTRACTOR: docling"
        echo "VALIDATION_URL: $(url validation "$pr")"
        echo "GATEWAY_URL: $(url gateway "$pr")"
        echo "DOCLING_URL: $(url docling "$pr")"
        echo "AUTH_URL: $(url auth "$pr")"
        ;;
      blog) echo "VITE_GATEWAY_URL: $(url gateway "$pr")" ;;
    esac
    case "$svc" in
      invoice | submission | worker)
        echo "AI_FAKE: 'true'"
        echo "JEV_FAKE: 'true'"
        echo "DOCUMENT_BUCKET: ascomply-preview-$(suffix "$pr")"
        echo "DOCUMENT_ENDPOINT: https://storage.googleapis.com"
        echo "DOCUMENT_REGION: auto"
        ;;
    esac
  } >"$file"
}

# secrets_of <pr> <svc>: the --set-secrets value of one service.
secrets_of() {
  local pr=$1 s=()
  case "$2" in
    gateway) s=(GATEWAY_TOKEN=gateway-token DATABASE_GATEWAY_URL=database-gateway-url DATABASE_MIGRATION_URL=database-migration-url
      GATEWAY_MOCK_ISSUER_KEY=mock-issuer-key) ;;
    auth) s=(DATABASE_URL=database-auth-url GOTRUE_JWT_KEYS=gotrue-jwt-keys GOTRUE_JWT_SECRET=gotrue-jwt-secret) ;;
    tenancy | portfolio | dashboard | notifications) s=(DATABASE_URL=database-app-url GATEWAY_TOKEN=gateway-token) ;;
    validation) s=(DATABASE_URL=database-app-url GATEWAY_TOKEN=gateway-token S2S_TOKEN=s2s-token) ;;
    invoice) s=(DATABASE_URL=database-app-url GATEWAY_TOKEN=gateway-token S2S_TOKEN=s2s-token) ;;
    submission) s=(DATABASE_URL=database-app-url GATEWAY_TOKEN=gateway-token) ;;
    worker) s=(DATABASE_URL=database-worker-url DATABASE_READER_URL=database-reader-url GATEWAY_TOKEN=gateway-token S2S_TOKEN=s2s-token GOTRUE_JWT_KEYS=gotrue-jwt-keys) ;;
  esac
  local out=() kv
  for kv in ${s[@]+"${s[@]}"}; do out+=("${kv%%=*}=$(secret "$pr" "${kv#*=}"):latest"); done
  case "$2" in
    invoice | submission | worker)
      out+=(DOCUMENT_ACCESS_KEY_ID=document-access-key-id:latest DOCUMENT_SECRET_ACCESS_KEY=document-secret-key:latest)
      ;;
  esac
  local IFS=,
  printf '%s' "${out[*]-}"
}

# deploy_one <name> <sha> <svc>: one Cloud Run service, <svc>-<suffix>, from this commit's image.
deploy_one() {
  local pr=$1 sha=$2 svc=$3 envf secrets min=0 cpu=1 memory=512Mi args
  case "$svc" in
    worker) min=1 memory=768Mi ;;
    docling) cpu=2 memory=4Gi ;;
    app | landing | library | blog | ops-console | support-console) memory=256Mi ;;
  esac
  envf=$(mktemp)
  env_file "$pr" "$svc" "$envf"
  args=(--image "$(image "$pr" "$svc" "$sha")" --service-account "$RUNTIME" --labels "$LABEL=$pr"
    --env-vars-file "$envf" --min-instances "$min" --max-instances "$(max_instances "$svc")" --cpu "$cpu" --memory "$memory"
    --no-cpu-boost --ingress all)
  secrets=$(secrets_of "$pr" "$svc")
  [ -z "$secrets" ] || args+=(--set-secrets "$secrets")
  case "$svc" in
    # The gateway checks GATEWAY_TOKEN and JWTs itself; the sites are public.
    gateway | app | landing | library | blog | ops-console | support-console) args+=(--no-invoker-iam-check) ;;
    *) args+=(--no-allow-unauthenticated) ;;
  esac
  case "$svc" in
    docling | app | landing | library | blog | ops-console | support-console) ;;
    *) args+=(--network preview --subnet run --vpc-egress private-ranges-only) ;;
  esac
  # The worker runs River and the sweepers between requests.
  [ "$svc" != worker ] || args+=(--no-cpu-throttling)
  gcloud run deploy "$svc-$(suffix "$pr")" "${gc[@]}" "${args[@]}"
  rm -f "$envf"
}

cmd_deploy() {
  local pr=$1 sha=$2 failed=0 svc
  shift 2
  export -f deploy_one env_file secrets_of url image secret suffix dbname max_instances gotrue_pool
  export PROJECT PROJECT_NUMBER REGION REGISTRY RUNTIME LABEL
  # Four at a time: africa-south1 allows 20 vCPU of allocation per minute per project.
  # shellcheck disable=SC2016 # the child bash expands its own arguments
  printf '%s\n' "$@" | xargs -P 4 -I{} bash -c 'gc=(--project "$PROJECT" --region "$REGION" --quiet); deploy_one "$0" "$1" "$2" >"deploy-$2.log" 2>&1' \
    "$pr" "$sha" {} || failed=1
  for svc in "$@"; do
    echo "--- $svc"
    cat "deploy-$svc.log"
  done
  [ "$failed" = 0 ] || die "a service did not deploy; its log is above"
}

# migrate_job <name> <sha>: the per-preview migrate job; it bootstraps, migrates and seeds the database.
migrate_job() {
  local pr=$1 sha=$2 envf
  envf=$(mktemp)
  {
    echo "DEPLOY_ENV: $(suffix "$pr")"
    echo "ENVIRONMENT: development"
    echo "GATEWAY_DB_BOOTSTRAP: 'true'"
    echo "DOCUMENT_BUCKET: ascomply-preview-$(suffix "$pr")"
    echo "DOCUMENT_ENDPOINT: https://storage.googleapis.com"
    echo "DOCUMENT_REGION: auto"
  } >"$envf"
  gcloud run jobs deploy "migrate-$(suffix "$pr")" "${gc[@]}" --image "$(image "$pr" migrate "$sha")" \
    --service-account "$RUNTIME" --labels "$LABEL=$pr" --env-vars-file "$envf" --max-retries 0 --task-timeout 900s \
    --network preview --subnet run --vpc-egress private-ranges-only \
    --set-secrets "DATABASE_SUPERUSER_URL=$(secret "$pr" database-superuser-url):latest,DATABASE_MIGRATION_URL=$(secret "$pr" database-migration-url):latest,DATABASE_URL=$(secret "$pr" database-app-url):latest,MIGRATOR_PASSWORD=migrator-password:latest,APP_PASSWORD=app-password:latest,READER_PASSWORD=reader-password:latest,GATEWAY_PASSWORD=gateway-password:latest,AUTH_ADMIN_PASSWORD=auth-admin-password:latest,DOCUMENT_ACCESS_KEY_ID=document-access-key-id:latest,DOCUMENT_SECRET_ACCESS_KEY=document-secret-key:latest"
  rm -f "$envf"
  gcloud run jobs execute "migrate-$(suffix "$pr")" "${gc[@]}" --wait
}

# grant <name>: GoTrue creates auth.users on its first boot, after the migrate job; the
# second run migrates nothing, seeds nothing and grants.
cmd_grant() {
  gcloud run jobs execute "migrate-$(suffix "$1")" "${gc[@]}" --wait --update-env-vars GATEWAY_DB_BOOTSTRAP=false
}

cmd_teardown() {
  local pr=$1 svc name failed=0
  for svc in "${BACKENDS[@]}" "${SITES[@]}"; do
    if gcloud run services describe "$svc-$(suffix "$pr")" "${gc[@]}" >/dev/null 2>&1; then
      gcloud run services delete "$svc-$(suffix "$pr")" "${gc[@]}" || failed=1
    fi
  done
  if gcloud run jobs describe "migrate-$(suffix "$pr")" "${gc[@]}" >/dev/null 2>&1; then
    gcloud run jobs delete "migrate-$(suffix "$pr")" "${gc[@]}" || failed=1
  fi
  database "$pr" drop || failed=1
  if gcloud storage buckets describe "$(bucket "$pr")" --project "$PROJECT" >/dev/null 2>&1; then
    gcloud storage rm --recursive "$(bucket "$pr")" --project "$PROJECT" --quiet || failed=1
  fi
  for name in $(gcloud secrets list --project "$PROJECT" --filter "labels.$LABEL=$pr" --format 'value(name)'); do
    gcloud secrets delete "$name" --project "$PROJECT" --quiet || failed=1
  done
  [ "$failed" = 0 ] || die "teardown of PR $pr left something behind; the errors are above"
  echo "PR $pr: preview deleted"
}

# prs: every PR that owns a service, a job, a bucket or a secret, with the oldest creation time.
cmd_prs() {
  {
    gcloud run services list "${gc[@]}" --filter "metadata.labels.$LABEL:*" \
      --format "value(metadata.labels.$LABEL,metadata.creationTimestamp)"
    gcloud run jobs list "${gc[@]}" --filter "metadata.labels.$LABEL:*" \
      --format "value(metadata.labels.$LABEL,metadata.creationTimestamp)"
    gcloud storage buckets list --project "$PROJECT" --filter "labels.$LABEL:*" \
      --format "value(labels.$LABEL,creation_time)"
    gcloud secrets list --project "$PROJECT" --filter "labels.$LABEL:*" \
      --format "value(labels.$LABEL,createTime)"
  } | sort -k1,1n -k2,2 | awk '!seen[$1]++ {print $1, $2}'
}

# budget: Cloud SQL connections one fleet holds at peak (instances x pool), from the deploy and secrets tables.
# cap: how many previews exist (a nightly counts) and whether three are open (ci-preview.md).
cmd_cap() {
  local prs n
  prs=$(cmd_prs)
  n=$(grep -c . <<<"$prs" || true)
  echo "open=$n"
  [ "$n" -ge 3 ] && echo "full=true" || echo "full=false"
}

cmd_budget() {
  local svc kv name pool n leader total=0
  for svc in "${BACKENDS[@]}"; do
    for kv in $(secrets_of 0 "$svc" | tr , ' '); do
      name=${kv#*=pr-0-}
      name=${name%:latest}
      pool=$(pool_conns "$name")
      [ -n "$pool" ] || continue
      n=$(($(max_instances "$svc") * pool))
      printf '%-14s %-22s %s\n' "$svc" "$name" "$n"
      total=$((total + n))
    done
  done
  # River's leader connection, outside the pool (internal/platform/db/leader.go).
  leader=$(max_instances worker)
  printf '%-14s %-22s %s\n' worker leader "$leader"
  n=$(($(max_instances auth) * $(gotrue_pool)))
  printf '%-14s %-22s %s\n' auth gotrue-pool "$n"
  echo "fleet $((total + leader + n))"
}

# invariants: GKE-11 AC6.
cmd_invariants() {
  local bad=0 names envs held token code
  names=$(gcloud secrets list --project "$PROJECT" --format 'value(name)')
  if grep -Ei 'openrouter|sentry' <<<"$names"; then
    echo "::error::ascomply-preview holds an OpenRouter or Sentry secret (listed above)."
    bad=1
  fi
  envs=$(gcloud run services list "${gc[@]}" --format json |
    jq -r '.[] | .metadata.name as $s | .spec.template.spec.containers[].env[]? | "\($s) \(.name)"')
  if grep -E ' (OPENROUTER_API_KEY|SENTRY_DSN|VITE_SENTRY_DSN|SENTRY_AUTH_TOKEN)$' <<<"$envs"; then
    echo "::error::a preview service sets an OpenRouter or Sentry variable (listed above)."
    bad=1
  fi
  # testIamPermissions answers for any caller; preview's deployer must hold none of these in prod.
  token=$(gcloud auth print-access-token)
  code=$(curl -sS -o "$RUNNER_TEMP/held.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    -d '{"permissions":["run.services.get","run.services.update","run.jobs.run","secretmanager.versions.access","cloudsql.instances.get","storage.buckets.get","iam.serviceAccounts.actAs","resourcemanager.projects.get"]}' \
    https://cloudresourcemanager.googleapis.com/v3/projects/ascomply-prod:testIamPermissions)
  case "$code" in
    200) held=$(jq -r '.permissions // [] | join(", ")' "$RUNNER_TEMP/held.json") ;;
    403) held="" ;; # no access to the project at all
    *) die "testIamPermissions on ascomply-prod answered HTTP $code" ;;
  esac
  if [ -n "$held" ]; then
    echo "::error::the preview deployer holds $held in ascomply-prod."
    bad=1
  fi
  # A deploy trigger outside GitHub Actions needs Cloud Build or Cloud Deploy.
  if gcloud services list --project "$PROJECT" --enabled --format 'value(config.name)' |
    grep -Ex 'cloudbuild.googleapis.com|clouddeploy.googleapis.com'; then
    echo "::error::ascomply-preview has Cloud Build or Cloud Deploy enabled (listed above); deploys run only from GitHub Actions."
    bad=1
  fi
  [ "$bad" = 0 ] || exit 1
  echo "Preview invariants hold: no OpenRouter or Sentry secret or variable, no role in ascomply-prod, no deploy trigger outside GitHub Actions."
}

case "${1:-}" in
  urls) name_arg "${2:-}"; cmd_urls "$2" ;;
  image) name_arg "${2:-}"; image "$2" "${3:?image}" "${4:?sha}" ;;
  secrets) name_arg "${2:-}"; cmd_secrets "$2" ;;
  bucket) name_arg "${2:-}"; cmd_bucket "$2" ;;
  database) name_arg "${2:-}"; database "$2" recreate ;;
  migrate) name_arg "${2:-}"; migrate_job "$2" "${3:?sha}" ;;
  grant) name_arg "${2:-}"; cmd_grant "$2" ;;
  deploy) name_arg "${2:-}"; [ "$#" -ge 4 ] || die "usage: preview.sh deploy <pr|nightly> <sha> <svc>..."; shift; cmd_deploy "$@" ;;
  token) name_arg "${2:-}"; gcloud secrets versions access latest --secret "$(secret "$2" gateway-token)" --project "$PROJECT" ;;
  teardown) name_arg "${2:-}"; cmd_teardown "$2" ;;
  prs) cmd_prs ;;
  cap) cmd_cap ;;
  invariants) cmd_invariants ;;
  budget) cmd_budget ;;
  *) die "usage: preview.sh urls|image|secrets|bucket|database|migrate|grant|deploy|token|teardown|prs|cap|invariants|budget" ;;
esac
