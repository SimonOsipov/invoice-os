// The Book-a-Demo popup shell — overlay, header, Close, Escape, Tab-trap and
// focus restore. The form itself is DemoLeadForm.

import { useEffect } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import { DemoLeadForm, DEMO_FORM_CSS } from './DemoLeadForm'
import { Eyebrow } from './ds/Eyebrow'
import { MODAL_CHROME_CSS, MODAL_SCRIM_STYLE, ModalHeader, modalCardStyle } from './modalChrome'
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
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Book a demo"
      style={MODAL_SCRIM_STYLE}
    >
      <style>{`
        ${MODAL_CHROME_CSS}
        ${DEMO_FORM_CSS}
      `}</style>

      <div
        onClick={(e) => e.stopPropagation()}
        onKeyDown={trapTab}
        style={modalCardStyle(510)}
      >
        <ModalHeader onClose={onClose} padX={20} />

        <DemoLeadForm
          idPrefix="dm"
          heading={
            <>
              <div style={{ marginBottom: 14 }}><Eyebrow>BOOK A DEMO</Eyebrow></div>
              <h3 style={{ fontSize: 28, lineHeight: 1.2, letterSpacing: 'var(--tracking-h3)', fontWeight: 700, margin: '0 0 8px', color: 'var(--ink)' }}>Let's talk about your workflow.</h3>
              <p className="t-body-sm" style={{ margin: '0 0 20px', lineHeight: 1.6 }}>A 20-minute walkthrough with a compliance specialist. Bring a sample invoice file — we'll validate it live.</p>
            </>
          }
          onDone={onClose}
          submit={submit}
        />
      </div>
    </div>
  )
}
