// Inline loading row: a 16px spinner and an optional label. The keyframe ships inline so
// the package needs no CSS import; `.apic-loading-spin` is the test hook.
import type * as React from 'react'

export function Loading(props: { label?: string }): React.JSX.Element {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        padding: '40px 0',
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
          width: 16,
          height: 16,
          border: '2px solid var(--line-2)',
          borderTopColor: 'var(--action)',
          borderRadius: '50%',
          display: 'inline-block',
        }}
      />
      {props.label ? <span style={{ fontSize: 13, color: 'var(--fg-3)' }}>{props.label}</span> : null}
    </div>
  )
}
