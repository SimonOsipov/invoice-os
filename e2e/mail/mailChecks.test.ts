import { mkdtempSync, readdirSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { describe, expect, it, vi } from 'vitest'

import { CheckSteps, isFakeAddress, judgeSends, MAIL_STEPS, mailTextDiff, overBudget, scrub, type Section } from './mailChecks'

const NOW = new Date('2026-10-30T13:00:00Z')
const DOMAIN = 'inbox.mailbox.app'
const TODAY = '2026-10-30 00:00:00.000000+00'
const YESTERDAY = '2026-10-29 23:59:59.999999+00'

const send = (to: string, created_at = TODAY) => ({ to: [to], created_at })
const nightly = (n: number, created_at = TODAY) =>
  Array.from({ length: n }, (_, i) => send(`nightly-${i}-invite@${DOMAIN}`, created_at))
const prod = Array.from({ length: 5 }, (_, i) => send(`customer${i}@ascomply.com`))

describe('overBudget', () => {
  it("leaves room for the run's 3 mails", () => {
    expect([22, 23, 25, 26].map((n) => overBudget([...nightly(n), ...prod], DOMAIN, NOW))).toEqual([false, true, true, true])
  })

  it("ignores another domain's nightly- name", () => {
    const other = Array.from({ length: 30 }, () => send('nightly-1-invite@other.test'))
    expect(overBudget(other, DOMAIN, NOW)).toBe(false)
  })

  it('ignores mail from before 00:00 UTC', () => {
    expect(overBudget(nightly(30, YESTERDAY), DOMAIN, NOW)).toBe(false)
  })
})

describe('judgeSends', () => {
  const inv = `nightly-1-invite@${DOMAIN}`
  const rst = `nightly-1-reset@${DOMAIN}`
  const base = [send(inv), send(inv), send(rst), send('buyer@ascomply.com')]
  const rows: [string, ReturnType<typeof send>[], boolean][] = [
    ['2 + 1 + production', base, true],
    ['1 invite', [send(inv), send(rst)], false],
    ['3 invite', [...base, send(inv)], false],
    ['0 reset', [send(inv), send(inv)], false],
    ['2 reset', [...base, send(rst)], false],
    ['a fake address', [...base, send('someone@example.com')], false],
  ]

  it.each(rows)('%s', (_name, sends, ok) => {
    expect(judgeSends(sends, inv, rst).ok).toBe(ok)
  })

  it('detail holds no address', () => {
    for (const [, sends, ok] of rows) if (!ok) expect(judgeSends(sends, inv, rst).detail).not.toContain('@')
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
  const jwt = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl'
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
    await cs.run('a', () => {})
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
    expect(s.failures[0]).toMatchObject({ suite: 'email', test: 'b' })
    expect(never).not.toHaveBeenCalled()
    expect(read(f)).toEqual(s)
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
    expect(s.steps.filter((x) => x.status === 'skipped')).toHaveLength(12)
    expect(s.ok).toBe(false)
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
    expect(read(f).steps).toHaveLength(13)
    await cs.run('mail budget', () => {})
    await cs.run('invite mail arrives', () => {})
    const s = read(f)
    expect(s.steps.filter((x) => x.status === 'passed')).toHaveLength(2)
    expect(s.steps.filter((x) => x.status === 'skipped')).toHaveLength(11)
    expect(s.ok).toBe(false)
    expect(s.failures.map((x) => x.message)).toEqual(['check did not finish'])
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

  it('names the 13 steps of the section', () => {
    expect(MAIL_STEPS.map((s) => s.name)).toHaveLength(13)
  })
})
