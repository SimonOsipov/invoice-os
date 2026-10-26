#!/usr/bin/env bash
# preview.sh — one PR's preview in ascomply-preview (GKE-11). Needs gcloud signed in as deploy-preview.
# The shared resources (network, Cloud SQL, preview-db job, shared secrets) are invoice-os-infra preview.tf.
#
#   preview.sh urls <pr>                  print <name>_url=<address> for each public site
#   preview.sh image <pr> <image> <sha>   print the image reference
#   preview.sh secrets <pr>               create this PR's secrets that do not exist yet
#   preview.sh bucket <pr>                create this PR's documents bucket if it does not exist
#   preview.sh database <pr>              drop and recreate pr_<pr>
#   preview.sh migrate <pr> <sha>         deploy the migrate job and run it
#   preview.sh grant <pr>                 run the migrate job for the auth.users grant only
#   preview.sh deploy <pr> <sha> <svc>... deploy services, four at a time
#   preview.sh token <pr>                 print the PR's gateway token
#   preview.sh teardown <pr>              delete everything this PR owns
#   preview.sh prs                        print "<pr> <created>" for each PR that owns a service
#   preview.sh invariants                 fail when preview holds a production secret or reaches prod
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
  database-app-url database-worker-url database-reader-url database-gateway-url database-auth-url)

die() {
  echo "::error::$*" >&2
  exit 1
}

pr_arg() {
  [[ "${1:-}" =~ ^[0-9]+$ ]] || die "a PR number is required, got '${1:-}'"
}

url() { printf 'https://%s-pr-%s-%s.%s.run.app' "$1" "$2" "$PROJECT_NUMBER" "$REGION"; }

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

image() { printf '%s/%s:pr-%s-%s' "$REGISTRY" "$2" "$1" "$3"; }

secret() { printf 'pr-%s-%s' "$1" "$2"; }

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

cmd_secrets() {
  local pr=$1 db="pr_$1" have name ip
  have=$(gcloud secrets list --project "$PROJECT" --filter "labels.$LABEL=$pr" --format 'value(name)')
  ip=$(gcloud sql instances describe preview --project "$PROJECT" --format 'value(ipAddresses[0].ipAddress)')
  [ -n "$ip" ] || die "the preview Cloud SQL instance has no private IP"
  dsn() { printf 'postgres://%s:%s@%s:5432/%s?sslmode=require%s' "$1" "$2" "$ip" "$db" "$3"; }

  for name in "${PR_SECRETS[@]}"; do
    if grep -qx "$(secret "$pr" "$name")" <<<"$have"; then
      continue
    fi
    # Pools: about 30 connections per PR fleet against the preview instance's 100.
    case "$name" in
      gateway-token | s2s-token | gotrue-jwt-secret) openssl rand -hex 32 ;;
      gotrue-jwt-keys) go run ./tools/prenv jwk-es256 ;;
      database-superuser-url) dsn postgres "$(shared postgres-password)" '' ;;
      database-migration-url) dsn invoice_migrator "$(shared migrator-password)" '' ;;
      database-app-url) dsn invoice_app "$(shared app-password)" '&pool_max_conns=2' ;;
      database-worker-url) dsn invoice_app "$(shared app-password)" '&pool_max_conns=6' ;;
      database-reader-url) dsn invoice_tenant_reader "$(shared reader-password)" '&pool_max_conns=1' ;;
      database-gateway-url) dsn invoice_gateway "$(shared gateway-password)" '&pool_max_conns=2' ;;
      database-auth-url) dsn supabase_auth_admin "$(shared auth-admin-password)" '' ;;
    esac | put "$(secret "$pr" "$name")" "$pr"
  done
}

bucket() { printf 'gs://ascomply-preview-pr-%s' "$1"; }

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

# database <pr> <action>: the preview-db job drops pr_<N> WITH (FORCE), then creates it for recreate.
database() {
  gcloud run jobs execute preview-db "${gc[@]}" --wait --update-env-vars "PR_DB=pr_$1,ACTION=$2"
}

# env_file <pr> <svc> <file>: the plain environment of one service, as gcloud --env-vars-file YAML.
env_file() {
  local pr=$1 svc=$2 file=$3 issuers
  {
    echo "DEPLOY_ENV: pr-$pr"
    echo "ENVIRONMENT: development"
    case "$svc" in
      gateway)
        issuers=$(jq -cn --arg i "urn:ascomply:auth:pr-$pr" --arg u "$(url auth "$pr")/.well-known/jwks.json" \
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
        echo "GOTRUE_JWT_ISSUER: urn:ascomply:auth:pr-$pr"
        echo "GOTRUE_SITE_URL: $(url landing "$pr")"
        echo "GOTRUE_DISABLE_SIGNUP: 'false'"
        # No mail: a preview confirms a sign-up at once.
        echo "GOTRUE_MAILER_AUTOCONFIRM: 'true'"
        echo "GOTRUE_DB_MAX_POOL_SIZE: '3'"
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
        echo "DOCUMENT_BUCKET: ascomply-preview-pr-$pr"
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
    gateway) s=(GATEWAY_TOKEN=gateway-token DATABASE_GATEWAY_URL=database-gateway-url DATABASE_MIGRATION_URL=database-migration-url) ;;
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

# deploy_one <pr> <sha> <svc>: one Cloud Run service, <svc>-pr-<N>, from this commit's image.
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
    --env-vars-file "$envf" --min-instances "$min" --max-instances 1 --cpu "$cpu" --memory "$memory"
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
  gcloud run deploy "$svc-pr-$pr" "${gc[@]}" "${args[@]}"
  rm -f "$envf"
}

