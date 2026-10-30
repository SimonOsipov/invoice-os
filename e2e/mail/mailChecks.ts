// Rules of the nightly mail check: budget, fake addresses, scrubbing, text diff and the section file.
import { chmodSync, readFileSync, renameSync, writeFileSync } from 'node:fs'

import { parseCreatedAt } from './resendClient'

export type Sent = { to: string[]; created_at: string }

// A run sends 3 mails and the day stops at 25.
const BUDGET_BEFORE_RUN = 22
const SPEC_FILE = 'e2e/mail/real-mail.spec.ts'

export function nightlySendsToday(sends: Sent[], domain: string, now: Date = new Date()): number {
  const dayStart = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())
  const suffix = `@${domain.toLowerCase()}`
  const today = sends.filter(
    (s) =>
      parseCreatedAt(s.created_at) >= dayStart &&
      s.to.some((a) => a.toLowerCase().startsWith('nightly-') && a.toLowerCase().endsWith(suffix)),
  )
  return today.length
}

export const overBudget = (sends: Sent[], domain: string, now: Date = new Date()): boolean =>
  nightlySendsToday(sends, domain, now) > BUDGET_BEFORE_RUN

export function isFakeAddress(address: string): boolean {
  const domain = address.slice(address.lastIndexOf('@') + 1).toLowerCase()
  return domain === 'example.com' || ['.example.com', '.example', '.test'].some((t) => domain.endsWith(t))
}

export function judgeSends(sends: Sent[], inviteAddr: string, resetAddr: string): { ok: boolean; detail: string } {
  const count = (addr: string) => sends.filter((s) => s.to.some((a) => a.toLowerCase() === addr.toLowerCase())).length
  const invite = count(inviteAddr)
  const reset = count(resetAddr)
  const fake = sends.filter((s) => s.to.some(isFakeAddress)).length
  const detail = `invite sends ${invite} (want 2), reset sends ${reset} (want 1), fake-domain sends ${fake} (want 0), all sends ${sends.length}`
  return { ok: invite === 2 && reset === 1 && fake === 0, detail }
}

export function scrub(s: string): string {
  return s
    .replace(/token=[^&\s"'<>]+/gi, 'token=<redacted>')
    .replace(/eyJ[\w-]+\.[\w-]+\.[\w-]*/g, '<jwt>')
    .replace(/\bre_[A-Za-z0-9_]{8,}/g, '<key>')
    .replace(/[^\s@<>"',;()]+@[^\s@<>"',;()]+\.[A-Za-z]{2,}/g, '<address>')
}

// The position of the first difference, never the text: a mail holds an address and a link.
export function mailTextDiff(want: string, got: string): string | null {
  const w = want.replace(/\s+/g, ' ').trim()
  const g = got.replace(/\s+/g, ' ').trim()
  if (w === g) return null
  let i = 0
  while (i < w.length && i < g.length && w[i] === g[i]) i++
  return `text differs at character ${i} (want ${w.length} characters, got ${g.length})`
}

export type StepSpec = { name: string; needs: string[] }

export const MAIL_STEPS: StepSpec[] = [
  { name: 'mail budget', needs: [] },
  { name: 'invite mail arrives', needs: ['mail budget'] },
  { name: 'invite text matches', needs: ['invite mail arrives'] },
  { name: 'invite page registers the invitee', needs: ['invite mail arrives'] },
  { name: 'password mail arrives', needs: ['invite page registers the invitee'] },
  { name: 'password mail text matches', needs: ['password mail arrives'] },
  { name: 'password link opens the workspace', needs: ['password mail arrives'] },
  { name: 'used invite link is refused', needs: ['invite mail arrives'] },
  { name: 'reset mail arrives', needs: ['mail budget'] },
  { name: 'reset text matches', needs: ['reset mail arrives'] },
  { name: 'reset link sets the password', needs: ['reset mail arrives'] },
  { name: 'sign-in with the new password', needs: ['reset link sets the password'] },
  { name: 'only these mails are real', needs: ['mail budget'] },
]

export type StepResult = { name: string; status: 'passed' | 'failed' | 'skipped'; duration_ms: number; detail?: string }
type Failure = { suite: 'email'; test: string; file: string; message: string }
export type Section = { ok: boolean; steps: StepResult[]; failures: Failure[] }

export class CheckSteps {
  private readonly steps: StepResult[]
  private readonly file: string
  private readonly specs: StepSpec[]

  constructor(file: string, specs: StepSpec[] = MAIL_STEPS) {
    this.file = file
    this.specs = specs
    this.steps = specs.map((s) => ({ name: s.name, status: 'skipped', duration_ms: 0 }))
    this.write()
  }

  async run(name: string, fn: () => Promise<void> | void): Promise<void> {
    const i = this.specs.findIndex((s) => s.name === name)
    if (i < 0) throw new Error(`unknown step ${name}`)
    const blocked = this.specs[i].needs.some((n) => this.steps.find((s) => s.name === n)?.status !== 'passed')
    if (!blocked) {
      const t0 = Date.now()
      try {
        await fn()
        this.steps[i] = { name, status: 'passed', duration_ms: Date.now() - t0 }
      } catch (e) {
        const detail = scrub(e instanceof Error ? e.message : String(e))
        this.steps[i] = { name, status: 'failed', duration_ms: Date.now() - t0, detail }
      }
    }
    this.write()
  }

  section(): Section {
    const failures: Failure[] = this.steps
      .filter((s) => s.status === 'failed')
      .map((s) => ({ suite: 'email', test: s.name, file: SPEC_FILE, message: s.detail ?? '' }))
    const finished = this.steps.every((s) => s.status === 'passed')
    if (!finished && failures.length === 0) {
      failures.push({ suite: 'email', test: 'check', file: SPEC_FILE, message: 'check did not finish' })
    }
    return { ok: finished, steps: this.steps, failures }
  }

  // A rename keeps a kill from leaving half a file.
  private write(): void {
    const tmp = `${this.file}.tmp`
    writeFileSync(tmp, JSON.stringify(this.section(), null, 2))
    renameSync(tmp, this.file)
  }
}

export type MailAccounts = {
  admin: { email: string; password: string; displayName: string; workspaceName: string }
  reset: { email: string; password: string }
}

// Holds passwords: mode 600, and the reader's error names no path or value.
export function writeAccounts(file: string, accounts: MailAccounts): void {
  writeFileSync(file, JSON.stringify(accounts), { mode: 0o600 })
  chmodSync(file, 0o600)
}

export function readAccounts(file: string): MailAccounts {
  try {
    return JSON.parse(readFileSync(file, 'utf8')) as MailAccounts
  } catch {
    throw new Error('the mail accounts file is missing or unreadable')
  }
}

// Names the missing variables, never a value.
export function requireEnv<K extends string>(...names: K[]): Record<K, string> {
  const missing = names.filter((n) => !process.env[n]?.trim())
  if (missing.length > 0) throw new Error(`not set: ${missing.join(', ')}`)
  return Object.fromEntries(names.map((n) => [n, process.env[n]!.trim()])) as Record<K, string>
}
