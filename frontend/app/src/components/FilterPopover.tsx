// Shared popover shell for the five audit filter triggers (AUDIT-07). Anatomy: MoreMenu
// (MemberParts.tsx:534-618) -- wrapper ref covers trigger + panel, useDismiss(open,
// onDismiss, wrapRef), trigger toggles explicitly on click so its own button can close an
// open panel. Two departures: a trigger carrying chevDownGlyph (not icon-only),
// and arbitrary children instead of MenuAction[].

import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'

import { chevDownGlyph } from '../glyphs'
import { useDismiss } from '../lib/useDismiss'

// v2 card elevation; MemberParts keeps its own popover shadow.
const POPOVER_SHADOW = 'var(--shadow-card)'

export interface FilterPopoverProps {
  /** Prefixes every data-testid this component renders. */
  testId: string
  label: string
  summary?: ReactNode
  open: boolean
  onOpen: () => void
  onClose: () => void
  /** Mirrors AuditPager's busy treatment -- disabled while a request is in flight. */
  disabled?: boolean
  /** Form field: label above a full-width 40-tall trigger, full-width panel. */
  block?: boolean
  children: ReactNode
}

// `open` is the source of truth for cross-popover coordination, but the trigger also flips
// local state immediately on its own click -- filterPopover_triggerClickClosesAnOpenPanel
// pins that the panel is gone in the SAME click, not on the parent's next render.
export function FilterPopover({ testId, label, summary, open, onOpen, onClose, disabled, block, children }: FilterPopoverProps) {
  const [isOpen, setIsOpen] = useState(open)
  useEffect(() => setIsOpen(open), [open])

  // On the WRAPPER, not the panel -- with the ref on the panel alone, clicking the trigger
  // of an open popover would dismiss it on mousedown and re-open it on click.
  const wrapRef = useRef<HTMLDivElement>(null)
  const dismiss = useCallback(() => {
    setIsOpen(false)
    onClose()
  }, [onClose])
  useDismiss(isOpen, dismiss, wrapRef)

  const labelId = `${testId}-label`
  const summaryId = `${testId}-summary`
  const field = (
    <>
      <button
        type="button"
        data-testid={`${testId}-trigger`}
        aria-expanded={isOpen}
        aria-labelledby={block ? `${labelId} ${summaryId}` : undefined}
        disabled={disabled}
        onClick={(e) => {
          e.stopPropagation()
          if (isOpen) {
            setIsOpen(false)
            onClose()
          } else {
            setIsOpen(true)
            onOpen()
          }
        }}
        className="pf-btn"
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: block ? 10 : 8,
          height: block ? 40 : 34,
          padding: block ? '0 12px' : '0 11px',
          ...(block ? { width: '100%', textAlign: 'left' as const } : {}),
          border: '1px solid var(--line-2)',
          background: isOpen && !block ? 'var(--bg-3)' : 'var(--bg-2)',
          color: isOpen && !block ? 'var(--action)' : 'var(--fg-1)',
          fontFamily: 'var(--font-sans)',
          fontSize: block ? 13.5 : 13,
          fontWeight: block ? 400 : 500,
          cursor: disabled ? 'not-allowed' : 'pointer',
          ...(disabled ? { opacity: 0.45, filter: 'none' } : {}),
        }}
      >
        {!block && <span>{label}</span>}
        {summary && (
          <span
            id={summaryId}
            style={{
              color: 'var(--fg-3)',
              fontWeight: 400,
              ...(block ? { flex: 1, minWidth: 0, whiteSpace: 'nowrap' as const, overflow: 'hidden', textOverflow: 'ellipsis' } : {}),
            }}
          >
            {summary}
          </span>
        )}
        <span
          data-testid={`${testId}-chevron`}
          aria-hidden
          style={{ display: 'inline-flex', flex: 'none', color: 'var(--fg-3)', transform: isOpen ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 160ms' }}
        >
          {chevDownGlyph}
        </span>
      </button>
      {isOpen && (
        <div
          data-testid={`${testId}-panel`}
          onClick={(e) => e.stopPropagation()}
          style={{
            position: 'absolute',
            top: 'calc(100% + 6px)',
            left: 0,
            ...(block ? { right: 0 } : {}),
            zIndex: 60,
            minWidth: 240,
            background: 'var(--bg-2)',
            border: '1px solid var(--line-2)',
            borderRadius: 'var(--radius-md)',
            boxShadow: POPOVER_SHADOW,
            overflow: 'hidden',
            animation: 'popIn 140ms ease-out',
          }}
        >
          {children}
        </div>
      )}
    </>
  )

  if (!block) {
    return (
      <div ref={wrapRef} style={{ position: 'relative' }}>
        {field}
      </div>
    )
  }
  return (
    <div ref={wrapRef}>
      <div id={labelId} data-testid={labelId} className="label" style={{ marginBottom: 7 }}>
        {label}
      </div>
      <div style={{ position: 'relative' }}>{field}</div>
    </div>
  )
}
