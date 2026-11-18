// The production consoles sit behind IAP; ctl sends a service-account token to those two origins only.
import { execFileSync } from 'node:child_process'

import type { BrowserContext } from '@playwright/test'

import { CtlError } from './main'

export const IAP_ORIGINS = ['https://ops.ascomply.com', 'https://sup.ascomply.com'] as const
export const CTL_IAP_ACCOUNT = 'ctl-console@ascomply-prod.iam.gserviceaccount.com'
export const isIapOrigin = (url: URL): boolean => (IAP_ORIGINS as readonly string[]).includes(url.origin)

const MINT_URL = `https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/${CTL_IAP_ACCOUNT}:generateAccessToken`

// The gcloud user token goes to iamcredentials only. No message or hint holds a token.
export async function mintIapToken(): Promise<{ token: string; expiresAt: string }> {
  let userToken = ''
  let cause = 'it printed nothing'
  try {
    userToken = execFileSync('gcloud', ['auth', 'print-access-token'], { encoding: 'utf8' }).trim()
  } catch (err) {
    cause = err instanceof Error ? (err.message.split('\n')[0] ?? '') : String(err)
  }
  if (!userToken) {
    throw new CtlError(`could not read a gcloud access token (gcloud auth print-access-token): ${cause}`, 'Run gcloud auth login with the account that holds Token Creator on ctl-console (SECAUD-35-05).', 1)
  }
  const res = await fetch(MINT_URL, {
    method: 'POST',
    headers: { Authorization: `Bearer ${userToken}`, 'Content-Type': 'application/json', 'X-Goog-User-Project': 'ascomply-prod' },
    body: JSON.stringify({ scope: ['openid', 'https://www.googleapis.com/auth/userinfo.email'], lifetime: '3600s' }),
  })
  if (res.status === 403) {
    throw new CtlError(
      `this gcloud account may not mint a token for ${CTL_IAP_ACCOUNT} (403)`,
      `The user grants it: gcloud iam service-accounts add-iam-policy-binding ${CTL_IAP_ACCOUNT} --member=user:<account> --role=roles/iam.serviceAccountTokenCreator --project=ascomply-prod (SECAUD-35-05).`,
      1,
    )
  }
  if (!res.ok) throw new CtlError(`generateAccessToken answered ${res.status}`, 'Retry; if it repeats, read gcloud auth list.', 1)
  const body = (await res.json().catch(() => ({}))) as { accessToken?: unknown; expireTime?: unknown }
  if (typeof body.accessToken !== 'string' || typeof body.expireTime !== 'string') {
    throw new CtlError('generateAccessToken answered without a token', 'Retry; if it repeats, read gcloud auth list.', 1)
  }
  return { token: body.accessToken, expiresAt: body.expireTime }
}

// Self-contained: iapRouteScript serialises it, so it names nothing from module scope.
export async function installIapRoute(context: BrowserContext, token: string, origins: readonly string[]): Promise<void> {
  await context.route(
    (u) => origins.includes(u.origin),
    async (route) => {
      const origin = new URL(route.request().url()).origin
      let res
      try {
        res = await route.fetch({ headers: { ...route.request().headers(), authorization: `Bearer ${token}` }, maxRedirects: 0 })
      } catch {
        return route.abort('failed')
      }
      const loc = res.status() >= 300 && res.status() < 400 ? res.headers()['location'] : undefined
      // never let the token follow a redirect off its origin
      if (loc !== undefined && new URL(loc, route.request().url()).origin !== origin) {
        return route.fulfill({ status: 401, contentType: 'text/plain', body: `ctl: IAP refused the token for ${origin}; run ctl login again` })
      }
      return route.fulfill({ response: res })
    },
  )
}

// One function expression for playwright-cli run-code --filename.
export const iapRouteScript = (token: string): string =>
  `async page => { const install = ${installIapRoute.toString()}; await install(page.context(), ${JSON.stringify(token)}, ${JSON.stringify(IAP_ORIGINS)}) }`
