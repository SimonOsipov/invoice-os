// The two `useAsync` shape facts a consuming surface now depends on, pinned in the package that
// owns the hook so a regression localizes here rather than in a frontend app's suite.
//
// This is a DECLARATION PIN, stated as one. `async-state.ts`'s Decision (i) keeps the hook's
// runtime path out of this package -- rendering it would mean adding jsdom, react-dom and
// @testing-library/react to a package four frontends import. The behavioural oracles live in
// `frontend/app/src/components/InvoiceDetail.extraction.test.tsx` (`QA-2`) and
// `InvoiceDetail.extractionGate.test.tsx` (`QA-3`).
//
// Read off `Function.prototype.toString()` rather than the file: this package's tsconfig
// carries no node types on purpose (`lib: ES2022 + DOM`, and `src/env.d.ts` keeps it free of
// vite's ambients too), so `node:fs` would cost it a `@types/node` dependency and make node
// globals visible to every browser-targeted file here.
//
// What depends on these two facts: `InvoiceDetail` runs `GET /v1/extractions` lazily, with
// `immediate: shouldFetchInvoices(base) && documentId != null` and `deps: [documentId]`. The id
// is null at mount and non-null one render later, so the request exists only because the effect
// re-runs on the caller's deps AND re-reads `immediate` when it does.
import { describe, expect, it } from 'vitest'

import { useAsync } from './async-state'

const SRC = useAsync.toString()

describe('useAsync: the deps effect', () => {
  it('useAsync_theSourceIsReadableAtAll', () => {
    // The floor for both rows below. If the transform ever minifies this package's test build,
    // every identifier assertion becomes meaningless and this row is what says so.
    expect(SRC, 'useAsync.toString() is not the hook').toContain('function useAsync')
    for (const name of ['producer', 'dispatch', 'immediate']) {
      expect(SRC, `identifiers are mangled -- the scans below cannot see \`${name}\``).toContain(name)
    }
  })

  it('useAsync_theDepsEffectReadsImmediateWhenItRuns', () => {
    const start = SRC.indexOf('useEffect')
    expect(start, 'useAsync no longer runs its producer from a useEffect').toBeGreaterThan(-1)
    const effect = SRC.slice(start)
    // Control needle: the slice is the effect, not a truncated span.
    expect(effect, 'the slice missed the effect body').toContain('run()')

    // THE fact. `immediate` is read inside the effect, so a deps change re-reads the CURRENT
    // value. Capturing it at mount (a ref, a lazy useState) means a producer whose `immediate`
    // is false at mount can never fire however the deps move afterwards -- and a lazily gated
    // lookup silently never runs.
    expect(effect, 'the effect must read `immediate` when it runs, not a value captured at mount').toContain('if (immediate)')
    expect(effect, 'a mount-captured `immediate` cannot see a later deps change').not.toMatch(/immediate(Ref|AtMount)/)
  })

  it('useAsync_theEffectReRunsOnTheCallersDeps', () => {
    // A hard `[]` here freezes the effect at mount and makes the fact above unreachable.
    expect(SRC, "useAsync must pass the caller's deps to its effect").toContain('opts?.deps ?? []')
  })
})
