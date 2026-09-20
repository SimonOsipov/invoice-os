// The Book-a-Demo lead-capture popup (task-117 / M4-19). Shell only — the form
// itself is DemoLeadForm (task-1108); this owns the overlay, Escape, Tab-trap, and
// focus restore, cloned VERBATIM from SignInModal.tsx's shell.

import { useEffect } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import { BrandMark } from '../icons'
import { DemoLeadForm, DEMO_FORM_CSS, Glyph } from './DemoLeadForm'
import type { DemoLead } from '../hubspot'

// A focusable element is eligible for the Tab-trap if it isn't disabled, is
// actually rendered (offsetParent is null for display:none / detached nodes), and
// is reachable by Tab at all. The tabIndex clause is load-bearing, not tidying:
// the honeypot below is an absolutely-positioned off-screen <input>, which the
// trap's `input,select,…` selector matches unconditionally and which keeps a
// NON-null offsetParent — so without this it would enter the trap list, possibly
// as its first/last node, and the Tab-wrap would try to focus an element the
// browser refuses, letting focus escape the modal.
export function isFocusable(el: HTMLElement): boolean {
  return !(el as HTMLButtonElement).disabled && el.offsetParent !== null && el.tabIndex >= 0
}

export function DemoModal({ onClose, submit }: { onClose: () => void; submit?: (lead: DemoLead) => Promise<void> }) {
  // Close on Escape (never a native dialog); restore focus to whatever opened the
  // modal so keyboard users land back where they were, on unmount.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      opener?.focus?.()
    }
  }, [onClose])

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

  return (
    <div
      className="asc-app dm-overlay"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Book a demo"
      style={{ position: 'fixed', inset: 0, zIndex: 200, background: 'oklch(16% .03 210 / .44)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24, animation: 'dmOvIn 160ms ease-out' }}
    >
      <style>{`
        @keyframes dmOvIn { from { opacity: 0; } to { opacity: 1; } }
        @keyframes dmCardIn { from { opacity: 0; transform: translateY(10px) scale(0.985); } to { opacity: 1; transform: none; } }
        ${DEMO_FORM_CSS}
        .si-close { transition: background 120ms ease-out, color 120ms ease-out; }
        .si-close:hover { background: var(--bg-3); color: var(--fg-1); }
        @media (max-width: 480px) { .dm-overlay { padding: 14px !important; } }
      `}</style>

      <div
        onClick={(e) => e.stopPropagation()}
        onKeyDown={trapTab}
        style={{ width: '100%', maxWidth: 452, maxHeight: 'calc(100dvh - 48px)', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', boxShadow: '0 32px 64px -24px oklch(16% .03 210 / .42)', overflowY: 'auto', animation: 'dmCardIn 200ms var(--ease-out)' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
            <BrandMark size={19} />
            <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>ASComply</span>
            <span className="mono" style={{ fontSize: 9, fontWeight: 500, letterSpacing: '0.08em', color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-sm)', padding: '1px 4px' }}>
              AFRICA
            </span>
          </div>
          <button onClick={onClose} className="si-close" aria-label="Close" style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-md)', border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
            <Glyph d="M18 6 6 18M6 6l12 12" size={17} sw={1.8} />
          </button>
        </div>

        <DemoLeadForm
          idPrefix="dm"
          variant="modal"
          heading={
            <>
              <div className="eyebrow" style={{ marginBottom: 8 }}>BOOK A DEMO</div>
              <h3 style={{ fontSize: 20, letterSpacing: '-0.02em', fontWeight: 600, margin: '0 0 6px' }}>See your invoices pass compliance in real time.</h3>
              <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', margin: '0 0 18px' }}>A 20-minute walkthrough with a compliance specialist. Bring a sample invoice file — we'll validate it live.</p>
            </>
          }
          onDone={onClose}
          submit={submit}
        />
      </div>
    </div>
  )
}
