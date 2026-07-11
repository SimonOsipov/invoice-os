// Presentational loading indicator (M3-06-03): a small CSS spinner + optional label.
// Styled via @invoice-os/design-tokens vars (--line-2/--accent/--fg-3/--font-sans),
// mirroring the app's SignIn.tsx spinner — a locally scoped keyframe via an inline
// <style> tag so the package needs no separate CSS import. Presentational only: no
// data fetching, no wiring to a live surface (that's M3-08/09).
import type * as React from 'react'

export function Loading(props: { label?: string }): React.JSX.Element {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 10,
        padding: 'var(--space-8)',
        fontFamily: 'var(--font-sans)',
      }}
    >
      <style>{`
        @keyframes apicLoadingSpin { to { transform: rotate(360deg); } }
        .apic-loading-spin { animation: apicLoadingSpin 0.7s linear infinite; }
      `}</style>
      <span
        className="apic-loading-spin"
        style={{
          width: 22,
          height: 22,
          border: '2px solid var(--line-2)',
          borderTopColor: 'var(--accent)',
          borderRadius: 99,
          display: 'inline-block',
        }}
      />
      {props.label ? <span style={{ fontSize: 13, color: 'var(--fg-3)' }}>{props.label}</span> : null}
    </div>
  )
}
