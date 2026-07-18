// The Book-a-Demo lead-capture modal (task-117 / M4-19), ported from the refreshed
// FiscalBridge Africa landing prototype (.ralph/prototype/InvoiceOS-Africa.dc.html,
// "Book a demo" modal). Shell (overlay/card/header) is cloned VERBATIM from
// SignInModal.tsx — including its "FiscalBridge" brand, which is already correct
// per task-99 (the prototype still shows the old "InvoiceOS" codename; normalized
// here). Submit is client-side theater only (no backend, no persistence), matching
// the existing landing pattern (auth.ts). Mount = open: the parent renders
// `{demoOpen && <DemoModal onClose={...} />}`, same as SignInModal.

import { useEffect, useRef, useState } from 'react'
import type { ChangeEvent, FormEvent, KeyboardEvent as ReactKeyboardEvent } from 'react'
import { BrandMark } from '../icons'
import { validateDemoForm, firstNameOf, type DemoFormErrors } from './demoForm'

function Glyph({ d, size = 16, sw = 1.7 }: { d: string | string[]; size?: number; sw?: number }) {
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

const ROLE_OPTIONS = ['Owner / Partner', 'Finance or Accounting lead', 'Tax / Compliance', 'Developer / IT', 'Other']
const SIZE_OPTIONS = ['Micro', 'Small', 'Medium', 'Large']
const VOLUME_OPTIONS = ['under 1k', '1k–10k', '10k–100k', '100k+']

const DEFAULT_FORM = {
  name: '',
  email: '',
  company: '',
  role: 'Finance or Accounting lead',
  size: 'Medium',
  volume: '1k–10k',
}

type DemoFormState = typeof DEFAULT_FORM
type DemoStep = 'form' | 'submitting' | 'success' | 'error'

// A focusable element is eligible for the Tab-trap if it isn't disabled and is
// actually rendered (offsetParent is null for display:none / detached nodes).
function isFocusable(el: HTMLElement): boolean {
  return !(el as HTMLButtonElement).disabled && el.offsetParent !== null
}

export function DemoModal({ onClose, submit }: { onClose: () => void; submit?: () => Promise<void> }) {
  const [form, setForm] = useState<DemoFormState>(DEFAULT_FORM)
  const [errors, setErrors] = useState<DemoFormErrors>({})
  const [demoStep, setDemoStep] = useState<DemoStep>('form')
  const submitTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const mounted = useRef(true)

  // Default submit stub: client-side theater, always resolves to success after
  // ~1300ms (matching the prototype's submitDemo). Its timer is stored on the
  // shared submitTimer ref so unmount cleanup still clears it. A real `submit`
  // (or an injected failing one from QA/tests) is used instead when provided.
  const runSubmit =
    submit ??
    (() =>
      new Promise<void>((resolve) => {
        submitTimer.current = setTimeout(resolve, 1300)
      }))

  // Close on Escape (never a native dialog); focus the first field on open (added —
  // SignInModal lacks this); clear any pending submit timer on unmount; restore
  // focus to whatever opened the modal so keyboard users land back where they were.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    const focusTimer = setTimeout(() => document.getElementById('dm-name')?.focus(), 60)
    return () => {
      window.removeEventListener('keydown', onKey)
      clearTimeout(focusTimer)
      if (submitTimer.current) clearTimeout(submitTimer.current)
      mounted.current = false
      opener?.focus?.()
    }
  }, [onClose])

  // On reaching a terminal panel (success/error) the <form> — and whatever held
  // focus — unmounts, dropping focus to <body> and silently breaking the Tab-trap
  // (document.activeElement no longer matches the trap's first/last node). Move
  // focus onto that panel's primary button so the trap keeps working.
  useEffect(() => {
    if (demoStep === 'success') document.getElementById('dm-success-done')?.focus()
    else if (demoStep === 'error') document.getElementById('dm-error-retry')?.focus()
  }, [demoStep])

  function setField(key: keyof DemoFormState, value: string) {
    setForm((prev) => ({ ...prev, [key]: value }))
    if (key === 'name' || key === 'email' || key === 'company') {
      setErrors((prev) => ({ ...prev, [key]: undefined }))
    }
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (demoStep === 'submitting') return
    const nextErrors = validateDemoForm(form)
    if (Object.keys(nextErrors).length) {
      setErrors(nextErrors)
      const firstKey = (['name', 'email', 'company'] as const).find((k) => nextErrors[k])
      if (firstKey) document.getElementById('dm-' + firstKey)?.focus()
      return
    }
    setErrors({})
    setDemoStep('submitting')
    // The default runSubmit stub always resolves (client-side theater, matching
    // the prototype's submitDemo), so the happy path never invents a failure
    // sentinel. The error branch is reachable by construction: any rejection —
    // from a future real submit, or an injected failing `submit` in QA/tests —
    // routes here.
    try {
      await runSubmit()
      if (mounted.current) setDemoStep('success')
    } catch {
      if (mounted.current) setDemoStep('error')
    }
  }

  function retry() {
    setDemoStep('form')
    setTimeout(() => document.getElementById('dm-name')?.focus(), 60)
  }

  // Tab focus-trap within the card (added — SignInModal lacks this): Tab on the
  // last focusable wraps to the first; Shift+Tab on the first wraps to the last.
  function trapTab(e: ReactKeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'Tab') return
    const list = Array.from(e.currentTarget.querySelectorAll<HTMLElement>('input,select,button,textarea,a[href]')).filter(isFocusable)
    if (!list.length) return
    const first = list[0]
    const last = list[list.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }

  const submitting = demoStep === 'submitting'

  return (
    <div
      className="if-v2 dm-overlay"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Book a demo"
      style={{ position: 'fixed', inset: 0, zIndex: 200, background: 'rgba(20,23,26,0.44)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24, animation: 'dmOvIn 160ms ease-out' }}
    >
      <style>{`
        @keyframes dmOvIn { from { opacity: 0; } to { opacity: 1; } }
        @keyframes dmCardIn { from { opacity: 0; transform: translateY(10px) scale(0.985); } to { opacity: 1; transform: none; } }
        @keyframes dmSpin { to { transform: rotate(360deg); } }
        .dm-input, .dm-select { transition: border-color 120ms, box-shadow 120ms; }
        .dm-input:focus, .dm-select:focus { border-color: var(--accent) !important; box-shadow: 0 0 0 3px var(--accent-glow); outline: none; }
        .dm-err { border-color: var(--status-red-text) !important; }
        .dm-select { appearance: none; -webkit-appearance: none; }
        .si-close { transition: background 120ms ease-out, color 120ms ease-out; }
        .si-close:hover { background: var(--bg-3); color: var(--fg-1); }
        @media (max-width: 480px) { .dm-row { flex-direction: column !important; } .dm-overlay { padding: 14px !important; } }
      `}</style>

      <div
        onClick={(e) => e.stopPropagation()}
        onKeyDown={trapTab}
        style={{ width: '100%', maxWidth: 452, maxHeight: 'calc(100dvh - 48px)', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 10, boxShadow: '0 32px 64px -24px rgba(20,23,26,0.42)', overflowY: 'auto', animation: 'dmCardIn 200ms var(--ease-out)' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <BrandMark size={19} />
            <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>FiscalBridge</span>
            <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 3, padding: '1px 4px' }}>
              AFRICA
            </span>
          </div>
          <button onClick={onClose} className="si-close" aria-label="Close" style={{ flex: 'none', width: 30, height: 30, borderRadius: 6, border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
            <Glyph d="M18 6 6 18M6 6l12 12" size={17} sw={1.8} />
          </button>
        </div>

        {(demoStep === 'form' || demoStep === 'submitting') && (
          <form noValidate onSubmit={handleSubmit} style={{ padding: 20 }}>
            <div className="label" style={{ marginBottom: 8 }}>/ BOOK A DEMO</div>
            <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 6px' }}>See your invoices pass compliance in real time.</h3>
            <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: '0 0 18px' }}>A 20-minute walkthrough with a compliance specialist. Bring a sample invoice file — we'll validate it live.</p>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              <div>
                <label htmlFor="dm-name" className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Full name <span style={{ color: 'var(--status-red-text)' }}>*</span>
                </label>
                <input
                  id="dm-name"
                  type="text"
                  className={'dm-input' + (errors.name ? ' dm-err' : '')}
                  value={form.name}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setField('name', e.target.value)}
                  placeholder="Ada Okafor"
                  autoComplete="name"
                  aria-required="true"
                  aria-invalid={Boolean(errors.name)}
                  aria-describedby={errors.name ? 'dm-name-error' : undefined}
                  disabled={submitting}
                  style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, padding: '0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)' }}
                />
                {errors.name && (
                  <div id="dm-name-error" role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                    <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.name}
                  </div>
                )}
              </div>

              <div>
                <label htmlFor="dm-email" className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Work email <span style={{ color: 'var(--status-red-text)' }}>*</span>
                </label>
                <input
                  id="dm-email"
                  type="email"
                  className={'dm-input' + (errors.email ? ' dm-err' : '')}
                  value={form.email}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setField('email', e.target.value)}
                  placeholder="you@company.com"
                  autoComplete="email"
                  aria-required="true"
                  aria-invalid={Boolean(errors.email)}
                  aria-describedby={errors.email ? 'dm-email-error' : undefined}
                  disabled={submitting}
                  style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, padding: '0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)' }}
                />
                {errors.email && (
                  <div id="dm-email-error" role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                    <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.email}
                  </div>
                )}
              </div>

              <div>
                <label htmlFor="dm-company" className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Company <span style={{ color: 'var(--status-red-text)' }}>*</span>
                </label>
                <input
                  id="dm-company"
                  type="text"
                  className={'dm-input' + (errors.company ? ' dm-err' : '')}
                  value={form.company}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setField('company', e.target.value)}
                  placeholder="Okafor & Partners"
                  autoComplete="organization"
                  aria-required="true"
                  aria-invalid={Boolean(errors.company)}
                  aria-describedby={errors.company ? 'dm-company-error' : undefined}
                  disabled={submitting}
                  style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, padding: '0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)' }}
                />
                {errors.company && (
                  <div id="dm-company-error" role="alert" style={{ display: 'flex', alignItems: 'center', gap: 7, marginTop: 7, fontSize: 12.5, color: 'var(--status-red-text)' }}>
                    <Glyph d={WARN_PATHS} size={15} sw={1.7} /> {errors.company}
                  </div>
                )}
              </div>

              <div>
                <label htmlFor="dm-role" className="label" style={{ display: 'block', marginBottom: 6 }}>
                  Role <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                </label>
                <div style={{ position: 'relative' }}>
                  <select
                    id="dm-role"
                    className="dm-select"
                    value={form.role}
                    onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('role', e.target.value)}
                    disabled={submitting}
                    style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, padding: '0 32px 0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
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
                  <label htmlFor="dm-size" className="label" style={{ display: 'block', marginBottom: 6 }}>
                    Taxpayer size <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                  </label>
                  <div style={{ position: 'relative' }}>
                    <select
                      id="dm-size"
                      className="dm-select"
                      value={form.size}
                      onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('size', e.target.value)}
                      disabled={submitting}
                      style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, padding: '0 32px 0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
                    >
                      <option value="" disabled>Select…</option>
                      {SIZE_OPTIONS.map((opt) => (
                        <option key={opt} value={opt}>{opt}</option>
                      ))}
                    </select>
                    <span style={{ position: 'absolute', right: 12, top: '50%', transform: 'translateY(-50%)', pointerEvents: 'none', color: 'var(--fg-3)', display: 'inline-flex' }}>
                      <Glyph d="m6 9 6 6 6-6" size={14} sw={1.8} />
                    </span>
                  </div>
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <label htmlFor="dm-volume" className="label" style={{ display: 'block', marginBottom: 6 }}>
                    Monthly invoices <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                  </label>
                  <div style={{ position: 'relative' }}>
                    <select
                      id="dm-volume"
                      className="dm-select"
                      value={form.volume}
                      onChange={(e: ChangeEvent<HTMLSelectElement>) => setField('volume', e.target.value)}
                      disabled={submitting}
                      style={{ width: '100%', height: 42, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 8, padding: '0 32px 0 13px', fontSize: 14, color: 'var(--fg-1)', fontFamily: 'var(--font-sans)', cursor: 'pointer' }}
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
            </div>

            <button type="submit" disabled={submitting} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, marginTop: 18, cursor: 'pointer', gap: 9 }}>
              {submitting ? (
                <>
                  <span style={{ width: 15, height: 15, border: '2px solid rgba(255,255,255,0.4)', borderTopColor: '#fff', borderRadius: 99, animation: 'dmSpin 0.7s linear infinite' }} />
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
          <div style={{ padding: '32px 22px 24px', textAlign: 'center' }}>
            <span style={{ width: 48, height: 48, borderRadius: 99, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'inline-grid', placeItems: 'center', marginBottom: 16 }}>
              <Glyph d="M20 6 9 17l-5-5" size={26} sw={2} />
            </span>
            <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 8px' }}>You're booked</h3>
            <p style={{ fontSize: 13, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 auto 20px', maxWidth: 330 }}>
              Thanks, {firstNameOf(form.name)}. A compliance specialist will email {form.email} within one business day to lock your 20-minute slot.
            </p>
            <button id="dm-success-done" onClick={onClose} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, cursor: 'pointer' }}>Done</button>
          </div>
        )}

        {demoStep === 'error' && (
          <div style={{ padding: '32px 22px 24px', textAlign: 'center' }}>
            <span style={{ width: 48, height: 48, borderRadius: 99, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', display: 'inline-grid', placeItems: 'center', marginBottom: 16 }}>
              <Glyph d={WARN_PATHS} size={26} sw={1.8} />
            </span>
            <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 8px' }}>Something went wrong</h3>
            <p style={{ fontSize: 13, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 auto 20px', maxWidth: 330 }}>We couldn't book your demo just now. Please try again — your details are still here.</p>
            <button id="dm-error-retry" onClick={retry} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, cursor: 'pointer' }}>Try again</button>
          </div>
        )}
      </div>
    </div>
  )
}
