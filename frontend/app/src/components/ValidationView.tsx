// Validation playground (M3-09-04) — a tenant-facing surface to assemble an invoice
// payload by hand (or from a preset) and run it against the validation engine
// (M3-09-01/02/03). Structurally mirrors ClientsView.tsx (page chrome, async-state
// switch over `playgroundState`, zero-network idle when no gateway is configured) and
// CreateForm.tsx/EntityFormModal.tsx (the field-label + `.pf-input` markup, the
// line-item repeater pattern). ViolationsTable renders both the clean-pass block and
// the violations table — this view never branches on `violations.length` itself.

import { useState } from 'react'

import { EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { closeGlyph, plusGlyph, shieldGlyph } from '../glyphs'
import { buildInvoicePayload, presetForm, type InvoiceFormState, type LineItemRow, type PresetKey } from '../lib/invoicePayload'
import { playgroundState, validateInvoice, type ValidateResponse } from '../lib/validationApi'
import { ViolationsTable } from './ViolationsTable'
import type { PlatformCtx } from '../types'

const PRESET_BUTTONS: { key: PresetKey; label: string }[] = [
  { key: 'clean', label: 'Clean' },
  { key: 'has-violations', label: 'Has violations' },
  { key: 'minimal', label: 'Minimal' },
]

export function ValidationView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [form, setForm] = useState<InvoiceFormState>(() => presetForm('clean'))
  // Same `base ? … : …` narrowing as ClientsView/EntityFormModal ([A-e]/[A-m]): the
  // producer never actually runs when base is null since `immediate: false` and the
  // Validate button is disabled — playgroundState's base==null short-circuit is what
  // keeps this surface at zero network on a no-gateway build.
  const validation = useAsync<ValidateResponse>(
    () => (base ? validateInvoice(ctx.authedFetch, base, buildInvoicePayload(form)) : Promise.reject(new Error('no gateway configured'))),
    { immediate: false },
  )
  const state = playgroundState(base, validation)

  function updateField(field: keyof Omit<InvoiceFormState, 'lineItems'>, value: string) {
    setForm((f) => ({ ...f, [field]: value }))
  }

  function updateLineItem(idx: number, field: keyof LineItemRow, value: string) {
    setForm((f) => ({ ...f, lineItems: f.lineItems.map((row, i) => (i === idx ? { ...row, [field]: value } : row)) }))
  }

  function addLineItem() {
    setForm((f) => ({ ...f, lineItems: [...f.lineItems, { description: '', id: '' }] }))
  }

  function removeLineItem(idx: number) {
    setForm((f) => ({ ...f, lineItems: f.lineItems.filter((_, i) => i !== idx) }))
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Validation playground</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>Assemble an invoice payload and run it against the Nigeria MBS rule pack.</p>
      </div>

      <div style={{ display: 'flex', gap: 8, marginBottom: 20 }}>
        {PRESET_BUTTONS.map((p) => (
          <button key={p.key} type="button" onClick={() => setForm(presetForm(p.key))} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, fontSize: 13 }}>
            {p.label}
          </button>
        ))}
      </div>

      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: 20, marginBottom: 20 }}>
        <div className="label" style={{ marginBottom: 12 }}>
          Invoice
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 20 }}>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Supplier name</div>
            <input className="pf-input" type="text" value={form.supplierName} onChange={(e) => updateField('supplierName', e.target.value)} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Supplier TIN</div>
            <input className="pf-input" type="text" value={form.supplierTin} onChange={(e) => updateField('supplierTin', e.target.value)} placeholder="########-####" style={{ fontFamily: 'var(--font-mono)' }} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Buyer TIN</div>
            <input className="pf-input" type="text" value={form.buyerTin} onChange={(e) => updateField('buyerTin', e.target.value)} placeholder="########-####" style={{ fontFamily: 'var(--font-mono)' }} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Invoice number</div>
            <input className="pf-input" type="text" value={form.invoiceNumber} onChange={(e) => updateField('invoiceNumber', e.target.value)} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Issue date</div>
            <input className="pf-input" type="text" value={form.issueDate} onChange={(e) => updateField('issueDate', e.target.value)} placeholder="YYYY-MM-DD" style={{ fontFamily: 'var(--font-mono)' }} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Currency</div>
            <input className="pf-input" type="text" value={form.currency} onChange={(e) => updateField('currency', e.target.value)} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Subtotal</div>
            <input className="pf-input" type="text" value={form.subtotal} onChange={(e) => updateField('subtotal', e.target.value)} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>VAT</div>
            <input className="pf-input" type="text" value={form.vat} onChange={(e) => updateField('vat', e.target.value)} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Total</div>
            <input className="pf-input" type="text" value={form.total} onChange={(e) => updateField('total', e.target.value)} />
          </div>
        </div>

        <div className="label" style={{ marginBottom: 12 }}>
          Line items
        </div>
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 6, overflow: 'hidden', marginBottom: 14 }}>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 200px 40px', gap: 10, padding: '9px 12px', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
            <span className="label">Description</span>
            <span className="label">ID</span>
            <span />
          </div>
          {form.lineItems.map((row, i) => (
            <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 200px 40px', gap: 10, padding: '9px 12px', borderBottom: '1px solid var(--line-1)', alignItems: 'center' }}>
              <input className="pf-input" type="text" value={row.description} onChange={(e) => updateLineItem(i, 'description', e.target.value)} />
              <input className="pf-input" type="text" value={row.id} onChange={(e) => updateLineItem(i, 'id', e.target.value)} style={{ fontFamily: 'var(--font-mono)' }} />
              <button
                type="button"
                onClick={() => removeLineItem(i)}
                className="pf-btn"
                aria-label="Remove line item"
                style={{ width: 30, height: 30, borderRadius: 6, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
              >
                {closeGlyph}
              </button>
            </div>
          ))}
        </div>
        <button
          type="button"
          onClick={addLineItem}
          className="pf-chip"
          style={{ height: 30, padding: '0 12px', borderRadius: 6, fontFamily: 'var(--font-sans)', fontSize: 12.5, fontWeight: 500, border: '1px dashed var(--line-3)', background: 'transparent', color: 'var(--fg-2)', display: 'inline-flex', alignItems: 'center', gap: 6 }}
        >
          <span style={{ display: 'inline-flex' }}>{plusGlyph}</span> Add line item
        </button>
      </div>

      <button onClick={validation.run} disabled={base == null || validation.status === 'loading'} className="v2-btn v2-btn-primary pf-btn" style={{ marginBottom: 24 }}>
        <span style={{ display: 'inline-flex', marginRight: -2 }}>{shieldGlyph}</span> Validate
      </button>

      {state === 'loading' && <Loading label="Validating…" />}

      {state === 'error' && validation.error && <ErrorState error={validation.error} onRetry={validation.run} />}

      {state === 'ready' && validation.data && <ViolationsTable violations={validation.data.violations} ruleSetVersion={validation.data.rule_set_version} />}

      {state === 'idle' && <EmptyState title="No results yet" message="Assemble a payload and validate." />}
    </div>
  )
}
