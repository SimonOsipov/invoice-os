// Adversarial/gap-fill coverage for LAND-02-02 (task-314), added at QA Mode B —
// deliberately a SEPARATE file from DemoModal.render.test.tsx so that file's
// RED-then-GREEN commit history (2fb51fc -> af7f275) stays untouched and
// auditable. Two gaps the RED stage's 8 rows could not reach:
//
// 1. The consent checkbox's inline error (aria-invalid / aria-describedby /
//    role="alert") is only rendered once `errors.consent` is truthy — i.e.
//    AFTER a failed validation. renderToStaticMarkup takes one snapshot of the
//    component's INITIAL state (errors starts at `{}` on every fresh render),
//    and this package has no @testing-library to dispatch a submit event and
//    observe a re-render (that is LAND-02-05's job, against the deployed e2e).
//    To reach the error markup anyway WITHOUT touching implementation code,
//    this seeds React's `useState` return value at render time via `vi.doMock`
//    — a render-time seam, not simulated user interaction, so it stays inside
//    this file's "SSR, no interactivity" constraint.
//
//    The mock keys off useState's call ORDER, which DemoModal calls in a fixed
//    sequence every render: 1st `form` (DEFAULT_FORM, an object with a `name`
//    key), 2nd `errors` (a plain object, `{}` by default), 3rd `demoStep` (a
//    string). React's Rules of Hooks guarantee this order never changes across
//    renders of the same component — the same guarantee the hooks system
//    itself is built on — so this is not the usual fragile call-order trick
//    against an opaque API. If DemoModal's own hook order changes, this test
//    needs updating alongside it; that coupling is intentional and documented
//    here, not hidden.
//
// 2. That DEFAULT_FORM.size really IS the imported DEFAULT_TAXPAYER_SIZE
//    constant, not a hardcoded literal that merely happens to match it today
//    (AC #1). Verified via the SSR markup's own `selected=""` marker — no
//    mocking needed, since this is reachable in the plain default render.
import { describe, expect, it, vi } from 'vitest'
import { createElement } from 'react'

import { DEFAULT_TAXPAYER_SIZE } from './demoForm'

function noop() {}

describe('DemoModal consent error aria wiring (LAND-02-02 adversarial, task-314)', () => {
  it('renders aria-invalid="true", aria-describedby, and the role="alert" error once errors.consent is set', async () => {
    vi.resetModules()
    vi.doMock('react', async (importOriginal) => {
      const actual = await importOriginal<typeof import('react')>()
      let call = 0
      return {
        ...actual,
        useState: <T,>(initial: T) => {
          call += 1
          // 2nd useState call in DemoModal is `errors` — seed it with a
          // consent error, exactly as validateDemoForm would after a failed
          // submit with the box unchecked.
          if (call === 2) {
            return actual.useState({ consent: 'Please confirm you agree before we can contact you.' } as unknown as T)
          }
          return actual.useState(initial)
        },
      }
    })

    try {
      const { renderToStaticMarkup } = await import('react-dom/server')
      const { DemoModal } = await import('./DemoModal')
      const html = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))

      const consentInputMatch = html.match(/<input[^>]*id="dm-consent"[^>]*>/)
      expect(consentInputMatch, 'expected to find #dm-consent in the seeded-error render').not.toBeNull()
      const consentInput = consentInputMatch![0]

      // The same pattern the three text inputs already use (AC #2): a
      // BOOLEAN aria-invalid (Boolean(errors.consent) -> true) and
      // aria-describedby pointing at the sibling error node.
      expect(consentInput).toContain('aria-invalid="true"')
      expect(consentInput).toContain('aria-describedby="dm-consent-error"')

      // The inline error itself: id + role="alert" + the exact message text.
      expect(html).toMatch(/<div id="dm-consent-error" role="alert"[^>]*>/)
      expect(html).toContain('Please confirm you agree before we can contact you.')
    } finally {
      vi.doUnmock('react')
      vi.resetModules()
    }
  })
})

describe('DemoModal default taxpayer size (LAND-02-02 adversarial, task-314)', () => {
  it('DEFAULT_FORM.size really is the imported DEFAULT_TAXPAYER_SIZE constant, not a coincidentally-equal literal', async () => {
    // Fresh, un-mocked import — this test runs after the mocked-React test
    // above, so re-import from a clean module registry to guarantee the real
    // react/react-dom are in play here.
    vi.resetModules()
    const { renderToStaticMarkup } = await import('react-dom/server')
    const { DemoModal } = await import('./DemoModal')
    const html = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))

    const sizeSelect = html.match(/<select[^>]*id="dm-size"[^]*?<\/select>/)
    expect(sizeSelect).not.toBeNull()
    const selectedOption = sizeSelect![0].match(/<option[^>]*value="([^"]+)"[^>]*selected(?:=""|\/)?[^>]*>/)
    expect(selectedOption, 'expected exactly one <option selected> in the taxpayer-size select').not.toBeNull()

    // The assertion is against the IMPORTED constant, never a retyped
    // literal — so if DEFAULT_FORM.size and DEFAULT_TAXPAYER_SIZE ever drift
    // apart (e.g. someone edits one but not the other), this goes red instead
    // of silently passing on a copy-pasted string that used to match.
    expect(selectedOption![1]).toBe(DEFAULT_TAXPAYER_SIZE)
  })
})
