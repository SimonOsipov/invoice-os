import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { unzipSync, zipSync } from 'fflate'
import { describe, expect, it } from 'vitest'
import { residuals, scrubDir, scrubText } from './reportScrub'

const b64u = (s: string) => Buffer.from(s).toString('base64url')
// Built from parts so no literal token sits in the source.
const jwt = () => [b64u('{"alg":"HS256"}'), b64u('{"sub":"user-1234"}'), b64u('signature-part')].join('.')
const enc = (s: string) => new TextEncoder().encode(s)
const dec = (b: Uint8Array) => new TextDecoder().decode(b)
const tmp = () => mkdtempSync(join(tmpdir(), 'scrub-'))
const put = (dir: string, rel: string, body: string | Uint8Array) => {
  mkdirSync(dirname(join(dir, rel)), { recursive: true })
  writeFileSync(join(dir, rel), body)
}
const CLI = resolve(import.meta.dirname, 'reportScrub.ts')
const run = (...args: string[]) => spawnSync(process.execPath, [CLI, ...args], { encoding: 'utf8' })
const pwTag = () => ['e2e-member', 'pw', '1'].join('-')

describe('scrubText', () => {
  it('redacts a JWT anywhere', () => {
    const t = jwt()
    const out = scrubText(`{"msg":"got ${t} back","h":"Bearer ${t}"}`)
    expect(out).not.toMatch(/eyJ[A-Za-z0-9_-]{10,}/)
    expect(out).not.toContain(t.split('.')[2])
    expect(out).toContain('[redacted]')
  })

  it('keeps a lone eyJ word', () => {
    expect(scrubText('eyJhello')).toBe('eyJhello')
  })

  it('redacts a truncated token', () => {
    const out = scrubText(`error: bad token eyJ${'abcDEF123_-x'}... in header`)
    expect(out).not.toMatch(/eyJ[A-Za-z0-9_-]{10,}/)
    expect(out).toContain('[redacted]')
  })

  it('redacts auth and cookie headers', () => {
    const har = [
      '{"name":"authorization","value":"Bearer abc"}',
      '{"name":"Cookie","value":"a=1; b=2"}',
      '{"name":"set-cookie","value":"__Host-rt-app=rtval; Path=/"}',
      '{"name":"x-gateway-token","value":"gwval"}',
      '{\\"name\\":\\"authorization\\",\\"value\\":\\"escval\\"}',
      '{"name":"content-type","value":"application/json"}',
    ].join('\n')
    const out = scrubText(har)
    for (const v of ['Bearer abc', 'a=1', 'rtval', 'gwval', 'escval']) expect(out).not.toContain(v)
    expect(out).toContain('"value":"application/json"')
    expect(out).toContain('\\"value\\":\\"[redacted]')
  })

  it('redacts password and token fields', () => {
    const out = scrubText(
      ['{"email":"a@b","password":"p1"}', '{\\"refresh_token\\":\\"r\\"}', 'grant_type=password&password=p2&refresh_token=r2'].join('\n'),
    )
    expect(out).toContain('"email":"a@b"')
    expect(out).toContain('grant_type=password&')
    expect(out).not.toMatch(/p1|"r\\"|p2|r2/)
    expect(JSON.parse(out.split('\n')[0]).password).toBe('[redacted]')
  })

  it('redacts fill values', () => {
    const out = scrubText(
      [
        '{"title":"Fill \\"secret-x\\""}',
        '{"type":"before","method":"fill","params":{"selector":"s","value":"secret-x"}}',
        'locator.fill("secret-x")',
        'Type "yval"',
      ].join('\n'),
    )
    expect(out).not.toMatch(/secret-x|yval/)
    expect(out).toContain('"selector":"s"')
    for (const l of out.split('\n').slice(0, 2)) expect(() => JSON.parse(l)).not.toThrow()
  })

  it('redacts raw non-JSON auth and cookie header lines', () => {
    const opaque = ['op', 'aque', Date.now()].join('')
    const out = scrubText(`GET /x\nAuthorization: Bearer ${opaque}\ncookie: sid=${opaque}\nSet-Cookie: rt=${opaque}; Path=/\nAccept: */*`)
    expect(out).not.toContain(opaque)
    expect(out).toContain('Accept: */*')
    const dir = tmp()
    put(dir, 'h.txt', `Authorization: Bearer ${opaque}`)
    expect(residuals(dir)).toEqual([join(dir, 'h.txt')])
  })

  // The raw-line rule skips a name that follows a word char or a quote, so these slip past the scrub and the residual check alike.
  it('redacts a header line inside a JSON string: after an escaped newline, or at the string start', () => {
    const opaque = ['op', 'aque', Date.now()].join('')
    const bodies = [
      `GET /x\\nAuthorization: Bearer ${opaque}\\nAccept: */*`,
      `HTTP/1.1 200\\r\\nSet-Cookie: rt=${opaque}; Path=/\\r\\n`,
      `cookie: sid=${opaque}`,
    ]
    for (const body of bodies) {
      const json = `{"m":"${body}"}`
      const out = scrubText(json)
      expect(out, body).not.toContain(opaque)
      expect(() => JSON.parse(out)).not.toThrow()
      const dir = tmp()
      put(dir, 'h.json', json)
      expect(residuals(dir), body).toEqual([join(dir, 'h.json')])
    }
  })

  it('redacts a quoted header value, Proxy-Authorization and a capitalised token=', () => {
    const v = ['qv', Date.now()].join('')
    const lines = [`Authorization: "Bearer ${v}"`, `Proxy-Authorization: Basic ${v}`, `{"m":"Proxy-Authorization: \\"Bearer ${v}\\""}`, `GET /cb?Token=${v}&x=1`]
    for (const line of lines) {
      const out = scrubText(line)
      expect(out, line).not.toContain(v)
      if (line.includes('Token=')) continue
      const dir = tmp()
      put(dir, 'h.txt', line)
      expect(residuals(dir), line).toEqual([join(dir, 'h.txt')])
    }
  })

  it('redacts a password key in any letter case', () => {
    const pw = ['Pw', 'x', Date.now()].join('-')
    const out = scrubText(`{"Password":"${pw}","PASSWORD":"${pw}","x":"{\\"Password\\":\\"${pw}\\"}"}`)
    expect(out).not.toContain(pw)
    const dir = tmp()
    put(dir, 'p.json', `{"PASSWORD":"${pw}"}`)
    expect(residuals(dir)).toEqual([join(dir, 'p.json')])
  })

  it('redacts a password value that holds an escaped quote', () => {
    const tail = ['tail', Date.now()].join('')
    const plain = scrubText(`{"password":"ab\\"${tail}","email":"a@b"}`)
    expect(plain).not.toContain(tail)
    expect(JSON.parse(plain).email).toBe('a@b')
    const esc = scrubText(`{"b":"{\\"password\\":\\"ab\\\\\\"${tail}\\",\\"email\\":\\"a@b\\"}"}`)
    expect(esc).not.toContain(tail)
    expect(esc).toContain('a@b')
  })
})

