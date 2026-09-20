// LOCAL guard on the deploy-only specs' steering marker, read from the fake client's
// source regex (not transcribed) so a mis-encoded marker can't burn the one deploy run.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { steerMarker } from './importFixtures'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const FAKE_GO = join(REPO, 'internal/platform/ai/fake.go')

describe('AIRM-01', () => {
  it('the steering marker round-trips through base64url with no padding', () => {
    // Engineered, not incidental: this payload's standard-base64 encoding is KNOWN to
    // contain '/', so a base64url<->base64 mix-up is caught here even though most
    // payloads' encodings happen to avoid every special character either way.
    const answer = { invoice_number: 'AIRM-01 test marker ~~ fixture ??', header_row: 1 }
    const marker = steerMarker(answer)

    expect(marker.startsWith('AIFAKE-ANSWER-')).toBe(true)
    const encoded = marker.slice('AIFAKE-ANSWER-'.length)

    // Control: this fixture must actually exercise a base64url-only character, or the
    // assertions below would pass on a mis-encoded marker too.
    expect(encoded, 'control: this fixture must produce a base64url-only character').toMatch(/[_-]/)

    // Standard (padded) base64 emits '+', '/' and trailing '=' -- none of those are legal
    // in Go's markerRe character class, so a padded encoding would be truncated by the
    // regex, fail to decode, and the fake client would answer blank.
    expect(encoded, 'no padding character').not.toContain('=')
    expect(encoded, 'no standard-base64 characters either').not.toMatch(/[+/]/)

    const decoded = JSON.parse(Buffer.from(encoded, 'base64url').toString('utf8'))
    expect(decoded).toEqual(answer)
  })
})

describe('AIRM-02', () => {
  it("the steering marker matches the fake client's own marker regex", () => {
    const src = readFileSync(FAKE_GO, 'utf8')
    // Anchored on the NAME, not "first MustCompile in the file" -- a second MustCompile
    // added above markerRe must not silently re-point this mirror at the wrong literal.
    const match = src.match(/\bmarkerRe\s*=\s*regexp\.MustCompile\(`([^`]+)`\)/)
    expect(match, "control: fake.go's named `markerRe = regexp.MustCompile(...)` declaration must be found, or this test reads nothing").not.toBeNull()

    const pattern = match![1]
    // Control: a mis-pathed read (e.g. an empty file, or a rewritten literal with no
    // AIFAKE- prefix) cannot pass the assertion below vacuously.
    expect(pattern.length).toBeGreaterThan(0)
    expect(pattern).toContain('AIFAKE-')

    // A value engineered to contain a base64url-only character ('_' or '-'), so a
    // narrowed character class in fake.go is caught, not just a wrong prefix.
    const answer = { invoice_number: 'AIRM-02 test marker ~~ fixture ??' }
    const marker = steerMarker(answer)
    // The ENCODED half only: the AIFAKE-ANSWER- prefix carries hyphens of its own, so the
    // same assertion over the whole marker holds for every payload and controls nothing.
    expect(
      marker.slice('AIFAKE-ANSWER-'.length),
      'control: this fixture must actually exercise a base64url-only character',
    ).toMatch(/[_-]/)

    // Go's regexp accepts constructs (e.g. named groups) that throw as a JS RegExp --
    // report that as a named assertion failure, not an uncaught error.
    let re: RegExp
    try {
      re = new RegExp(pattern)
    } catch (err) {
      throw new Error(`markerRe pattern ${JSON.stringify(pattern)} is not a valid JS RegExp: ${(err as Error).message}`)
    }

    const found = marker.match(re)
    expect(found, `marker ${marker} must match the fake client's own regex`).not.toBeNull()
    expect(found![0], 'the match must be the WHOLE marker, not a prefix truncated by a narrower class').toBe(marker)
  })
})
