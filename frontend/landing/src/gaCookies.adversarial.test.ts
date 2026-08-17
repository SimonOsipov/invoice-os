// Adversarial coverage for gaCookies.ts, added at QA. Two holes it closes:
//   1. Deleting the host-only write (gaCookies.ts:49) left all 16 shipped cookie
//      specs green. jsdom does not distinguish a host-only cookie from one scoped by
//      `domain=`, and every domain variant of `localhost` / `www.ascomply.com` is
//      accepted there, so the jar can never observe that write. A real browser CAN:
//      a cookie set with no `domain=` is a different cookie from `.host` and only a
//      delete with no `domain=` removes it. The oracle here is the write log.
//   2. `Privacy.tsx` publishes "the only thing our code ever writes to that list is
//      the instruction that deletes these two", and docs/privacy-policy-claims.md E3
//      says every write is an expiry. Nothing asserted it. Now the log does.
import { describe, expect, it } from 'vitest'
import { clearGaCookies, cookieDomainVariants, gaCookieNames, isGaCookieName } from './gaCookies'

/** Records every assignment instead of keeping a jar, so writes are observable. */
function recordingDoc(initial: string) {
  const writes: string[] = []
  return {
    writes,
    doc: {
      get cookie() {
        return initial
      },
      set cookie(v: string) {
        writes.push(v)
      },
    } as Pick<Document, 'cookie'>,
  }
}

describe('every write is an expiry, never a store', () => {
  it('the published claim holds literally: Max-Age=0 on every assignment', () => {
    const { writes, doc } = recordingDoc('_ga=GA1.1.1.1; _ga_X=GS1.1; hs=keep')
    clearGaCookies('www.ascomply.com', doc)

    expect(writes.length, 'nothing was written').toBeGreaterThan(0)
    for (const w of writes) {
      expect(w, `not an expiry: ${w}`).toContain('Max-Age=0')
      expect(w, `not an expiry: ${w}`).toContain('expires=Thu, 01 Jan 1970')
      // The value is empty: `<name>=;`. A write carrying a value would be a store.
      expect(w, `write carries a value: ${w}`).toMatch(/^[^=]+=;/)
    }
  })

  it('control: the recorder would catch a store', () => {
    const { writes, doc } = recordingDoc('')
    doc.cookie = '_ga=stored; path=/'
    expect(writes).toEqual(['_ga=stored; path=/'])
    expect(writes[0]).not.toContain('Max-Age=0')
  })

  it('a bare expiry with no domain= is emitted for every name — the host-only cookie', () => {
    // Load-bearing and unobservable in jsdom: deleting this write leaves every
    // jar-based spec green while a real browser keeps its host-only _ga forever.
    const { writes, doc } = recordingDoc('_ga=1; _ga_X=2')
    clearGaCookies('www.ascomply.com', doc)

    for (const name of ['_ga', '_ga_X']) {
      const bare = writes.filter((w) => w.startsWith(`${name}=`) && !w.includes('domain='))
      expect(bare.length, `no host-only expiry for ${name}`).toBe(1)
    }
  })

  it('one expiry per name per domain variant, plus the bare one', () => {
    const { writes, doc } = recordingDoc('_ga=1')
    clearGaCookies('www.ascomply.com', doc)
    expect(writes.length).toBe(cookieDomainVariants('www.ascomply.com').length + 1)
    for (const domain of cookieDomainVariants('www.ascomply.com')) {
      expect(writes.filter((w) => w.endsWith(`domain=${domain}`)).length, domain).toBe(1)
    }
  })

  it('only GA names are ever written', () => {
    const { writes, doc } = recordingDoc('hs_keep=stay; _gid=1; _gat=1; sessionid=abc; _ga=1')
    const targeted = clearGaCookies('ascomply.com', doc)
    expect(targeted).toEqual(['_ga'])
    for (const w of writes) {
      expect(w.split('=')[0], `wrote a non-GA name: ${w}`).toBe('_ga')
    }
  })

  it('a name present twice in the jar is targeted once', () => {
    // A real jar holds `_ga` twice when one copy is host-only and one is on the
    // registrable domain; document.cookie reports both.
    const { writes, doc } = recordingDoc('_ga=host; _ga=domain')
    expect(clearGaCookies('ascomply.com', doc)).toEqual(['_ga'])
    expect(writes.length).toBe(cookieDomainVariants('ascomply.com').length + 1)
  })
})

describe('adversarial cookie strings', () => {
  it('a value containing = does not corrupt the name', () => {
    // GA4 values are base64-ish and really do carry `=` padding.
    expect(gaCookieNames('_ga=GA1.1.a=b=c; _ga_X=GS1.1.pad==')).toEqual(['_ga', '_ga_X'])
    const { writes, doc } = recordingDoc('_ga=GA1.1.a=b=c')
    clearGaCookies('ascomply.com', doc)
    for (const w of writes) expect(w.startsWith('_ga=;')).toBe(true)
  })

  it('_ga as a substring of another name is left alone', () => {
    for (const name of ['my_ga', '_gaz', '_ga_', '__ga', '_ga-1', '_GA', '_ga_X.y']) {
      expect(isGaCookieName(name), `"${name}" must not be treated as a GA cookie`).toBe(false)
    }
    const { writes, doc } = recordingDoc('my_ga=1; _gaz=2; _ga_=3; __ga=4')
    expect(clearGaCookies('ascomply.com', doc)).toEqual([])
    expect(writes, 'a lookalike name was expired').toEqual([])
  })

  it('an empty jar writes nothing at all', () => {
    for (const raw of ['', '   ', ';', ';;;', '=']) {
      const { writes, doc } = recordingDoc(raw)
      expect(clearGaCookies('ascomply.com', doc), `raw: "${raw}"`).toEqual([])
      expect(writes, `raw: "${raw}"`).toEqual([])
    }
  })

  it('whitespace and quoting around a name are tolerated', () => {
    expect(gaCookieNames('  _ga =1;   _ga_X   =2')).toEqual(['_ga', '_ga_X'])
  })
})

describe('adversarial hostnames — total, never throwing', () => {
  it.each([
    ['192.168.1.10', 3],
    ['127.0.0.1', 3],
    ['localhost', 1],
    ['ascomply.com.', 2],
    ['www.ascomply.com.', 3],
    ['', 1],
    ['.', 1],
    ['a.b.c.d.e.f', 5],
  ])('%s yields %i variant pairs and throws nothing', (host, pairs) => {
    const out = cookieDomainVariants(host)
    expect(Array.isArray(out)).toBe(true)
    // A single-label host is its own only variant, undotted; everything else is
    // emitted twice, dotted and undotted.
    expect(out.length).toBe(pairs === 1 && !host.includes('.') ? 1 : pairs * 2)
    for (const v of out) expect(typeof v).toBe('string')
  })

  it('a hostname argument is never required to be a real host to be safe', () => {
    for (const host of ['192.168.1.10', 'localhost', 'ascomply.com.', '', 'a']) {
      const { doc } = recordingDoc('_ga=1')
      expect(() => clearGaCookies(host, doc), host).not.toThrow()
    }
  })

  it('a single-label host still gets the bare expiry, so it is not a dead call', () => {
    const { writes, doc } = recordingDoc('_ga=1')
    clearGaCookies('localhost', doc)
    expect(writes.filter((w) => !w.includes('domain=')).length).toBe(1)
    expect(writes.filter((w) => w.endsWith('domain=localhost')).length).toBe(1)
  })
})
