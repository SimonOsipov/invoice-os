import { useState } from 'react'
import { SAMPLE_INVOICE, type DraftTestResult, type Violation } from '../rulesApi'
import { Fault } from './Fault'
import { Modal } from './Modal'

type Props = {
  result: DraftTestResult | null
  busy: boolean
  onRun: (invoice: Record<string, unknown>) => void
  onClose: () => void
}

const parseInvoice = (text: string): Record<string, unknown> | null => {
  try {
    const v: unknown = JSON.parse(text)
    return v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}

const line = (v: Violation): string => [v.rule_key, v.severity, v.message, v.path].filter(Boolean).join(' · ')

function Column({ title, violations }: { title: string; violations: Violation[] }) {
  return (
    <div style={{ flex: 1, minWidth: 0 }}>
      <div className="label" style={{ marginBottom: 6 }}>
        {title}
      </div>
      {violations.length === 0 && <div style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>No violations.</div>}
      {violations.map((v, i) => (
        <div key={i} className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', padding: '6px 0', borderBottom: '1px solid var(--line-1)', overflowWrap: 'anywhere' }}>
          {line(v)}
        </div>
      ))}
    </div>
  )
}

export function DraftTestModal({ result, busy, onRun, onClose }: Props) {
  const [text, setText] = useState(SAMPLE_INVOICE)
  const invoice = parseInvoice(text)
  const blocked = busy || invoice === null
  return (
    <Modal width={720} onClose={onClose}>
      <div style={{ padding: '20px 24px', borderBottom: '1px solid var(--line-1)' }}>
        <h3 style={{ fontSize: 17, margin: '0 0 4px' }}>Test draft</h3>
        <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0 }}>Runs the whole draft and the version in force against one invoice. Nothing is saved.</p>
      </div>
      <div style={{ padding: '16px 24px', maxHeight: '60vh', overflowY: 'auto' }}>
        <div className="label" style={{ marginBottom: 6 }}>
          Sample invoice
        </div>
        <div className="ops-input ops-field" style={{ display: 'flex', height: 'auto', padding: '8px 11px' }}>
          <textarea
            aria-label="Sample invoice"
            className="mono"
            rows={12}
            value={text}
            onChange={(e) => setText(e.target.value)}
            style={{ border: 0, outline: 'none', background: 'transparent', resize: 'vertical', fontSize: 12, color: 'var(--fg-1)', flex: 1, minWidth: 0, padding: 0 }}
          />
        </div>
        {invoice === null && <Fault text="Invoice JSON does not parse." />}
        {result && (
          <div style={{ marginTop: 16 }}>
            {result.draft.error !== null && <Fault text={result.draft.error} />}
            <div style={{ display: 'flex', gap: 18 }}>
              <Column title={`Draft v${result.draft.rule_set_version}`} violations={result.draft.violations} />
              <Column title={`In force v${result.in_force.rule_set_version}`} violations={result.in_force.violations} />
            </div>
          </div>
        )}
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
        <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
          Cancel
        </button>
        <button
          type="button"
          disabled={blocked}
          onClick={() => invoice && onRun(invoice)}
          className="ops-btn v2-btn v2-btn-primary"
          style={{ height: 38, ...(blocked ? { opacity: 0.45, filter: 'none', cursor: 'not-allowed' } : {}) }}
        >
          Run test
        </button>
      </div>
    </Modal>
  )
}
