# Golden token fixture (M2-05)

`golden_token.json` pins the **Supabase GoTrue JWT claim contract** so the M2-05 mock
issuer cannot drift from what real GoTrue emits. The M2-05 contract test asserts that a
mock-minted token matches the **shape** (claim keys + types) of `decoded_header` /
`decoded_claims` — never the values (`iss`, `kid`, `exp`, `sub`, `session_id` differ).

## What's here (and what isn't)

- `decoded_header` — real GoTrue header: `alg` (**ES256**), `kid`, `typ`.
- `decoded_claims` — real GoTrue claim set: `iss, sub, aud=authenticated, exp, iat, email,
  phone, app_metadata{provider,providers}, user_metadata, role=authenticated, aal, amr,
  session_id, is_anonymous`. Our custom `app_metadata.tenant_id` is added later via a
  Custom Access Token Hook (M8) and is contract-compatible (nested in the existing object).
- `jwks` — the project's public JWKS (EC public key only; safe to commit).
- **The raw signed JWT is intentionally omitted** — this is a public repo, and the decoded
  shape is sufficient to detect drift. (If a real end-to-end verify is ever wanted, re-mint
  a throwaway token locally; do not commit it.)

## How it was captured (repeat at M8-05 against the production project)

```sh
SUPA_URL="https://<ref>.supabase.co"
APIKEY="<publishable key from Settings -> API Keys>"

# 1) mint a token for a confirmed test user (created via dashboard -> Auth -> Add user)
curl -s -X POST "$SUPA_URL/auth/v1/token?grant_type=password" \
  -H "apikey: $APIKEY" -H "Authorization: Bearer $APIKEY" \
  -H "Content-Type: application/json" \
  -d '{"email":"<user>","password":"<pw>"}'   # -> .access_token

# 2) snapshot the JWKS
curl -s "$SUPA_URL/auth/v1/.well-known/jwks.json"
```

Then decode the JWT header/payload, drop the raw token + signature, and write the shape +
JWKS into `golden_token.json`.

Source project: `sbgzrerzxemextujwpil` (org FiscalBridge, free tier), signing alg ES256.
Captured 2026-07-07.
