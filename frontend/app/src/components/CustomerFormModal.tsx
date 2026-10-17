import { useState, type FormEvent } from 'react'

import { chevDownGlyph, closeGlyph } from '../glyphs'
import {
  BUYER_TYPES,
  buyerFormFrom,
  mapCorrectionError,
  toCustomerUpdateInput,
  updateCustomer,
  type BuyerFormState,
  type Customer,
} from '../lib/buyerDirectory'
import type { PlatformCtx } from '../types'

const TITLE = 'Edit customer'

const TEXT_FIELDS: Array<{ key: Exclude<keyof BuyerFormState, 'buyerType'>; label: string }> = [
  { key: 'name', label: 'Name' },
  { key: 'contactPerson', label: 'Contact person' },
  { key: 'email', label: 'Email' },
  { key: 'telephone', label: 'Phone' },
  { key: 'street', label: 'Street' },
  { key: 'city', label: 'City' },
  { key: 'postalZone', label: 'Postal zone' },
  { key: 'country', label: 'Country' },
  { key: 'state', label: 'State' },
  { key: 'lga', label: 'LGA' },
]

const LABEL: React.CSSProperties = { fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }

export function CustomerFormModal({ customer, ctx, base, onClose, onSuccess }: { customer: Customer; ctx: PlatformCtx; base: string; onClose: () => void; onSuccess: () => void }) {
  const [form, setForm] = useState<BuyerFormState>(() => buyerFormFrom(customer))
  const [formError, setFormError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  function updateField(field: keyof BuyerFormState, value: string) {
    setForm((f) => ({ ...f, [field]: value }))
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting) return
    setFormError(null)
    const diff = toCustomerUpdateInput(customer, form)
    if (Object.keys(diff).length === 0) {
      onClose() // the server answers an empty PATCH with 400
      return
    }
    setSubmitting(true)
    try {
      await updateCustomer(ctx.authedFetch, base, customer.id, diff)
      onSuccess()
    } catch (err) {
      setFormError(mapCorrectionError(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div
      onClick={() => { if (!submitting) onClose() }}
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={TITLE}
        style={{ width: 480, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', boxShadow: 'var(--shadow-card)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div className="card-title">{TITLE}</div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            style={{ width: 34, height: 34, borderRadius: 'var(--radius-md)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <form onSubmit={handleSubmit} style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
          <div style={{ flex: 1, overflow: 'auto', padding: 20 }}>
            {formError && (
              <div style={{ marginBottom: 16, padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12.5, color: 'var(--status-red-text)' }}>
                {formError}
              </div>
            )}

            <div style={{ marginBottom: 14 }}>
              <div style={LABEL}>TIN</div>
              <div className="mono" style={{ fontSize: 13 }}>{customer.tin}</div>
            </div>

            {TEXT_FIELDS.slice(0, 2).map(({ key, label }) => (
              <div key={key} style={{ marginBottom: 14 }}>
                <div style={LABEL}>{label}</div>
                <input aria-label={label} className="pf-input" value={form[key]} onChange={(e) => updateField(key, e.target.value)} disabled={submitting} />
              </div>
            ))}

            <div style={{ marginBottom: 14 }}>
              <div style={LABEL}>Buyer type</div>
              <span style={{ position: 'relative', display: 'block' }}>
                <select aria-label="Buyer type" className="pf-input" value={form.buyerType} onChange={(e) => updateField('buyerType', e.target.value)} disabled={submitting}>
                  <option value="">—</option>
                  {BUYER_TYPES.map((t) => (
                    <option key={t} value={t}>{t}</option>
                  ))}
                </select>
                <span aria-hidden="true" style={{ position: 'absolute', right: 11, top: '50%', transform: 'translateY(-50%)', display: 'inline-flex', color: 'var(--fg-3)', pointerEvents: 'none' }}>
                  {chevDownGlyph}
                </span>
              </span>
            </div>

            {TEXT_FIELDS.slice(2).map(({ key, label }, i, rest) => (
              <div key={key} style={i === rest.length - 1 ? undefined : { marginBottom: 14 }}>
                <div style={LABEL}>{label}</div>
                <input aria-label={label} className="pf-input" value={form[key]} onChange={(e) => updateField(key, e.target.value)} disabled={submitting} />
              </div>
            ))}
          </div>

          <div style={{ flex: 'none', display: 'flex', justifyContent: 'flex-end', gap: 9, padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
            <button type="button" onClick={onClose} disabled={submitting} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, fontSize: 13 }}>
              Cancel
            </button>
            <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary pf-btn" style={{ height: 36, fontSize: 13 }}>
              {submitting ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