describe('scrubDir', () => {
  const report = (title: string) => JSON.stringify({ files: [{ tests: [{ title }] }] })

  it('rewrites the embedded report zip', () => {
    const dir = tmp()
    const zip = zipSync({ 'report.json': enc(report('Fill "pwval"')) })
    put(dir, 'index.html', `<html><script>app()</script><script type="application/zip">data:application/zip;base64,${Buffer.from(zip).toString('base64')}</script></html>`)
    scrubDir(dir)
    const html = readFileSync(join(dir, 'index.html'), 'latin1')
    const payload = /base64,([^<]*)</.exec(html)![1]
    const json = dec(unzipSync(new Uint8Array(Buffer.from(payload, 'base64')))['report.json'])
    expect(() => JSON.parse(json)).not.toThrow()
    expect(json).not.toContain('pwval')
    expect(html).toContain('<script>app()</script>')
  })

  it('rewrites a nested trace zip and keeps binaries', () => {
    const dir = tmp()
    // A binary entry that reads like a typed value: the scrubber must not touch it.
    const png = new Uint8Array([137, 80, 78, 71, 0, 255, ...enc('Fill "keep"'), 200])
    const net = `{"headers":[{"name":"x","value":"Bearer ${jwt()}"}]}`
    put(dir, 'data/t.zip', zipSync({ '0-trace.network': enc(net), 'p.png': png }))
    scrubDir(dir)
    const e = unzipSync(new Uint8Array(readFileSync(join(dir, 'data/t.zip'))))
    expect(dec(e['0-trace.network'])).not.toMatch(/eyJ/)
    expect(dec(e['0-trace.network'])).toContain('[redacted]')
    expect(Array.from(e['p.png'])).toEqual(Array.from(png))
  })

  it('keeps non-ASCII text intact while redacting', () => {
    const dir = tmp()
    put(dir, 'r.json', '{"name":"Zoë — ok","title":"Fill \\"pw\\""}')
    scrubDir(dir)
    expect(readFileSync(join(dir, 'r.json'), 'utf8')).toBe('{"name":"Zoë — ok","title":"Fill \\"[redacted]\\""}')
  })

  it('scrubs an extensionless resource and a zip nested in a zip', () => {
    const dir = tmp()
    const inner = zipSync({ 'resources/abc123': enc(`{"access_token":"${jwt()}","email":"a@b"}`), 'notes.md': enc('"password":"Ab12cd34Ef56gh78"') })
    put(dir, 'data/t.zip', zipSync({ 'outer.trace': enc('{"method":"click"}'), 'inner.zip': inner }))
    scrubDir(dir)
    const outer = unzipSync(new Uint8Array(readFileSync(join(dir, 'data/t.zip'))))
    expect(Object.keys(outer).sort()).toEqual(['inner.zip', 'outer.trace'])
    const e = unzipSync(outer['inner.zip'])
    expect(dec(e['resources/abc123'])).toBe('{"access_token":"[redacted]","email":"a@b"}')
    expect(dec(e['notes.md'])).toBe('"password":"[redacted]"')
    expect(residuals(dir)).toEqual([])
  })

  it('leaves a clean tree unchanged', () => {
    const dir = tmp()
    const files: Record<string, Uint8Array> = {
      'a.json': enc('{"ok":true}'),
      'data/t.zip': zipSync({ 'x.trace': enc('{"method":"click"}') }),
      'index.html': enc('<html>nothing</html>'),
      'p.png': new Uint8Array([0, 1, 2, 250]),
    }
    for (const [n, b] of Object.entries(files)) put(dir, n, b)
    scrubDir(dir)
    for (const [n, b] of Object.entries(files)) expect(Array.from(readFileSync(join(dir, n)))).toEqual(Array.from(b))
  })
})

