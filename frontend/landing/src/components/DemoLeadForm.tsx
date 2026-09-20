// The shared Book-a-Demo lead-capture form (task-1108, extracted from DemoModal).
// Rendered inside DemoModal's card and, from BUG-19-02, inside DemoCta's bare card.

import { useEffect, useRef, useState } from 'react'
import type { ChangeEvent, FormEvent, ReactNode } from 'react'
import {
  validateDemoForm,
  firstNameOf,
  CONSENT_TEXT,
  TAXPAYER_SIZE_OPTIONS,
  ROLE_OPTIONS,
  VOLUME_OPTIONS,
  DEFAULT_FORM,
  type DemoFormErrors,
  type DemoFormState,
  type DemoFieldKey,
  type DemoStep,
} from './demoForm'
import { resolveSubmitTarget, submitDemoLead, type DemoLead } from '../hubspot'
import { trackedHubSpotSubmit } from '../analytics'

export function Glyph({ d, size = 16, sw = 1.7 }: { d: string | string[]; size?: number; sw?: number }) {
  const paths = Array.isArray(d) ? d : [d]
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={sw} strokeLinecap="round" strokeLinejoin="round">
      {paths.map((p, i) => (
        <path key={i} d={p} />
      ))}
    </svg>
  )
}

// Shared warning-triangle glyph (used for inline field errors and the error-state icon).
const WARN_PATHS = [
  'M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z',
  'M12 9v4',
  'M12 17h.01',
]

// The form's half of DemoModal's single <style> element — interpolated there so the
// popup still renders exactly one <style>; DemoCta (BUG-19-02) renders its own.
export const DEMO_FORM_CSS = `
  @keyframes dmSpin { to { transform: rotate(360deg); } }
  .dm-input, .dm-select { transition: border-color 120ms, box-shadow 120ms; }
  .dm-input:focus, .dm-select:focus { border-color: var(--action) !important; box-shadow: 0 0 0 3px var(--action-glow); outline: none; }
  .dm-err { border-color: var(--status-red-text) !important; }
  .dm-select { appearance: none; -webkit-appearance: none; }
  @media (max-width: 480px) { .dm-row { flex-direction: column !important; align-items: stretch !important; } }
`

