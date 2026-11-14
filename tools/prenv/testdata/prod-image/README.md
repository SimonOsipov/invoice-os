Read-only captures from production on 2026-11-13 (gcloud, account with read access), trimmed to the fields `prod-image.sh pin` reads. Nothing secret: env, secrets and service accounts are dropped.
- gateway-images-list.json: `gcloud artifacts docker images list africa-south1-docker.pkg.dev/ascomply-preview/images/gateway --include-tags --format json` (298 versions: tagged indexes, their untagged image and attestation children).
- gateway-service.json: `gcloud run services describe gateway --project ascomply-prod --region africa-south1 --format json` (template image by tag; status.traffic 100 % on gateway-00383-biy, rollback on gateway-00380-mac, which has no `percent`).
- revision-gateway-00383-biy.json, revision-gateway-00380-mac.json: `gcloud run revisions describe <rev> ... --format json` (status.imageDigest is an untagged child manifest of a tagged index).
- migrate-job.json: `gcloud run jobs describe migrate --project ascomply-prod --region africa-south1 --format json`.
