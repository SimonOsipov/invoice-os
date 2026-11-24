// Intuit's sign-in for the nightly QuickBooks spec. Every Intuit selector lives here.
import type { Page } from '@playwright/test'

import { requireEnv } from '../mail/mailChecks'
import { getReceived, listReceived, parseCreatedAt, type Fetch, type MailItem } from '../mail/resendClient'

const POLL_MS = 5_000
const SCOPE = 'com.intuit.quickbooks.accounting' // intuitScope in internal/erp/quickbooks.go
const CODE_WAIT_MS = 120_000

export type IntuitEnv = { INTUIT_LOGIN_EMAIL: string; INTUIT_LOGIN_PASSWORD: string; RESEND_READ_KEY: string }

export function intuitLoginEnv(): IntuitEnv {
  try {
    return requireEnv('INTUIT_LOGIN_EMAIL', 'INTUIT_LOGIN_PASSWORD', 'RESEND_READ_KEY')
  } catch (e) {
    throw new Error(`environment: ${(e as Error).message}`)
  }
}

// Each problem names the part, never its value (the state and client id are secrets).
export function consentUrlProblems(url: string, gatewayUrl: string): string[] {
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return ['the consent URL is not a URL']
  }
  const q = u.searchParams
  const problems: string[] = []
  if (u.host !== 'appcenter.intuit.com') problems.push('host is not appcenter.intuit.com')
  if (u.pathname !== '/connect/oauth2') problems.push('path is not /connect/oauth2')
  if (q.get('response_type') !== 'code') problems.push('response_type is not code')
  if (!(q.get('scope') ?? '').split(' ').includes(SCOPE)) problems.push(`scope lacks ${SCOPE}`)
  if (q.get('redirect_uri') !== `${gatewayUrl}/erp/callback`) problems.push('redirect_uri is not the gateway /erp/callback')
  if (!q.get('state')) problems.push('state is empty')
  if (!q.get('client_id')) problems.push('client_id is empty')
  return problems
}

export function intuitSignInCode(text: string | null | undefined): string | null {
  return /(?<![\d#])\d{6}(?!\d)/.exec(text ?? '')?.[0] ?? null
}

// No subject match: Intuit's subject is unmeasured and may carry the code.
export function pickIntuitCodeMail(items: MailItem[], to: string, since: Date): MailItem | null {
  const want = to.toLowerCase()
  let best: MailItem | null = null
  for (const m of items) {
    if (!m.to.some((a) => a.toLowerCase() === want)) continue
    const t = parseCreatedAt(m.created_at)
    if (t < since.getTime()) continue
    if (!best || t > parseCreatedAt(best.created_at)) best = m
  }
  return best
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

// The deadline error names no address. A Resend error is polled through until the deadline.
export async function waitForIntuitCode(key: string, to: string, since: Date, deadlineMs: number, fetchFn: Fetch = fetch): Promise<string> {
  const start = Date.now()
  const read = new Set<string>()
  for (;;) {
    try {
      const items = (await listReceived(key, since, fetchFn)).filter((m) => !read.has(m.id))
      const hit = pickIntuitCodeMail(items, to, since)
      if (hit) {
        read.add(hit.id)
        const mail = await getReceived(key, hit.id, fetchFn)
        const code = intuitSignInCode(mail.text ?? mail.html)
        if (code) return code
        continue
      }
    } catch (e) {
      if ((e as { transient?: boolean }).transient !== true) throw e
    }
    if (Date.now() - start >= deadlineMs) throw new Error(`no Intuit sign-in code in ${deadlineMs / 60_000} minutes`)
    await sleep(POLL_MS)
  }
}

const EMAIL = (p: Page) => p.getByLabel(/email|user ?id/i).first()
const PASSWORD = (p: Page) => p.locator('input[type="password"]').first()
const CODE = (p: Page) => p.getByLabel(/code/i).first()
const CAPTCHA = (p: Page) => p.locator('iframe[src*="captcha" i], iframe[title*="captcha" i], [id*="captcha" i]').first()
const button = (p: Page, name: RegExp) => p.getByRole('button', { name }).first()

// Types the password only through fill. Returns when the popup leaves Intuit's sign-in screens.
export async function signInToIntuit(page: Page, env: IntuitEnv): Promise<void> {
  await EMAIL(page).waitFor({ timeout: 60_000 })
  await EMAIL(page).fill(env.INTUIT_LOGIN_EMAIL)
  await button(page, /continue|sign in|next/i).click()
  await PASSWORD(page).waitFor({ timeout: 30_000 })
  await PASSWORD(page).fill(env.INTUIT_LOGIN_PASSWORD)
  const since = new Date()
  await button(page, /sign in|continue/i).click()

  const next = await Promise.race([
    CODE(page).waitFor({ timeout: 60_000 }).then(() => 'code' as const),
    CAPTCHA(page).waitFor({ timeout: 60_000 }).then(() => 'captcha' as const),
    button(page, /connect|authorize/i).waitFor({ timeout: 60_000 }).then(() => 'consent' as const),
  ])
  if (next === 'captcha') throw new Error('Intuit asked for a captcha')
  if (next === 'code') {
    const code = await waitForIntuitCode(env.RESEND_READ_KEY, env.INTUIT_LOGIN_EMAIL, since, CODE_WAIT_MS)
    await CODE(page).fill(code)
    await button(page, /continue|verify|submit|sign in/i).click()
    await button(page, /connect|authorize/i).waitFor({ timeout: 60_000 })
  }
  const picker = page.getByRole('combobox').first()
  if (await picker.isVisible()) await picker.selectOption({ index: 0 })
  await button(page, /connect|authorize/i).click()
}
