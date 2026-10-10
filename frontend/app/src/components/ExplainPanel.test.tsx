// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { EXPLAIN_COPY, fixPatch, type ExplainFix, type ExplainResult } from '../lib/explain'
import type { InvoiceLineItem } from '../lib/invoices'
import type { AuthedFetch } from '../lib/portfolio'
import { AI_DISCLOSURE } from './CreateUpload'
import { ExplainPanel } from './ExplainPanel'

afterEach(cleanup)

const lines: InvoiceLineItem[] = [1, 2].map((n) => ({
  id: `l${n}`,
  line_no: n,
  description: `Item ${n}`,
  quantity: '1',
  unit_price: '10.00',
  line_total: '10.00',
  line_tax: '0.75',
}))
const lineFix: ExplainFix = { field: 'unit_price', label: 'Unit price', line: 2, current: '-5.00', value: '5.00' }
const ok = (fix: ExplainFix | null): ExplainResult => ({ status: 'ok', explanation: 'Because.', fix })

// explain answers `explain`; PATCH answers `patch` (a promise, so a test can hold it pending).
function setup(explain: Promise<ExplainResult>, patch: () => Promise<unknown> = () => Promise.resolve({}), props: Partial<React.ComponentProps<typeof ExplainPanel>> = {}) {
  const authedFetch = vi.fn((_url: string, opts?: { method?: string }) =>
    opts?.method === 'PATCH' ? patch() : explain,
  ) as unknown as AuthedFetch & ReturnType<typeof vi.fn>
  const onAccepted = vi.fn()
  const onAcceptFailed = vi.fn()
  render(
    <ExplainPanel
      ctx={{ authedFetch }}
      base="https://gw"
      invoiceId="inv-1"
      violation={{ rule_key: 'line.sum', severity: 'error', message: 'm', path: 'line_items[2].unit_price' }}
      lines={lines}
      acceptDisabled={false}
      onAccepted={onAccepted}
      onAcceptFailed={onAcceptFailed}
      {...props}
    />,
  )
  const patches = () => (authedFetch as unknown as ReturnType<typeof vi.fn>).mock.calls.filter(([, o]) => o?.method === 'PATCH')
  return { onAccepted, onAcceptFailed, patches }
}

describe('ExplainPanel', () => {
  it('explainPanel_showsExplanationAndLineFix', async () => {
    setup(Promise.resolve(ok(lineFix)))
    expect((await screen.findByTestId('explain-text')).textContent).toBe('Because.')
    expect(screen.getByTestId('explain-fix').textContent).toBe('Line 2 · Unit price: -5.00 → 5.00')
    expect(screen.getByText(EXPLAIN_COPY.proposed)).toBeTruthy()
  })

  it('explainPanel_headerFixHasNoLinePrefix', async () => {
    setup(Promise.resolve(ok({ field: 'currency', label: 'Currency', line: null, current: 'USD', value: 'NGN' })))
    expect((await screen.findByTestId('explain-fix')).textContent).toBe('Currency: USD → NGN')
  })

  it('explainPanel_noPatchWithoutAccept', async () => {
    const { patches } = setup(Promise.resolve(ok(lineFix)))
    await screen.findByTestId('explain-accept')
    expect(patches()).toHaveLength(0)
  })

  it('explainPanel_errorShowsInline', async () => {
    setup(Promise.reject(new Error('invoice is not validated')))
    expect((await screen.findByTestId('explain-error')).textContent).toBe('invoice is not validated')
    expect(screen.queryByTestId('explain-accept')).toBeNull()
  })

  it('explainPanel_acceptSendsTheFixPatchAndReportsOnce', async () => {
    const { patches, onAccepted } = setup(Promise.resolve(ok(lineFix)))
    fireEvent.click(await screen.findByTestId('explain-accept'))
    await waitFor(() => expect(onAccepted).toHaveBeenCalledTimes(1))
    expect(patches()).toHaveLength(1)
    expect(patches()[0][1].body).toEqual(fixPatch(lines, lineFix))
  })

  it('explainPanel_acceptFailureReportsOnce', async () => {
    const msg = 'line_items id must name a line of this invoice'
    const { patches, onAccepted, onAcceptFailed } = setup(Promise.resolve(ok(lineFix)), () => Promise.reject(new Error(msg)))
    fireEvent.click(await screen.findByTestId('explain-accept'))
    await waitFor(() => expect(onAcceptFailed).toHaveBeenCalledTimes(1))
    expect(onAcceptFailed).toHaveBeenCalledWith(msg)
    expect(onAccepted).not.toHaveBeenCalled()
    expect(patches()).toHaveLength(1)
  })

  it('explainPanel_fixWithoutLineHasNoAccept', async () => {
    setup(Promise.resolve(ok({ ...lineFix, line: 3 })))
    await screen.findByTestId('explain-fix')
    expect(screen.queryByTestId('explain-accept')).toBeNull()
  })

  it('explainPanel_acceptInFlightIsDisabled', async () => {
    const { patches } = setup(Promise.resolve(ok(lineFix)), () => new Promise(() => {}))
    const btn = (await screen.findByTestId('explain-accept')) as HTMLButtonElement
    fireEvent.click(btn)
    fireEvent.click(btn)
    await waitFor(() => expect(btn.textContent).toBe(EXPLAIN_COPY.accepting))
    expect(btn.disabled).toBe(true)
    expect(patches()).toHaveLength(1)
  })

  it('explainPanel_acceptDisabledCarriesTitle', async () => {
    const { patches } = setup(Promise.resolve(ok(lineFix)), undefined, { acceptDisabled: true, acceptTitle: 'because' })
    const btn = (await screen.findByTestId('explain-accept')) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(btn.title).toBe('because')
    fireEvent.click(btn)
    expect(patches()).toHaveLength(0)
  })

  it('explainPanel_carriesNoDisclosure', async () => {
    const states: (() => Promise<ExplainResult>)[] = [
      () => new Promise(() => {}),
      () => Promise.resolve(ok(lineFix)),
      () => Promise.resolve({ status: 'unavailable', explanation: null, fix: null }),
      () => Promise.reject(new Error('conflict')),
    ]
    for (const p of states) {
      setup(p())
      await new Promise((r) => setTimeout(r, 0))
      const text = screen.getByTestId('explain-panel').textContent ?? ''
      expect(text).not.toMatch(/\bAI\b/)
      expect(text).not.toMatch(/provider/i)
      expect(text).not.toContain(AI_DISCLOSURE)
      cleanup()
    }
  })
})
