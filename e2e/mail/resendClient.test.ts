import { afterEach, describe, expect, it, vi } from 'vitest'

import { contactTopics, getContact, getReceived, listSent, parseCreatedAt, settledMailCount, waitForMail, waitForMailCount, type Fetch } from './resendClient'

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

describe('getReceived retries', () => {
  afterEach(() => vi.useRealTimers())

  it('retries a 503 and a dropped connection, then returns the mail', async () => {
    vi.useFakeTimers()
    const answers = [
      () => json({}, 503),
      () => {
        throw new TypeError('fetch failed')
      },
      () => json({ ...item('a', NEW), html: '<p>x</p>', text: null }),
    ]
    let n = 0
    const stub = vi.fn(async () => answers[n++]()) as unknown as Fetch
    const p = getReceived('k', 'a', stub)
    await vi.advanceTimersByTimeAsync(10_000)
    expect((await p).html).toBe('<p>x</p>')
  })

  it('a 404 is final', async () => {
    const stub = vi.fn(async () => json({}, 404))
    await expect(getReceived('k', 'a', stub as unknown as Fetch)).rejects.toThrow('404')
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

describe('getContact', () => {
  it.each([[404], [200]])('returns %i', async (status) => {
    const stub = vi.fn(async (_url: string, _init?: RequestInit) => json({}, status))
    expect(await getContact('k', 'a@x.test', stub as unknown as Fetch)).toBe(status)
  })

  it('throws the status only on 500', async () => {
    const stub = vi.fn(async () => json({ email: 'a@x.test' }, 500))
    await expect(getContact('k', 'a@x.test', stub as unknown as Fetch)).rejects.toThrow(/^Resend answered 500$/)
  })

  it('path-escapes the address', async () => {
    const stub = vi.fn(async (_url: string, _init?: RequestInit) => json({}))
    await getContact('k', 'a+b@x.test', stub as unknown as Fetch)
    expect(stub.mock.calls[0][0]).toBe('https://api.resend.com/contacts/a%2Bb%40x.test')
  })
})

describe('contactTopics', () => {
  const topic = (id: string, subscription = 'opt_in') => ({ id, name: 'Blog', subscription, created_at: 'c', description: 'd' })

  it('lists id, name and subscription', async () => {
    const stub = vi.fn(async (_url: string) => json({ data: [topic('t1')], has_more: false }))
    expect(await contactTopics('k', 'a@x.test', stub as unknown as Fetch)).toEqual([
      { id: 't1', name: 'Blog', subscription: 'opt_in' },
    ])
    expect(stub.mock.calls[0][0]).toContain('/contacts/a%40x.test/topics')
  })

  it('follows has_more', async () => {
    const urls: string[] = []
    const pages = [
      { data: [topic('t1'), topic('t2', 'opt_out')], has_more: true },
      { data: [topic('t3')], has_more: false },
    ]
    const stub = vi.fn(async (url: string) => {
      urls.push(url)
      return json(pages[urls.length - 1])
    })
    const got = await contactTopics('k', 'a@x.test', stub as unknown as Fetch)
    expect(got.map((t) => t.id)).toEqual(['t1', 't2', 't3'])
    expect(got.map((t) => t.subscription)).toEqual(['opt_in', 'opt_out', 'opt_in'])
    expect(urls[0]).not.toContain('after=')
    expect(urls[1]).toContain('after=t2')
  })
})

describe('waitForMailCount', () => {
  afterEach(() => vi.useRealTimers())

  const at = (ms = 0) => new Date(Date.now() + ms).toISOString().replace('T', ' ').replace('Z', '000+00')
  const mails = (n: number, to = 'me@x.test', subject = 'S') => Array.from({ length: n }, (_, i) => item(`m${i}`, at(), to, subject))
  const polls = (...answers: ReturnType<typeof item>[][]) => {
    let n = 0
    return vi.fn(async () => json({ data: answers[Math.min(n++, answers.length - 1)], has_more: false })) as unknown as Fetch
  }
  const since = () => new Date(Date.now() - 60_000)

  it('returns once the count reaches want', async () => {
    vi.useFakeTimers()
    const fetchFn = polls(mails(1), mails(3))
    const p = waitForMailCount('k', 'me@x.test', 'S', 3, since(), 120_000, fetchFn)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(await p).toHaveLength(3)
    expect(fetchFn).toHaveBeenCalledTimes(2)
  })

  it('counts only the exact recipient and subject', async () => {
    vi.useFakeTimers()
    const noise = [...mails(1, 'me+1@x.test'), ...mails(1, 'me@x.test', 'other'), ...mails(1, 'ME@x.test')]
    const p = waitForMailCount('k', 'me@x.test', 'S', 2, since(), 120_000, polls([...mails(1), ...noise]))
    await vi.advanceTimersByTimeAsync(0)
    expect(await p).toHaveLength(2)
  })

  it('returns more than want when more arrived', async () => {
    const got = await waitForMailCount('k', 'me@x.test', 'S', 3, since(), 120_000, polls(mails(4)))
    expect(got).toHaveLength(4)
  })

  it('ignores a mail from before since', async () => {
    vi.useFakeTimers()
    const p = waitForMailCount('k', 'me@x.test', 'S', 1, since(), 60_000, polls([item('old', at(-120_000), 'me@x.test', 'S')]))
    const assertion = expect(p).rejects.toThrow('found 0 of 1 mails in 1 minutes')
    await vi.advanceTimersByTimeAsync(70_000)
    await assertion
  })

  it('throws at the deadline with the counts and no address', async () => {
    vi.useFakeTimers()
    const p = waitForMailCount('k', 'me@x.test', 'S', 3, since(), 120_000, polls(mails(2)))
    const err = p.catch((e: Error) => e)
    await vi.advanceTimersByTimeAsync(130_000)
    const e = (await err) as Error
    expect(e.message).toMatch(/^found 2 of 3 mails in 2 minutes$/)
    expect(e.message).not.toContain('@')
  })

  it('keeps polling through a 429, a 503 and a dropped connection', async () => {
    vi.useFakeTimers()
    const answers = [
      () => json({}, 429),
      () => json({}, 503),
      () => {
        throw new TypeError('fetch failed')
      },
      () => json({ data: mails(1), has_more: false }),
    ]
    let n = 0
    const stub = vi.fn(async () => answers[n++]()) as unknown as Fetch
    const p = waitForMailCount('k', 'me@x.test', 'S', 1, since(), 120_000, stub)
    await vi.advanceTimersByTimeAsync(15_000)
    expect(await p).toHaveLength(1)
  })

  it('a 404 is final', async () => {
    const stub = vi.fn(async () => json({ message: 'x@secret.test' }, 404))
    await expect(waitForMailCount('k', 'me@x.test', 'S', 1, since(), 120_000, stub as unknown as Fetch)).rejects.toThrow(/^Resend answered 404$/)
    expect(stub).toHaveBeenCalledTimes(1)
  })
})

describe('settledMailCount', () => {
  afterEach(() => vi.useRealTimers())

  const at = () => new Date().toISOString().replace('T', ' ').replace('Z', '000+00')
  const mails = (n: number) => Array.from({ length: n }, (_, i) => item(`m${i}`, at(), 'me@x.test', 'S'))
  const since = () => new Date(Date.now() - 60_000)
  // Answers by elapsed time; the last rule that has started wins.
  const timeline = (...rules: [number, () => Response][]) => {
    const t0 = Date.now()
    return vi.fn(async () => {
      const el = Date.now() - t0
      return [...rules].reverse().find(([from]) => el >= from)![1]()
    }) as unknown as Fetch
  }

  it('waits the whole hold before it returns', async () => {
    vi.useFakeTimers()
    let done = false
    const p = settledMailCount('k', 'me@x.test', 'S', since(), 120_000, timeline([0, () => json({ data: mails(3), has_more: false })])).then((r) => {
      done = true
      return r
    })
    await vi.advanceTimersByTimeAsync(119_000)
    expect(done).toBe(false)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(await p).toHaveLength(3)
  })

  it('counts a mail that lands during the hold', async () => {
    vi.useFakeTimers()
    const fetchFn = timeline([0, () => json({ data: mails(3), has_more: false })], [60_000, () => json({ data: mails(4), has_more: false })])
    const p = settledMailCount('k', 'me@x.test', 'S', since(), 120_000, fetchFn)
    await vi.advanceTimersByTimeAsync(120_000)
    expect(await p).toHaveLength(4)
  })

  it('retries a transient error at the end of the hold', async () => {
    vi.useFakeTimers()
    const fetchFn = timeline(
      [0, () => json({ data: mails(3), has_more: false })],
      [120_000, () => json({}, 503)],
      [125_000, () => json({ data: mails(3), has_more: false })],
    )
    const p = settledMailCount('k', 'me@x.test', 'S', since(), 120_000, fetchFn)
    await vi.advanceTimersByTimeAsync(125_000)
    expect(await p).toHaveLength(3)
  })

  it('throws when no read succeeds after the hold', async () => {
    vi.useFakeTimers()
    const stub = vi.fn(async () => json({}, 503)) as unknown as Fetch
    const p = settledMailCount('k', 'me@x.test', 'S', since(), 120_000, stub)
    const err = p.catch((e: Error) => e)
    await vi.advanceTimersByTimeAsync(149_000)
    let settled = false
    void err.then(() => (settled = true))
    await vi.advanceTimersByTimeAsync(0)
    expect(settled).toBe(false)
    await vi.advanceTimersByTimeAsync(1_000)
    const e = (await err) as Error
    expect(e.message).toMatch(/^no inbox read after 2\.5 minutes \(last: Resend answered 503\)$/)
    expect(e.message).not.toContain('@')
  })

  it('a 404 is final', async () => {
    const stub = vi.fn(async () => json({}, 404))
    await expect(settledMailCount('k', 'me@x.test', 'S', since(), 120_000, stub as unknown as Fetch)).rejects.toThrow(/^Resend answered 404$/)
    expect(stub).toHaveBeenCalledTimes(1)
  })
})