cmd_deploy() {
  local pr=$1 sha=$2 failed=0 svc
  shift 2
  export -f deploy_one env_file secrets_of url image secret
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

# migrate_job <pr> <sha>: the per-PR migrate job; it bootstraps, migrates and seeds pr_<N>.
migrate_job() {
  local pr=$1 sha=$2 envf
  envf=$(mktemp)
  {
    echo "DEPLOY_ENV: pr-$pr"
    echo "ENVIRONMENT: development"
    echo "GATEWAY_DB_BOOTSTRAP: 'true'"
    echo "DOCUMENT_BUCKET: ascomply-preview-pr-$pr"
    echo "DOCUMENT_ENDPOINT: https://storage.googleapis.com"
    echo "DOCUMENT_REGION: auto"
  } >"$envf"
  gcloud run jobs deploy "migrate-pr-$pr" "${gc[@]}" --image "$(image "$pr" migrate "$sha")" \
    --service-account "$RUNTIME" --labels "$LABEL=$pr" --env-vars-file "$envf" --max-retries 0 --task-timeout 900s \
    --network preview --subnet run --vpc-egress private-ranges-only \
    --set-secrets "DATABASE_SUPERUSER_URL=$(secret "$pr" database-superuser-url):latest,DATABASE_MIGRATION_URL=$(secret "$pr" database-migration-url):latest,DATABASE_URL=$(secret "$pr" database-app-url):latest,MIGRATOR_PASSWORD=migrator-password:latest,APP_PASSWORD=app-password:latest,READER_PASSWORD=reader-password:latest,GATEWAY_PASSWORD=gateway-password:latest,AUTH_ADMIN_PASSWORD=auth-admin-password:latest,DOCUMENT_ACCESS_KEY_ID=document-access-key-id:latest,DOCUMENT_SECRET_ACCESS_KEY=document-secret-key:latest"
  rm -f "$envf"
  gcloud run jobs execute "migrate-pr-$pr" "${gc[@]}" --wait
}

# grant <pr>: GoTrue creates auth.users on its first boot, after the migrate job; the
# second run migrates nothing, seeds nothing and grants.
cmd_grant() {
  gcloud run jobs execute "migrate-pr-$1" "${gc[@]}" --wait --update-env-vars GATEWAY_DB_BOOTSTRAP=false
}

cmd_teardown() {
  local pr=$1 svc name failed=0
  for svc in "${BACKENDS[@]}" "${SITES[@]}"; do
    if gcloud run services describe "$svc-pr-$pr" "${gc[@]}" >/dev/null 2>&1; then
      gcloud run services delete "$svc-pr-$pr" "${gc[@]}" || failed=1
    fi
  done
  if gcloud run jobs describe "migrate-pr-$pr" "${gc[@]}" >/dev/null 2>&1; then
    gcloud run jobs delete "migrate-pr-$pr" "${gc[@]}" || failed=1
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
  urls) pr_arg "${2:-}"; cmd_urls "$2" ;;
  image) pr_arg "${2:-}"; image "$2" "${3:?image}" "${4:?sha}" ;;
  secrets) pr_arg "${2:-}"; cmd_secrets "$2" ;;
  bucket) pr_arg "${2:-}"; cmd_bucket "$2" ;;
  database) pr_arg "${2:-}"; database "$2" recreate ;;
  migrate) pr_arg "${2:-}"; migrate_job "$2" "${3:?sha}" ;;
  grant) pr_arg "${2:-}"; cmd_grant "$2" ;;
  deploy) pr_arg "${2:-}"; [ "$#" -ge 4 ] || die "usage: preview.sh deploy <pr> <sha> <svc>..."; shift; cmd_deploy "$@" ;;
  token) pr_arg "${2:-}"; gcloud secrets versions access latest --secret "$(secret "$2" gateway-token)" --project "$PROJECT" ;;
  teardown) pr_arg "${2:-}"; cmd_teardown "$2" ;;
  prs) cmd_prs ;;
  invariants) cmd_invariants ;;
  *) die "usage: preview.sh urls|image|secrets|bucket|database|migrate|grant|deploy|token|teardown|prs|invariants" ;;
esac
