import { afterEach, describe, expect, it, vi } from 'vitest'

import { listSent, waitForMail, type Fetch } from './resendClient'

const SINCE = new Date('2026-10-30T00:00:00Z')
const NEW = '2026-10-30 12:00:00.000000+00'
const OLD = '2026-10-29 23:59:59.000000+00'

const item = (id: string, created_at: string, to = 'a@x.test', subject = 's') => ({ id, to: [to], subject, created_at })
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

describe('listSent', () => {
  it('reads three pages down to since', async () => {
    const urls: string[] = []
    const pages = [
      { data: [item('p1a', NEW), item('p1b', NEW)], has_more: true },
      { data: [item('p2a', NEW), item('p2b', NEW)], has_more: true },
      { data: [item('p3a', NEW), item('p3old', OLD)], has_more: true },
      { data: [item('p4a', NEW)], has_more: false },
    ]
    const stub = vi.fn(async (url: string) => {
      urls.push(url)
      return json(pages[urls.length - 1])
    })
    const got = await listSent('k', SINCE, stub as unknown as Fetch)
    expect(got.map((m) => m.id)).toEqual(['p1a', 'p1b', 'p2a', 'p2b', 'p3a'])
    expect(urls).toHaveLength(3)
    expect(urls[0]).not.toContain('after=')
    expect(urls[1]).toContain('after=p1b')
    expect(urls[2]).toContain('after=p2b')
  })

  it('parses created_at with a space and +00', async () => {
    const stub = vi.fn(async () =>
      json({
        data: [item('in', '2026-10-30 00:00:00.000001+00'), item('out', '2026-10-29 23:59:59.999999+00')],
        has_more: false,
      }),
    )
    const got = await listSent('k', SINCE, stub as unknown as Fetch)
    expect(got.map((m) => m.id)).toEqual(['in'])
  })

  it('sends the bearer key', async () => {
    const stub = vi.fn(async (_url: string, _init?: RequestInit) => json({ data: [], has_more: false }))
    await listSent('the-key', SINCE, stub as unknown as Fetch)
    expect((stub.mock.calls[0][1]?.headers as Record<string, string>).Authorization).toBe('Bearer the-key')
  })
})

describe('waitForMail', () => {
  afterEach(() => vi.useRealTimers())

  // The mails at each poll, newest first; the last answer repeats.
  const polls = (...answers: ReturnType<typeof item>[][]) => {
    let n = 0
    return vi.fn(async () => json({ data: answers[Math.min(n++, answers.length - 1)], has_more: false })) as unknown as Fetch
  }
  const now = () => new Date().toISOString().replace('T', ' ').replace('Z', '000+00')

  it('finds the exact recipient on a later poll', async () => {
    vi.useFakeTimers()
    const fetchFn = polls([item('other', now(), 'other@x.test', 'S')], [item('mine', now(), 'Me@x.test', 'S')])
    const p = waitForMail('k', 'me@x.test', 'S', 180_000, fetchFn)
    await vi.advanceTimersByTimeAsync(5_000)
    expect((await p).id).toBe('mine')
  })

  it('tells two mails at one address apart by subject', async () => {
    vi.useFakeTimers()
    const invite = item('invite', now(), 'me@x.test', 'invited')
    const fetchFn = polls([invite], [item('confirm', now(), 'me@x.test', 'confirm'), invite])
    const p = waitForMail('k', 'me@x.test', 'confirm', 180_000, fetchFn)
    await vi.advanceTimersByTimeAsync(5_000)
    expect((await p).id).toBe('confirm')
  })

  it('throws at the deadline', async () => {
    vi.useFakeTimers()
    const p = waitForMail('k', 'me@x.test', 'S', 180_000, polls([]))
    const assertion = expect(p).rejects.toThrow('no mail in 3 minutes')
    await vi.advanceTimersByTimeAsync(200_000)
    await assertion
  })

  it('a 429 names the status only', async () => {
    const stub = vi.fn(async () => json({ message: 'slow down someone@secret.test' }, 429)) as unknown as Fetch
    const err = await waitForMail('k', 'me@x.test', 'S', 180_000, stub).catch((e: Error) => e)
    expect((err as Error).message).toContain('429')
    expect((err as Error).message).not.toContain('@')
  })
})
