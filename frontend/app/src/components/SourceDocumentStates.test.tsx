// @vitest-environment jsdom
// QA adversarial (task-392, BUG-03-03): NoSourceCanvas is actorLabel's third caller, an
// undocumented one the story's plan didn't grep for -- SourceDocumentModal.test.tsx only
// asserts this canvas's static sentence, never the resolved-persona clause it computes.
// An unproven migration is how the original raw-uuid defect got in in the first place.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { NoSourceCanvas } from './SourceDocumentStates'

afterEach(cleanup)

describe('NoSourceCanvas actor resolution ([actor-label-shared])', () => {
  it('a known persona subject resolves to a name, not the raw uuid', () => {
    render(<NoSourceCanvas invoiceNumber="INV-2026-0037" createdAt="2026-06-12T09:15:00Z" createdBy={APP_PERSONAS.firm.subject} />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).toContain(`by ${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`)
    expect(canvas.textContent).not.toContain(APP_PERSONAS.firm.subject)
  })

  // NoSourceCanvas's own contract (SourceDocumentStates.tsx comment: "a raw uuid never
  // appears mid-prose"): unlike the strip's attribution and the kept-banner, an
  // unrecognised subject here is NOT shown raw -- the whole "by ..." clause is dropped.
  it('an unrecognised subject omits the "by" clause instead of leaking a raw uuid mid-prose', () => {
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    render(<NoSourceCanvas invoiceNumber="INV-2026-0037" createdAt="2026-06-12T09:15:00Z" createdBy={unknown} />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).not.toContain(unknown)
    // "ASComply on ..." not "ASComply by ... on ..." -- the clause is dropped whole, not
    // filled with the raw uuid. (Narrower than ' by ': the fallback prose below contains
    // "entered by hand", an unrelated match.)
    expect(canvas.textContent).toContain('into ASComply on')
  })

  it('a null creator also omits the "by" clause', () => {
    render(<NoSourceCanvas invoiceNumber="INV-2026-0037" createdAt="2026-06-12T09:15:00Z" createdBy={null} />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).toContain('into ASComply on')
    expect(canvas.textContent).not.toContain('Not recorded')
  })
})

// AUDIT-02-04 Stage-4. `system` actors the genesis row of every seeded invoice
// (db/seed.dev.sql:628) and now resolves to a NAME, so the old `!creator.mono` gate wrote
// "typed into ASComply by System" -- nobody typed it in. Only a person is ever named.
describe('NoSourceCanvas names a person and nobody else ([actor-label-shared])', () => {
  it('a system actor omits the "by" clause', () => {
    render(<NoSourceCanvas invoiceNumber="DEMO-2026-1009" createdAt="2026-06-12T09:15:00Z" createdBy="system" />)

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).not.toContain('by System')
    expect(canvas.textContent).toContain('into ASComply on')
  })

  it("the server's resolved pair decides the clause, not APP_PERSONAS", () => {
    render(
      <NoSourceCanvas
        invoiceNumber="DEMO-2026-1009"
        createdAt="2026-06-12T09:15:00Z"
        createdBy={APP_PERSONAS.inhouse.subject}
        createdByResolved={{ name: 'System', kind: 'system' }}
      />,
    )

    const canvas = screen.getByTestId('source-document-no-source')
    expect(canvas.textContent).not.toContain('by System')
    expect(canvas.textContent).not.toContain(APP_PERSONAS.inhouse.name)
    expect(canvas.textContent).toContain('into ASComply on')
  })
})
