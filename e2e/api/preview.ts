import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

const GATEWAY = /^https:\/\/gateway-(pr-[1-9][0-9]*|nightly)-944095690109\.africa-south1\.run\.app\/?$/

// 'pr-<N>' or 'nightly', from GATEWAY_URL; the naming scripts/ci/preview.sh uses.
export function previewSuffix(): string {
  const url = process.env.GATEWAY_URL?.trim() ?? ''
  const m = GATEWAY.exec(url)
  if (!m) throw new Error(`preview.ts: GATEWAY_URL ${url} is not a preview gateway`)
  return m[1]
}

// One secret of this preview. The value stays in memory: never log or write it.
export function previewSecret(name: string, version = 'latest'): string {
  return execFileSync(
    'gcloud',
    ['secrets', 'versions', 'access', version, '--secret', `${previewSuffix()}-${name}`, '--project', 'ascomply-preview'],
    { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] },
  )
}

// run.app URL of one service in this preview; also its ID-token audience.
export function serviceUrl(service: string): string {
  return `https://${service}-${previewSuffix()}-944095690109.africa-south1.run.app`
}

interface ExternalAccount {
  audience: string
  subject_token_type: string
  token_url: string
  service_account_impersonation_url: string
  credential_source: { url: string; headers?: Record<string, string> }
}

async function step(name: string, res: Response): Promise<Record<string, unknown>> {
  const text = await res.text()
  if (!res.ok) throw new Error(`googleIdToken: ${name} answered ${res.status}: ${text}`)
  return JSON.parse(text) as Record<string, unknown>
}

// Google ID token for a Cloud Run audience. CI mints it from the federated credential
// (gcloud cannot, and deploy-preview cannot sign its own); a laptop uses gcloud.
const idTokens = new Map<string, Promise<string>>()

export function googleIdToken(audience: string): Promise<string> {
  let memo = idTokens.get(audience)
  if (!memo) {
    memo = mintIdToken(audience)
    idTokens.set(audience, memo)
    // A failed mint must not poison a retry.
    const mint = memo
    mint.catch(() => {
      if (idTokens.get(audience) === mint) idTokens.delete(audience)
    })
  }
  return memo
}

async function mintIdToken(audience: string): Promise<string> {
  const file = process.env.GOOGLE_APPLICATION_CREDENTIALS
  const cred = file ? (JSON.parse(readFileSync(file, 'utf8')) as Partial<ExternalAccount> & { type?: string }) : undefined
  if (!cred || cred.type !== 'external_account') {
    return execFileSync('gcloud', ['auth', 'print-identity-token'], { encoding: 'utf8' }).trim()
  }
  const c = cred as ExternalAccount
  const oidc = await step('github oidc', await fetch(c.credential_source.url, { headers: c.credential_source.headers }))
  const sts = await step(
    'sts token exchange',
    await fetch(c.token_url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({
        grant_type: 'urn:ietf:params:oauth:grant-type:token-exchange',
        audience: c.audience,
        scope: 'https://www.googleapis.com/auth/cloud-platform',
        requested_token_type: 'urn:ietf:params:oauth:token-type:access_token',
        subject_token: String(oidc.value),
        subject_token_type: c.subject_token_type,
      }),
    }),
  )
  const iam = await step(
    'iam generateIdToken',
    await fetch(c.service_account_impersonation_url.replace(':generateAccessToken', ':generateIdToken'), {
      method: 'POST',
      headers: { Authorization: `Bearer ${String(sts.access_token)}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ audience, includeEmail: true }),
    }),
  )
  return String(iam.token)
}
