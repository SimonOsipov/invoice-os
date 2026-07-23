// Entity add/edit modal (M3-08-05, task-60) — a thin JSX shell over the pure
// lib/entityForm.ts helpers (M3-08-02): no validation/diff/error-mapping logic lives
// here, it's all delegated to the already-tested pure functions. Structurally mirrors
// XmlModal.tsx (fixed backdrop, stopPropagation'd panel, header + close button) with a
// footer of Cancel/Submit buttons in the app's `pf-input`/`v2-btn` visual language
// (CreateForm.tsx's label + input convention).
//
// Open/mode/edit-target state lives in ClientsView's local useState ([A-l]) — this
// component only renders the form and orchestrates submit; it never fetches the list
// itself (that's ClientsView's `list.run()`, invoked via onSuccess).
//
// Submit orchestration (Obsidian M3-08 §6):
// - create: createEntity(ctx.authedFetch, base, toEntityInput(state))
// - edit:   diff = toEntityUpdateInput(entity, state); an empty diff skips the PATCH
//   entirely and just closes (avoids the backend's all-nil 400, [A-k]) — a cleared
//   optional (e.g. Sector) is sent as '' so the backend clears it (F6/[A-k], GATE
//   verified against store.go:185-194).
// - success -> onSuccess() (parent refetches via list.run() + closes)
// - ApiError -> mapSubmitError: {field:'tin', message} shows on the TIN field;
//   {message} (no field) shows as a form-level banner; null (401) shows nothing — the
//   authedFetch seam has already called signOut and the surface is unmounting to
//   <SignIn> ([A-c]). Client-side validation is presence-only (name+TIN); TIN
//   grammar/duplicate detection is the server's job, surfaced from 400/409 ([A-g]).
import { useState, type FormEvent } from 'react'

import { ApiError } from '@invoice-os/api-client'

import { closeGlyph } from '../glyphs'
import {
  emptyEntityForm,
  entityFormFrom,
  mapSubmitError,
  toEntityInput,
  toEntityUpdateInput,
  validateEntityForm,
  type EntityFormErrors,
  type EntityFormState,
} from '../lib/entityForm'
import { createEntity, updateEntity, type Entity } from '../lib/portfolio'
import type { PlatformCtx } from '../types'

export interface EntityFormModalProps {
  mode: 'create' | 'edit'
  entity?: Entity
  ctx: PlatformCtx
  base: string
  onClose: () => void
  onSuccess: () => void
}

export function EntityFormModal({ mode, entity, ctx, base, onClose, onSuccess }: EntityFormModalProps) {
  const [form, setForm] = useState<EntityFormState>(() => (mode === 'edit' && entity ? entityFormFrom(entity) : emptyEntityForm()))
  const [fieldErrors, setFieldErrors] = useState<EntityFormErrors>({})
  const [formError, setFormError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const title = mode === 'create' ? 'Add client' : 'Edit client'

  function updateField<K extends keyof EntityFormState>(field: K, value: string) {
    setForm((f) => ({ ...f, [field]: value }))
  }

  function handleError(err: unknown) {
    if (!(err instanceof ApiError)) {
      setFormError('Something went wrong. Please try again.')
      return
    }
    const mapped = mapSubmitError(err)
    if (mapped === null) return // 401 — the authedFetch seam already fired signOut
    if (mapped.field === 'tin') {
      setFieldErrors((e) => ({ ...e, tin: mapped.message }))
    } else {
      setFormError(mapped.message)
    }
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting) return // double-submit guard

    const result = validateEntityForm(form)
    if (!result.valid) {
      setFieldErrors(result.errors)
      setFormError(null)
      return
    }
    setFieldErrors({})
    setFormError(null)

    if (mode === 'edit') {
      if (!entity) return // defensive: ClientsView always passes entity for edit mode
      const diff = toEntityUpdateInput(entity, form)
      if (Object.keys(diff).length === 0) {
        onClose() // nothing changed — skip the PATCH, avoids the backend's all-nil 400
        return
      }
      setSubmitting(true)
      try {
        await updateEntity(ctx.authedFetch, base, entity.id, diff)
        onSuccess()
      } catch (err) {
        handleError(err)
      } finally {
        setSubmitting(false)
      }
      return
    }

    setSubmitting(true)
    try {
      await createEntity(ctx.authedFetch, base, toEntityInput(form))
      onSuccess()
    } catch (err) {
      handleError(err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div
      onClick={() => { if (!submitting) onClose() }}
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'rgba(20,23,26,0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        style={{ width: 480, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', boxShadow: '0 24px 60px -20px rgba(20,23,26,0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div style={{ fontSize: 15, fontWeight: 600 }}>{title}</div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            style={{ width: 34, height: 34, borderRadius: 'var(--radius-lg)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <form onSubmit={handleSubmit} style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
          <div style={{ flex: 1, overflow: 'auto', padding: 20 }}>
            {formError && (
              <div style={{ marginBottom: 16, padding: '10px 12px', borderRadius: 'var(--radius-lg)', background: 'var(--status-red-bg)', border: '1px solid var(--status-red-border)', fontSize: 12.5, color: 'var(--status-red-text)' }}>
                {formError}
              </div>
            )}

            <div style={{ marginBottom: 14 }}>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Name</div>
              <input className="pf-input" value={form.name} onChange={(e) => updateField('name', e.target.value)} disabled={submitting} />
              {fieldErrors.name && <div style={{ marginTop: 6, fontSize: 11.5, color: 'var(--status-red-text)' }}>{fieldErrors.name}</div>}
            </div>

            <div style={{ marginBottom: 14 }}>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>TIN</div>
              <input
                className="pf-input"
                value={form.tin}
                onChange={(e) => updateField('tin', e.target.value)}
                disabled={submitting}
                placeholder="########-####"
                style={{ fontFamily: 'var(--font-mono)' }}
              />
              {fieldErrors.tin && <div style={{ marginTop: 6, fontSize: 11.5, color: 'var(--status-red-text)' }}>{fieldErrors.tin}</div>}
            </div>

            <div style={{ marginBottom: 14 }}>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Registration (optional)</div>
              <input className="pf-input" value={form.registration} onChange={(e) => updateField('registration', e.target.value)} disabled={submitting} />
            </div>

            <div style={{ marginBottom: 14 }}>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Sector (optional)</div>
              <input className="pf-input" value={form.sector} onChange={(e) => updateField('sector', e.target.value)} disabled={submitting} />
            </div>

            <div>
              <div style={{ fontSize: 12, color: 'var(--fg-2)', marginBottom: 6 }}>Address (optional)</div>
              <input className="pf-input" value={form.address} onChange={(e) => updateField('address', e.target.value)} disabled={submitting} />
            </div>
          </div>

          <div style={{ flex: 'none', display: 'flex', justifyContent: 'flex-end', gap: 9, padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
            <button type="button" onClick={onClose} disabled={submitting} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, fontSize: 13 }}>
              Cancel
            </button>
            <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary pf-btn" style={{ height: 36, fontSize: 13 }}>
              {submitting ? 'Saving…' : mode === 'create' ? 'Add client' : 'Save changes'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
