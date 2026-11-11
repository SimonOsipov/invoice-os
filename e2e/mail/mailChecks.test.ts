import { chmodSync, mkdtempSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { describe, expect, it, vi } from 'vitest'

import { brandLayoutFacts, CheckSteps, isFakeAddress, judgeBrandLayout, judgeSends, MAIL_STEPS, mailTextDiff, overBudget, overBudgetFor, readAccounts, readinessBudgetProblem, readinessMailEnv, requireEnv, scrub, writeAccounts, type Section } from './mailChecks'

const NOW = new Date('2026-10-30T13:00:00Z')
const DOMAIN = 'inbox.mailbox.app'
const TODAY = '2026-10-30 00:00:00.000000+00'
const YESTERDAY = '2026-10-29 23:59:59.999999+00'

const send = (to: string, created_at = TODAY) => ({ to: [to], created_at })
const nightly = (n: number, created_at = TODAY) =>
  Array.from({ length: n }, (_, i) => send(`nightly-${i}-invite@${DOMAIN}`, created_at))
const prod = Array.from({ length: 5 }, (_, i) => send(`customer${i}@ascomply.com`))

describe('overBudget', () => {
  it("leaves room for the run's 5 mails", () => {
    expect([20, 21, 25, 26].map((n) => overBudget([...nightly(n), ...prod], DOMAIN, NOW))).toEqual([false, true, true, true])
  })

  it('counts the signup and blog addresses', () => {
    const sends = Array.from({ length: 21 }, (_, i) => send(`nightly-1-${i % 2 ? 'signup' : 'blog'}@${DOMAIN}`))
    expect(overBudget(sends, DOMAIN, NOW)).toBe(true)
  })

  it("ignores another domain's nightly- name", () => {
    const other = Array.from({ length: 30 }, () => send('nightly-1-invite@other.test'))
    expect(overBudget(other, DOMAIN, NOW)).toBe(false)
  })

  it('ignores a name on the domain that is not nightly-', () => {
    const other = Array.from({ length: 30 }, (_, i) => send(`buyer${i}@${DOMAIN}`))
    expect(overBudget(other, DOMAIN, NOW)).toBe(false)
  })

  it('ignores mail from before 00:00 UTC', () => {
    expect(overBudget(nightly(30, YESTERDAY), DOMAIN, NOW)).toBe(false)
  })
})

describe('overBudgetFor', () => {
  it('leaves room for a readiness run and the email run', () => {
    expect([17, 18].map((n) => overBudgetFor(nightly(n), DOMAIN, 8, NOW))).toEqual([false, true])
  })

  it('a readiness run at the threshold leaves the email run inside its budget', () => {
    expect(overBudget(nightly(13 + 7), DOMAIN, NOW)).toBe(false)
    expect(overBudget(nightly(14 + 7), DOMAIN, NOW)).toBe(true)
  })
})

describe('readinessMailEnv', () => {
  it('returns the three values', () => {
    vi.stubEnv('RESEND_READ_KEY', ' k ')
    vi.stubEnv('RESEND_RECEIVING_DOMAIN', ' d.resend.app')
    vi.stubEnv('NIGHTLY_RUN_ID', '1-1 ')
    vi.stubEnv('CHECK_MAIL_ON_OUTCOME', 'success')
    expect(readinessMailEnv()).toEqual({ RESEND_READ_KEY: 'k', RESEND_RECEIVING_DOMAIN: 'd.resend.app', NIGHTLY_RUN_ID: '1-1' })
    vi.unstubAllEnvs()
  })

  it('names every missing variable as an environment error', () => {
    vi.stubEnv('RESEND_READ_KEY', undefined)
    vi.stubEnv('RESEND_RECEIVING_DOMAIN', 'd.resend.app')
    vi.stubEnv('NIGHTLY_RUN_ID', '  ')
    vi.stubEnv('CHECK_MAIL_ON_OUTCOME', 'success')
    expect(() => readinessMailEnv()).toThrow(/^environment: not set: RESEND_READ_KEY, NIGHTLY_RUN_ID$/)
    vi.unstubAllEnvs()
  })

  it.each(['failure', 'skipped', ''])('is an environment error when the mail window outcome is %j', (outcome) => {
    vi.stubEnv('RESEND_READ_KEY', 'k')
    vi.stubEnv('RESEND_RECEIVING_DOMAIN', 'd.resend.app')
    vi.stubEnv('NIGHTLY_RUN_ID', '1-1')
    vi.stubEnv('CHECK_MAIL_ON_OUTCOME', outcome)
    expect(() => readinessMailEnv()).toThrow(/^environment: the real check mail window did not open$/)
    vi.unstubAllEnvs()
  })
})

describe('readinessBudgetProblem', () => {
  it('is null under the threshold', () => {
    expect(readinessBudgetProblem(nightly(13), DOMAIN, NOW)).toBeNull()
  })

  it('names the count and the cap when spent', () => {
    expect(readinessBudgetProblem(nightly(14), DOMAIN, NOW)).toBe(
      'environment: 14 nightly mails went out today; a readiness run and the email run need 12 and the day stops at 25',
    )
  })

  it("counts only today's nightly run sends", () => {
    const sends = [...nightly(13), send('a@example.com'), send(`customer@${DOMAIN}`), send(`customer2@${DOMAIN}`), ...nightly(1, YESTERDAY)]
    expect(readinessBudgetProblem(sends, DOMAIN, NOW)).toBeNull()
  })
})

describe('judgeSends', () => {
  const want = {
    invite: `nightly-1-invite@${DOMAIN}`,
    reset: `nightly-1-reset@${DOMAIN}`,
    signup: `nightly-1-signup@${DOMAIN}`,
    blog: `nightly-1-blog@${DOMAIN}`,
  }
  const base = [
    send(want.invite),
    send(want.invite),
    send(want.reset),
    send(want.signup),
    send(want.blog),
    send('buyer@ascomply.com'),
  ]
  const without = (addr: string) => base.filter((s) => !s.to.includes(addr))
  const rows: [string, ReturnType<typeof send>[], boolean][] = [
    ['2 + 1 + 1 + 1 + production', base, true],
    ['1 invite', base.slice(1), false],
    ['3 invite', [...base, send(want.invite)], false],
    ['0 reset', without(want.reset), false],
    ['2 reset', [...base, send(want.reset)], false],
    ['0 signup', without(want.signup), false],
    ['2 signup', [...base, send(want.signup)], false],
    ['0 blog', without(want.blog), false],
    ['2 blog', [...base, send(want.blog)], false],
    ['a fake address', [...base, send('someone@example.com')], false],
  ]

  it.each(rows)('%s', (_name, sends, ok) => {
    expect(judgeSends(sends, want).ok).toBe(ok)
  })

  it('detail holds no address', () => {
    for (const [, sends, ok] of rows) if (!ok) expect(judgeSends(sends, want).detail).not.toContain('@')
  })
})

describe('judgeBrandLayout', () => {
  const logo = (src: string) => `<img src="${src}" width="36" alt="ASComply">`
  const good = `${logo('https://api.x.test/emails/mark.png')}<td bgcolor="#082f31"><h1>Hi</h1></td>${logo('https://api.x.test/emails/mark.png')}ASComply Africa · Lagos, Nigeria`

  it('accepts the real layout', () => {
    const html = readFileSync(join(__dirname, '../../internal/accountmail/layout.html'), 'utf8')
    expect(judgeBrandLayout(brandLayoutFacts(html))).toBeNull()
  })

  it('accepts the good fixture', () => {
    expect(judgeBrandLayout(brandLayoutFacts(good))).toBeNull()
  })

  it.each([
    ['no logo', good.replaceAll('alt="ASComply"', 'alt="x"'), 'logo'],
    ['src ends /other.png', good.replaceAll('mark.png', 'other.png'), 'src'],
    ['one of two logos wrong', good.replace('mark.png', 'other.png'), 'src'],
    ['hero without h1', good.replace('<h1>Hi</h1>', 'Hi'), 'heading'],
    ['no band before the h1', good.replace('bgcolor="#082f31"', ''), 'heading'],
    ['footer line missing', good.replace('ASComply Africa · Lagos, Nigeria', ''), 'footer'],
  ])('names the missing part: %s', (_name, html, part) => {
    expect(judgeBrandLayout(brandLayoutFacts(html))).toContain(part)
  })

  it('reason holds no mail text', () => {
    const html = good.replace('ASComply Africa · Lagos, Nigeria', 'ada@x.test')
    const reason = judgeBrandLayout(brandLayoutFacts(html)) ?? ''
    expect(reason).not.toBe('')
    expect(reason).not.toContain('@')
  })
})

describe('isFakeAddress', () => {
  it.each([
    ['a@example.com', true],
    ['a@buyer.example.com', true],
    ['a@corp.example', true],
    ['a@engi06.test', true],
    ['a@example.test', true],
    ['A@EXAMPLE.COM', true],
    ['a@ascomply.com', false],
    ['a@notexample.com', false],
    ['a@mytest', false],
  ])('%s', (addr, fake) => expect(isFakeAddress(addr)).toBe(fake))
})

describe('scrub', () => {
  const jwt = ['{"alg":"HS256"}', '{"sub":"1"}', 'signature']
    .map((part) => Buffer.from(part).toString('base64url'))
    .join('.')
  it.each([
    ['sent to nightly-1-invite@inbox.mailbox.app now', 'sent to <address> now'],
    ['link ?token=abc123&type=x', 'link ?token=<redacted>&type=x'],
    [`bearer ${jwt} end`, 'bearer <jwt> end'],
    ['plain text', 'plain text'],
  ])('%s', (input, want) => expect(scrub(input)).toBe(want))
})

describe('mailTextDiff', () => {
  const base = 'Hello there\nyour link is https://gw.test/a?x=1 today'
  it.each([
    ['equal', base, base, false],
    ['whitespace runs only', base, 'Hello   there your link is https://gw.test/a?x=1   today', false],
    ['one word changed', base, base.replace('Hello', 'Howdy'), true],
    ['a different link', base, base.replace('x=1', 'x=2'), true],
    ['one side empty', base, '', true],
  ])('%s', (_name, want, got, differs) => {
    expect(mailTextDiff(want, got) !== null).toBe(differs)
  })

  it('names the position of the first difference', () => {
    expect(mailTextDiff('abc def', 'abc xyz')).toContain('character 4')
  })

  it('holds none of the mail', () => {
    const out = mailTextDiff('Hello ada@secret.test token=tok123', 'Howdy ada@secret.test token=tok123') ?? ''
    for (const s of ['@', 'tok123', 'Hello', 'Howdy']) expect(out).not.toContain(s)
  })
})

describe('CheckSteps', () => {
  const tmpFile = () => join(mkdtempSync(join(tmpdir(), 'mailchecks-')), 'email.json')
  const read = (f: string) => JSON.parse(readFileSync(f, 'utf8')) as Section
  const three = [
    { name: 'a', needs: [] },
    { name: 'b', needs: [] },
    { name: 'c', needs: ['b'] },
    { name: 'd', needs: [] },
  ]

  it('keeps run order and skips a dependent', async () => {
    const f = tmpFile()
    const cs = new CheckSteps(f, three)
    const never = vi.fn()
    await cs.run('a', () => new Promise<void>((r) => setTimeout(r, 30)))
    await cs.run('b', () => {
      throw new Error('boom for ada@secret.test')
    })
    await cs.run('c', never)
    await cs.run('d', () => {})
    const s = cs.section()
    expect(s.steps.map((x) => x.status)).toEqual(['passed', 'failed', 'skipped', 'passed'])
    expect(s.steps[1].detail).toBe('boom for <address>')
    expect(s.ok).toBe(false)
    expect(s.failures).toHaveLength(1)
    expect(s.failures[0]).toMatchObject({
      suite: 'email',
      test: 'b',
      file: 'e2e/mail/real-mail.spec.ts',
      message: 'boom for <address>',
    })
    expect(s.steps[0].duration_ms).toBeGreaterThanOrEqual(20)
    expect(never).not.toHaveBeenCalled()
    expect(read(f)).toEqual(s)
  })

  it('a failed step with nothing skipped is not ok', async () => {
    const cs = new CheckSteps(tmpFile(), [{ name: 'a', needs: [] }, { name: 'b', needs: [] }])
    await cs.run('a', () => {})
    await cs.run('b', () => {
      throw new Error('x')
    })
    expect(cs.section().ok).toBe(false)
  })

  it('a failed budget runs no other step', async () => {
    const f = tmpFile()
    const cs = new CheckSteps(f)
    const body = vi.fn()
    await cs.run('mail budget', () => {
      throw new Error('over')
    })
    for (const s of MAIL_STEPS.slice(1)) await cs.run(s.name, body)
    const s = cs.section()
    expect(body).not.toHaveBeenCalled()
    expect(s.steps.filter((x) => x.status === 'skipped')).toHaveLength(MAIL_STEPS.length - 1)
    expect(s.ok).toBe(false)
  })

  it('the used invite link is not tried when the password link failed', async () => {
    const cs = new CheckSteps(tmpFile())
    const body = vi.fn()
    for (const s of MAIL_STEPS) {
      if (s.name === 'password link opens the workspace') await cs.run(s.name, () => { throw new Error('x') })
      else await cs.run(s.name, body)
    }
    expect(cs.section().steps.find((x) => x.name === 'used invite link is refused')?.status).toBe('skipped')
  })

  it('an unparsable send date throws', () => {
    expect(() => overBudget([{ to: ['nightly-1@x.test'], created_at: 'garbage' }], 'x.test')).toThrow('created_at')
  })

  it('all passed', async () => {
    const cs = new CheckSteps(tmpFile(), three.filter((x) => x.name !== 'c'))
    for (const n of ['a', 'b', 'd']) await cs.run(n, () => {})
    const s = cs.section()
    expect(s.ok).toBe(true)
    expect(s.failures).toEqual([])
    expect(s.steps.some((x) => 'detail' in x)).toBe(false)
  })

  it('a killed check reads as not finished', async () => {
    const f = tmpFile()
    const cs = new CheckSteps(f)
    expect(read(f).steps).toHaveLength(MAIL_STEPS.length)
    await cs.run('mail budget', () => {})
    await cs.run('invite mail arrives', () => {})
    const s = read(f)
    expect(s.steps.filter((x) => x.status === 'passed')).toHaveLength(2)
    expect(s.steps.filter((x) => x.status === 'skipped')).toHaveLength(MAIL_STEPS.length - 2)
    expect(s.ok).toBe(false)
    expect(s.failures).toEqual([
      { suite: 'email', test: 'check', file: 'e2e/mail/real-mail.spec.ts', message: 'check did not finish' },
    ])
  })

  it('a finished check clears the marker', async () => {
    const f = tmpFile()
    const cs = new CheckSteps(f)
    for (const s of MAIL_STEPS) await cs.run(s.name, () => {})
    const s = read(f)
    expect(s.ok).toBe(true)
    expect(s.failures).toEqual([])
  })

  it('the rewrite leaves no temp file', async () => {
    const f = tmpFile()
    const cs = new CheckSteps(f)
    await cs.run('mail budget', () => {})
    expect(readdirSync(join(f, '..'))).toEqual(['email.json'])
  })

  it('names the steps of the section', () => {
    expect(MAIL_STEPS.map((s) => s.name)).toEqual([
      'mail budget',
      'invite mail arrives',
      'invite mail uses the brand layout',
      'invite text matches',
      'invite page registers the invitee',
      'password mail arrives',
      'password mail uses the brand layout',
      'password mail text matches',
      'password link opens the workspace',
      'used invite link is refused',
      'reset mail arrives',
      'reset mail uses the brand layout',
      'reset text matches',
      'reset link sets the password',
      'sign-in with the new password',
      'signup mail arrives',
      'signup mail uses the brand layout',
      'signup link confirms the address',
      'newsletter mail arrives',
      'newsletter mail uses the brand layout',
      'newsletter link confirms the subscription',
      'subscriber is a Resend contact on the Blog topic',
      'only these mails are real',
    ])
  })

  it('every step needs only earlier steps', () => {
    MAIL_STEPS.forEach((step, i) => {
      for (const n of step.needs) expect(MAIL_STEPS.findIndex((x) => x.name === n)).toBeLessThan(i)
    })
  })

  it('a failed brand step skips nothing', async () => {
    const brand = MAIL_STEPS.filter((s) => s.name.endsWith('uses the brand layout')).map((s) => s.name)
    expect(brand).toHaveLength(5)
    for (const failing of brand) {
      const cs = new CheckSteps(tmpFile())
      for (const s of MAIL_STEPS) {
        if (s.name === failing) await cs.run(s.name, () => { throw new Error('x') })
        else await cs.run(s.name, () => {})
      }
      const others = cs.section().steps.filter((x) => x.name !== failing)
      expect(others.every((x) => x.status === 'passed')).toBe(true)
    }
  })

  it('a failed signup mail skips only the signup steps', async () => {
    const cs = new CheckSteps(tmpFile())
    for (const s of MAIL_STEPS) {
      if (s.name === 'signup mail arrives') await cs.run(s.name, () => { throw new Error('x') })
      else await cs.run(s.name, () => {})
    }
    const status = (n: string) => cs.section().steps.find((x) => x.name === n)?.status
    expect(status('signup mail uses the brand layout')).toBe('skipped')
    expect(status('signup link confirms the address')).toBe('skipped')
    for (const n of ['newsletter link confirms the subscription', 'reset link sets the password', 'only these mails are real']) {
      expect(status(n)).toBe('passed')
    }
  })
})

describe('writeAccounts', () => {
  const accounts = {
    admin: { email: 'a@example.com', password: 'pw-admin-secret', displayName: 'Mail Admin', workspaceName: 'Mail Workspace' },
    reset: { email: 'r@example.com', password: 'pw-reset-secret' },
  }

  it('keeps the file private', () => {
    const file = join(mkdtempSync(join(tmpdir(), 'mail-accounts-')), 'accounts.json')
    writeAccounts(file, accounts)
    expect(statSync(file).mode & 0o777).toBe(0o600)
    expect(readAccounts(file)).toEqual(accounts)
  })

  it('narrows a file that already exists', () => {
    const file = join(mkdtempSync(join(tmpdir(), 'mail-accounts-')), 'accounts.json')
    writeFileSync(file, '{}', { mode: 0o644 })
    chmodSync(file, 0o644)
    writeAccounts(file, accounts)
    expect(statSync(file).mode & 0o777).toBe(0o600)
  })

  it('throws on a missing file with no path or value in the text', () => {
    const file = join(mkdtempSync(join(tmpdir(), 'mail-accounts-')), 'absent-accounts.json')
    expect(() => readAccounts(file)).toThrow(/missing or unreadable/)
    try {
      readAccounts(file)
    } catch (e) {
      expect((e as Error).message).not.toContain('absent-accounts')
    }
  })
})

describe('requireEnv', () => {
  it('names every missing variable and no value', () => {
    vi.stubEnv('MAIL_TEST_SET', 'a-secret-value')
    vi.stubEnv('MAIL_TEST_BLANK', '  ')
    vi.stubEnv('MAIL_TEST_ABSENT', undefined)
    try {
      requireEnv('MAIL_TEST_SET', 'MAIL_TEST_BLANK', 'MAIL_TEST_ABSENT')
      expect.unreachable()
    } catch (e) {
      expect((e as Error).message).toBe('not set: MAIL_TEST_BLANK, MAIL_TEST_ABSENT')
    } finally {
      vi.unstubAllEnvs()
    }
  })

  it('returns the trimmed values', () => {
    vi.stubEnv('MAIL_TEST_SET', ' v ')
    expect(requireEnv('MAIL_TEST_SET')).toEqual({ MAIL_TEST_SET: 'v' })
    vi.unstubAllEnvs()
  })
})
