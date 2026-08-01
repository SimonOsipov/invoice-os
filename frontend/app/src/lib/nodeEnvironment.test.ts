// Guards AC-2: no `@vitest-environment` docblock here, so this runs under
// vitest.config.ts's default. If that default is ever flipped to jsdom (or a
// global environmentMatchGlobs equivalent is reintroduced), every pure-function
// suite under src/lib/ would keep passing silently — none of them assert on the
// environment. This is the only thing that would go red.
import { describe, expect, it } from 'vitest'

describe('vitest default environment', () => {
  it('runs src/lib tests without a DOM', () => {
    expect(typeof window).toBe('undefined')
    expect(typeof document).toBe('undefined')
  })
})