export function DemoLeadForm({
  idPrefix,
  variant,
  heading,
  onDone,
  submit,
}: {
  idPrefix: string
  variant: 'modal' | 'card'
  heading?: ReactNode
  onDone?: () => void
  submit?: (lead: DemoLead) => Promise<void>
}) {
  const [form, setForm] = useState<DemoFormState>(DEFAULT_FORM)
  const [errors, setErrors] = useState<DemoFormErrors>({})
  const [demoStep, setDemoStep] = useState<DemoStep>('form')
  const submitTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const mounted = useRef(true)
  const firstFormPanel = useRef(true)

  // Demo-mode stub: client-side theater, always resolves to success after ~1300ms
  // (matching the prototype's submitDemo). Its timer is stored on the shared
  // submitTimer ref so unmount cleanup still clears it. Deliberately ONE helper
  // with the delay written once — a closed HubSpot gate and a tripped honeypot
  // both route through it, and they must be timing-indistinguishable.
  const runStub = () =>
    new Promise<void>((resolve) => {
      submitTimer.current = setTimeout(resolve, 1300)
    })

  // Clear any pending submit timer on unmount. Escape/focus-restore stay on
  // DemoModal, which owns the overlay.
  useEffect(
    () => () => {
      if (submitTimer.current) clearTimeout(submitTimer.current)
      mounted.current = false
    },
    [],
  )

  // Whichever panel is showing owns the focus, and it takes it exactly once — when
  // that panel appears. See DemoModal.tsx's former header comment for the full
  // rationale (deferred setTimeout focus used to race a submit inside the window).
  // The card's first form panel does not steal focus on mount — only a later
  // "form" panel (after a retry) does; the modal always focuses on mount.
  useEffect(() => {
    if (demoStep === 'form') {
      const skip = variant === 'card' && firstFormPanel.current
      firstFormPanel.current = false
      if (!skip) document.getElementById(`${idPrefix}-name`)?.focus()
    } else if (demoStep === 'success') {
      ;(document.getElementById(`${idPrefix}-success-done`) ?? document.getElementById(`${idPrefix}-success`))?.focus()
    } else if (demoStep === 'error') {
      document.getElementById(`${idPrefix}-error-retry`)?.focus()
    }
  }, [demoStep, idPrefix, variant])

  function setField(key: DemoFieldKey, value: string) {
    setForm((prev) => ({ ...prev, [key]: value }))
    if (key === 'name' || key === 'email' || key === 'company') {
      setErrors((prev) => ({ ...prev, [key]: undefined }))
    }
  }

  // setField's sibling for the one boolean: a computed-key spread carrying
  // `value: string` cannot also carry a checkbox.
  function setConsent(next: boolean) {
    setForm((prev) => ({ ...prev, consent: next }))
    setErrors((prev) => ({ ...prev, consent: undefined }))
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (demoStep === 'submitting') return
    // Read the honeypot off the form SYNCHRONOUSLY, before any await: React clears
    // e.currentTarget once the handler's synchronous portion returns, so a
    // post-await read would always see null and the trap would silently never fire.
    // Reading the DOM (rather than mirroring it into state) is also what catches
    // the naive bots that assign input.value directly without dispatching an event.
    const trap = String(new FormData(e.currentTarget).get('website') ?? '').trim()
    const nextErrors = validateDemoForm(form)
    if (Object.keys(nextErrors).length) {
      setErrors(nextErrors)
      const firstKey = (['name', 'email', 'company', 'consent'] as const).find((k) => nextErrors[k])
      if (firstKey) document.getElementById(`${idPrefix}-${firstKey}`)?.focus()
      return
    }
    setErrors({})
    setDemoStep('submitting')

    // Built field by field from the seven answers. The honeypot's value is
    // deliberately absent — it is not part of the form's data model.
    const lead: DemoLead = {
      name: form.name,
      email: form.email,
      company: form.company,
      role: form.role,
      size: form.size,
      volume: form.volume,
      consent: form.consent,
    }

    // All four branches share ONE success/error transition. A tripped honeypot is
    // dropped by running the very same stub a closed gate runs — no early return,
    // no distinguishable timing, and nothing written to the console on any path,
    // so a bot cannot tell a silent drop from a real submit. The error branch is
    // reachable by construction: any rejection — a HubSpot non-2xx, or an injected
    // failing `submit` in QA/tests — routes here.
    try {
      if (trap) await runStub()
      else if (submit) await submit(lead)
      else {
        const target = resolveSubmitTarget(window.location.hostname)
        // Wrapped here, not around the shared success transition below: this is the
        // only branch of the four that reaches HubSpot.
        if (target) await trackedHubSpotSubmit(() => submitDemoLead(target, lead, CONSENT_TEXT))
        else await runStub()
      }
      if (mounted.current) setDemoStep('success')
    } catch {
      if (mounted.current) setDemoStep('error')
    }
  }

  // The form panel does not exist yet at this point — the effect above focuses
  // `${idPrefix}-name` once React has put it back in the DOM.
  function retry() {
    setDemoStep('form')
  }

  const submitting = demoStep === 'submitting'
  const formPadding = variant === 'modal' ? 20 : 0
  const panelPadding = variant === 'modal' ? '32px 22px 24px' : 0

  return (
    <>
      {(demoStep === 'form' || demoStep === 'submitting') && (
        <form noValidate onSubmit={handleSubmit} style={{ padding: formPadding }}>
          {heading}

          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div>
              <label htmlFor={`${idPrefix}-name`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Full name <span style={{ color: 'var(--status-red-text)' }}>*</span>
              </label>
              <input
                id={`${idPrefix}-name`}
                type="text"
                className={'dm-input' + (errors.name ? ' dm-err' : '')}
                value={form.name}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setField('name', e.target.value)}
                placeholder="Ada Okafor"
                autoComplete="name"
                aria-required="true"
                aria-invalid={Boolean(errors.name)}
                aria-describedby={errors.name ? `${idPrefix}-name-error` : undefined}
                disabled={submitting}
                style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)' }}
              />
              {errors.name && (
                <div id={`${idPrefix}-name-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.name}
                </div>
              )}
            </div>

            <div>
              <label htmlFor={`${idPrefix}-email`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Work email <span style={{ color: 'var(--status-red-text)' }}>*</span>
              </label>
              <input
                id={`${idPrefix}-email`}
                type="email"
                className={'dm-input' + (errors.email ? ' dm-err' : '')}
                value={form.email}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setField('email', e.target.value)}
                placeholder="you@company.com"
                autoComplete="email"
                aria-required="true"
                aria-invalid={Boolean(errors.email)}
                aria-describedby={errors.email ? `${idPrefix}-email-error` : undefined}
                disabled={submitting}
                style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)' }}
              />
              {errors.email && (
                <div id={`${idPrefix}-email-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.email}
                </div>
              )}
            </div>

            <div>
              <label htmlFor={`${idPrefix}-company`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Company <span style={{ color: 'var(--status-red-text)' }}>*</span>
              </label>
              <input
                id={`${idPrefix}-company`}
                type="text"
                className={'dm-input' + (errors.company ? ' dm-err' : '')}
                value={form.company}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setField('company', e.target.value)}
                placeholder="Okafor & Partners"
                autoComplete="organization"
                aria-required="true"
                aria-invalid={Boolean(errors.company)}
                aria-describedby={errors.company ? `${idPrefix}-company-error` : undefined}
                disabled={submitting}
                style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)' }}
              />
              {errors.company && (
                <div id={`${idPrefix}-company-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.company}
                </div>
              )}
            </div>

            <div>
              <label htmlFor={`${idPrefix}-role`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                Role <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
              </label>
              <div style={{ position: 'relative' }}>
                <select
                  id={`${idPrefix}-role`}
                  className="dm-select"
                  value={form.role}
                  onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('role', e.target.value)}
                  disabled={submitting}
                  style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '0 32px 0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
                >
                  <option value="" disabled>Select…</option>
                  {ROLE_OPTIONS.map((opt) => (
                    <option key={opt} value={opt}>{opt}</option>
                  ))}
                </select>
                <span style={{ position: 'absolute', right: 12, top: '50%', transform: 'translateY(-50%)', pointerEvents: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>
                  <Glyph d="m6 9 6 6 6-6" size={14} sw={1.8} />
                </span>
              </div>
            </div>

            <div className="dm-row" style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                <label htmlFor={`${idPrefix}-size`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Taxpayer size <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                </label>
                <div style={{ position: 'relative' }}>
                  <select
                    id={`${idPrefix}-size`}
                    className="dm-select"
                    value={form.size}
                    onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('size', e.target.value)}
                    disabled={submitting}
                    style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '0 32px 0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
                  >
                    <option value="" disabled>Select…</option>
                    {TAXPAYER_SIZE_OPTIONS.map((opt) => (
                      <option key={opt} value={opt}>{opt}</option>
                    ))}
                  </select>
                  <span style={{ position: 'absolute', right: 12, top: '50%', transform: 'translateY(-50%)', pointerEvents: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>
                    <Glyph d="m6 9 6 6 6-6" size={14} sw={1.8} />
                  </span>
                </div>
              </div>
              <div style={{ flex: 1, minWidth: 0 }}>
                <label htmlFor={`${idPrefix}-volume`} className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Monthly invoices <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                </label>
                <div style={{ position: 'relative' }}>
                  <select
                    id={`${idPrefix}-volume`}
                    className="dm-select"
                    value={form.volume}
                    onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('volume', e.target.value)}
                    disabled={submitting}
                    style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', padding: '0 32px 0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
                  >
                    <option value="" disabled>Select…</option>
                    {VOLUME_OPTIONS.map((opt) => (
                      <option key={opt} value={opt}>{opt}</option>
                    ))}
                  </select>
                  <span style={{ position: 'absolute', right: 12, top: '50%', transform: 'translateY(-50%)', pointerEvents: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>
                    <Glyph d="m6 9 6 6 6-6" size={14} sw={1.8} />
                  </span>
                </div>
              </div>
            </div>

            <div>
              <label htmlFor={`${idPrefix}-consent`} style={{ display: 'flex', alignItems: 'flex-start', gap: 9, fontSize: 12.5, lineHeight: 1.5, color: 'var(--fg-2)', cursor: 'pointer' }}>
                <input
                  id={`${idPrefix}-consent`}
                  type="checkbox"
                  checked={form.consent}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setConsent(e.target.checked)}
                  aria-required="true"
                  aria-invalid={Boolean(errors.consent)}
                  aria-describedby={errors.consent ? `${idPrefix}-consent-error` : undefined}
                  disabled={submitting}
                  style={{ flex: 'none', width: 15, height: 15, marginTop: 2, accentColor: 'var(--action)', cursor: 'pointer' }}
                />
                {/* The imported constant, never a retyped sentence: this is the one
                    mechanism that keeps the wording the visitor was SHOWN identical to
                    the wording submitted as legalConsentOptions.consent.text. */}
                {CONSENT_TEXT}
              </label>
              {errors.consent && (
                <div id={`${idPrefix}-consent-error`} role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                  <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.consent}
                </div>
              )}
            </div>
          </div>

          {/* Honeypot. Bots fill every input they find; humans never see this one, so
              any value here means "not a human" and the submission is dropped
              silently. Off-screen rather than display:none — bots skip display:none
              fields, which would defeat the trap entirely. tabIndex={-1} keeps it out
              of the Tab-trap, and is one half of that fix: an off-screen input still
              has a non-null offsetParent, so isFocusable's tabIndex clause is the
              other half. Uncontrolled and read straight off the form in handleSubmit;
              its value never reaches `lead`. */}
          {/* autoComplete="new-password" (not "off") plus the four vendor data-*
              attributes below are deliberate, not decoration: Chrome profile
              autofill and every major password manager ignore autocomplete="off"
              (Chromium issue 40223868), so a browser or password manager that
              autofills this field turns a REAL human's lead into a silent drop.
              "new-password" is safe here because password-manager "suggest a
              strong password" UI is gated on type="password", and this input
              stays type="text". Do not "tidy" these away. */}
          <div style={{ position: 'absolute', left: -9999, width: 1, height: 1, overflow: 'hidden' }}>
            <input
              type="text"
              name="website"
              tabIndex={-1}
              aria-hidden="true"
              autoComplete="new-password"
              data-lpignore="true"
              data-1p-ignore="true"
              data-bwignore="true"
              data-form-type="other"
            />
          </div>

          <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, marginTop: 18, cursor: 'pointer', gap: 9 }}>
            {submitting ? (
              <>
                <span style={{ width: 15, height: 15, border: '2px solid oklch(100% 0 0 / .4)', borderTopColor: 'var(--text-on-dark)', borderRadius: 99, animation: 'dmSpin 0.7s linear infinite' }} />
                Booking…
              </>
            ) : (
              'Book my demo →'
            )}
          </button>
          <p style={{ fontSize: 12, color: 'var(--fg-3)', textAlign: 'center', margin: '14px 0 0' }}>No card required · Data resident in-region</p>
        </form>
      )}

      {demoStep === 'success' && (
        <div id={`${idPrefix}-success`} tabIndex={-1} style={{ padding: panelPadding, textAlign: 'center' }}>
          <span style={{ width: 48, height: 48, borderRadius: 99, background: 'var(--action-tint)', color: 'var(--action)', display: 'inline-grid', placeItems: 'center', marginBottom: 16 }}>
            <Glyph d="M20 6 9 17l-5-5" size={26} sw={2} />
          </span>
          <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 8px' }}>You're booked</h3>
          <p style={{ fontSize: 13, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 auto 20px', maxWidth: 330 }}>
            Thanks, {firstNameOf(form.name)}. A compliance specialist will email {form.email} within one business day to lock your 20-minute slot.
          </p>
          {onDone && (
            <button id={`${idPrefix}-success-done`} onClick={onDone} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, cursor: 'pointer' }}>Done</button>
          )}
        </div>
      )}

      {demoStep === 'error' && (
        <div style={{ padding: panelPadding, textAlign: 'center' }}>
          <span style={{ width: 48, height: 48, borderRadius: 99, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', display: 'inline-grid', placeItems: 'center', marginBottom: 16 }}>
            <Glyph d={WARN_PATHS} size={26} sw={1.8} />
          </span>
          <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 8px' }}>Something went wrong</h3>
          <p style={{ fontSize: 13, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 auto 20px', maxWidth: 330 }}>We couldn't book your demo just now. Please try again — your details are still here.</p>
          <button id={`${idPrefix}-error-retry`} onClick={retry} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, cursor: 'pointer' }}>Try again</button>
        </div>
      )}
    </>
  )
}