describe('residuals', () => {
  it('flags an unredacted password field', () => {
    const dir = tmp()
    put(dir, 'bad.png', '{"password":"Ab12cd34Ef56gh78"}')
    put(dir, 'ok.png', '{"password":"[redacted]"}')
    const got = residuals(dir)
    expect(got).toEqual([join(dir, 'bad.png')])
  })
})

describe('reportScrub CLI', () => {
  it('fails closed on a residual', () => {
    const dir = tmp()
    put(dir, 'shot.png', `${pwTag()}`)
    const r = run(dir)
    expect(r.status).toBe(1)
    expect(r.stderr).toContain('shot.png')
    expect(r.stderr).not.toContain(pwTag())
  })

  it('skips a missing directory', () => {
    expect(run(join(tmp(), 'nope')).status).toBe(0)
  })

  it('fails closed on a corrupt trace zip', () => {
    const dir = tmp()
    const junk = 'not-a-zip-' + 'q'.repeat(20)
    put(dir, 'data/t.zip', junk)
    put(dir, 'report.json', `{"a":"${jwt()}"}`)
    const r = run(dir)
    expect(r.status).toBe(1)
    expect(r.stderr).toContain(join('data', 't.zip'))
    expect(r.stderr).not.toContain(junk)
    expect(r.stderr).not.toContain(jwt())
  })

  it('fails closed on a credential outside the embedded payload', () => {
    const dir = tmp()
    put(dir, 'index.html', `<script>var t="${jwt()}"</script><script>data:application/zip;base64,${Buffer.from(zipSync({ 'a.json': enc('{}') })).toString('base64')}</script>`)
    const r = run(dir)
    expect(r.status).toBe(1)
    expect(r.stderr).toContain('index.html')
    expect(r.stderr).not.toContain(jwt())
  })

  it('fails closed on a truncated trace zip', () => {
    const dir = tmp()
    const whole = zipSync({ 'a.network': enc('{"a":1}'), 'b.trace': enc('{"b":2}') })
    put(dir, 'data/t.zip', whole.slice(0, whole.length - 30))
    const r = run(dir)
    expect(r.status).toBe(1)
    expect(r.stderr).toContain(join('data', 't.zip'))
  })

  it('scrubs and exits 0 on a tree whose only credentials are redactable', () => {
    const dir = tmp()
    put(dir, 'r.json', `{"headers":[{"name":"authorization","value":"Bearer ${jwt()}"}],"body":{"password":"${pwTag()}"}}`)
    const r = run(dir)
    expect(r.status).toBe(0)
    const out = readFileSync(join(dir, 'r.json'), 'utf8')
    expect(out).not.toContain(pwTag())
    expect(out).not.toMatch(/eyJ/)
    expect(JSON.parse(out).body.password).toBe('[redacted]')
  })

  it('fails closed on a bad embedded payload', () => {
    const good = Buffer.from(zipSync({ 'a.json': enc('{}') })).toString('base64')
    // The second payload decodes under a lenient reader: only the strict check refuses it.
    for (const payload of ['AAAA!!notzip', `${good}!!`]) {
      const dir = tmp()
      put(dir, 'index.html', `<script>data:application/zip;base64,${payload}</script>`)
      const r = run(dir)
      expect(r.status).toBe(1)
      expect(r.stderr).toContain('index.html')
    }
  })
})
