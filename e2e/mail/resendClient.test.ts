import { afterEach, describe, expect, it, vi } from 'vitest'

import { listSent, parseCreatedAt, waitForMail, type Fetch } from './resendClient'

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

  it('ignores a mail from before the lookback', async () => {
    vi.useFakeTimers()
    const stale = new Date(Date.now() - 120_000).toISOString().replace('T', ' ').replace('Z', '000+00')
    const p = waitForMail('k', 'me@x.test', 'S', 180_000, polls([item('old', stale, 'me@x.test', 'S')]))
    const assertion = expect(p).rejects.toThrow('no mail in 3 minutes')
    await vi.advanceTimersByTimeAsync(200_000)
    await assertion
  })

  it('throws at the deadline', async () => {
    vi.useFakeTimers()
    const p = waitForMail('k', 'me@x.test', 'S', 180_000, polls([]))
    const assertion = expect(p).rejects.toThrow('no mail in 3 minutes')
    await vi.advanceTimersByTimeAsync(200_000)
    await assertion
  })

  it('a 404 is final and names the status only', async () => {
    const stub = vi.fn(async () => json({ message: 'no such thing someone@secret.test' }, 404))
    const err = await waitForMail('k', 'me@x.test', 'S', 180_000, stub as unknown as Fetch).catch((e: Error) => e)
    expect((err as Error).message).toContain('404')
    expect((err as Error).message).not.toContain('@')
    expect(stub).toHaveBeenCalledTimes(1)
  })

  it('keeps polling through a 429, a 503 and a dropped connection', async () => {
    vi.useFakeTimers()
    const answers = [
      () => json({ message: 'slow down' }, 429),
      () => json({}, 503),
      () => {
        throw new TypeError('fetch failed')
      },
      () => json({ data: [item('mine', now(), 'me@x.test', 'S')], has_more: false }),
    ]
    let n = 0
    const stub = vi.fn(async () => answers[n++]()) as unknown as Fetch
    const p = waitForMail('k', 'me@x.test', 'S', 180_000, stub)
    await vi.advanceTimersByTimeAsync(15_000)
    expect((await p).id).toBe('mine')
    expect(n).toBe(4)
  })

  it('at the deadline reports the last status only', async () => {
    vi.useFakeTimers()
    const stub = vi.fn(async () => json({ message: 'slow down someone@secret.test' }, 429)) as unknown as Fetch
    const p = waitForMail('k', 'me@x.test', 'S', 180_000, stub)
    const assertion = expect(p).rejects.toThrow(/no mail in 3 minutes \(last: Resend answered 429\)$/)
    await vi.advanceTimersByTimeAsync(200_000)
    await assertion
  })
})

describe('listSent retries', () => {
  afterEach(() => vi.useRealTimers())

  it('retries a 429 and a dropped connection, then returns the page', async () => {
    vi.useFakeTimers()
    const answers = [
      () => json({}, 429),
      () => {
        throw new TypeError('fetch failed')
      },
      () => json({ data: [item('a', NEW)], has_more: false }),
    ]
    let n = 0
    const stub = vi.fn(async () => answers[n++]()) as unknown as Fetch
    const p = listSent('k', SINCE, stub)
    await vi.advanceTimersByTimeAsync(10_000)
    expect((await p).map((m) => m.id)).toEqual(['a'])
  })

  it('gives up after a bounded number of tries', async () => {
    vi.useFakeTimers()
    const stub = vi.fn(async () => json({}, 500))
    const p = listSent('k', SINCE, stub as unknown as Fetch)
    const assertion = expect(p).rejects.toThrow('500')
    await vi.advanceTimersByTimeAsync(60_000)
    await assertion
    expect(stub).toHaveBeenCalledTimes(4)
  })

  it('does not retry a 401', async () => {
    const stub = vi.fn(async () => json({}, 401))
    await expect(listSent('k', SINCE, stub as unknown as Fetch)).rejects.toThrow('401')
    expect(stub).toHaveBeenCalledTimes(1)
  })
})

describe('parseCreatedAt', () => {
  it('throws on an unparsable date', () => {
    expect(() => parseCreatedAt('not a date')).toThrow('not a date')
  })

  it('listSent rejects an item with an unparsable date', async () => {
    const stub = vi.fn(async () => json({ data: [item('bad', 'garbage')], has_more: true }))
    await expect(listSent('k', SINCE, stub as unknown as Fetch)).rejects.toThrow('created_at')
    expect(stub).toHaveBeenCalledTimes(1)
  })
})
