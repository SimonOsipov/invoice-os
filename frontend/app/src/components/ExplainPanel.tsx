import { useRef, useState } from 'react'
import { useAsync } from '@invoice-os/api-client'

import { EXPLAIN_COPY, explainViolation, fixPatch, type ExplainResult } from '../lib/explain'
import { editInvoice, type InvoiceLineItem } from '../lib/invoices'
import { violationKey, type Violation } from '../lib/validationApi'
import type { AuthedFetch } from '../lib/portfolio'

export interface ExplainPanelProps {
  ctx: { authedFetch: AuthedFetch }
  base: string
  invoiceId: string
  violation: Violation
  lines: readonly InvoiceLineItem[]
  acceptDisabled: boolean
  acceptTitle?: string
  onAccepted: () => void
  onAcceptFailed: (message: string) => void
}

const NOTE = { fontSize: 12.5, color: 'var(--fg-2)', lineHeight: 1.5 } as const

export function ExplainPanel({ ctx, base, invoiceId, violation, lines, acceptDisabled, acceptTitle, onAccepted, onAcceptFailed }: ExplainPanelProps): React.JSX.Element {
  const explain = useAsync<ExplainResult>(() => explainViolation(ctx.authedFetch, base, invoiceId, violation), {
    deps: [invoiceId, violationKey(violation)],
  })
  const [accepting, setAccepting] = useState(false)
  const inFlight = useRef(false)

  const result = explain.data
  const fix = result?.status === 'ok' ? result.fix : null
  const patch = fix ? fixPatch(lines, fix) : null

  const accept = async () => {
    if (acceptDisabled || inFlight.current || patch === null) return
    inFlight.current = true
    setAccepting(true)
    try {
      await editInvoice(ctx.authedFetch, base, invoiceId, patch)
      onAccepted()
    } catch (err) {
      onAcceptFailed(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
    } finally {
      inFlight.current = false
      setAccepting(false)
    }
  }

  const disabled = acceptDisabled || accepting

  return (
    <div data-testid="explain-panel" style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: '4px 0', minWidth: 0 }}>
      {explain.status === 'loading' && (
        <div data-testid="explain-loading" style={NOTE}>{EXPLAIN_COPY.loading}</div>
      )}
      {explain.status === 'error' && (
        <div
          data-testid="explain-error"
          style={{ padding: '9px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12.5, color: 'var(--status-red-text)', overflowWrap: 'anywhere' }}
        >
          {explain.error?.message}
        </div>
      )}
      {result?.status === 'unavailable' && (
        <div data-testid="explain-unavailable" style={NOTE}>{EXPLAIN_COPY.unavailable}</div>
      )}
      {result?.status === 'ok' && (
        <>
          {result.explanation && (
            <div data-testid="explain-text" style={{ ...NOTE, color: 'var(--fg-1)', overflowWrap: 'anywhere' }}>{result.explanation}</div>
          )}
          {fix && (
            <>
              <div className="label">{EXPLAIN_COPY.proposed}</div>
              <div data-testid="explain-fix" className="mono" style={{ fontSize: 12, color: 'var(--fg-1)', overflowWrap: 'anywhere' }}>
                {fix.line !== null ? `Line ${fix.line} · ` : ''}{fix.label}: {fix.current ?? '—'} → {fix.value}
              </div>
            </>
          )}
          {patch !== null && (
            <div>
              <button
                type="button"
                data-testid="explain-accept"
                disabled={disabled}
                title={acceptTitle}
                onClick={accept}
                className="v2-btn v2-btn-primary pf-btn"
                style={{ height: 30, ...(disabled ? { opacity: 0.45, cursor: 'not-allowed', filter: 'none' } : null) }}
              >
                {accepting ? EXPLAIN_COPY.accepting : EXPLAIN_COPY.accept}
              </button>
            </div>
          )}
        </>
      )}
    </div>
  )
}
