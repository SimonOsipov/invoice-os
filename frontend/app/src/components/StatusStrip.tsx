// The five-node state strip: a pure renderer over stripNodes()' output. The explicit
// ReactNode return type is load-bearing -- TS infers `void`, not `never`, for a declared
// function whose body only throws, and JSX then rejects the component.

import { Fragment, type ReactNode } from 'react'

import { crossGlyph, tickGlyph11 } from '../glyphs'
import type { StripNode, StripState } from '../lib/invoiceStrip'

// The prototype's strip map (Platform.dc.html); labels use --fg-3 where it draws --fg-4.
const TONE: Record<StripState, { bg: string; border: string; fg: string; label: string }> = {
  done: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', fg: 'var(--status-green-text)', label: 'var(--fg-1)' },
  failed: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', fg: 'var(--status-red-text)', label: 'var(--fg-1)' },
  current: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-text)', fg: 'var(--status-amber-text)', label: 'var(--status-amber-text)' },
  unreached: { bg: 'var(--bg-2)', border: 'var(--line-3)', fg: 'var(--fg-4)', label: 'var(--fg-3)' },
  'not-required': { bg: 'var(--bg-3)', border: 'var(--line-2)', fg: 'var(--fg-4)', label: 'var(--fg-3)' },
}

export function StatusStrip({ nodes }: { nodes: StripNode[] }): ReactNode {
  return (
    // The pf-scroll-x recipe (platform.css): the strip is DESIGNED to overflow, and a scroll
    // region a keyboard user cannot reach hides the far node.
    <div
      data-testid="status-strip"
      className="pf-scroll-x"
      tabIndex={0}
      role="group"
      aria-label="Invoice state"
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        overflowX: 'auto',
        marginBottom: 16,
        background: 'var(--bg-2)',
        border: '1px solid var(--line-1)',
        borderRadius: 'var(--radius-md)',
        padding: '13px 20px',
      }}
    >
      {nodes.map((n, i) => (
        <Fragment key={n.key}>
          {i > 0 && (
            <span aria-hidden="true" style={{ flex: 1, minWidth: 10, height: 1, marginTop: 10, background: 'var(--line-2)' }} />
          )}
          <div
            data-testid="strip-node"
            data-key={n.key}
            data-state={n.state}
            style={{ flex: 'none', minWidth: 'max-content', display: 'flex', alignItems: 'flex-start', gap: 9, padding: '0 10px' }}
          >
            <span
              aria-hidden="true"
              style={{
                flex: 'none',
                width: 19,
                height: 19,
                marginTop: 1,
                borderRadius: '50%',
                display: 'grid',
                placeItems: 'center',
                background: TONE[n.state].bg,
                border: `1px solid ${TONE[n.state].border}`,
                color: TONE[n.state].fg,
              }}
            >
              {n.state === 'done' ? (
                tickGlyph11
              ) : n.state === 'failed' ? (
                crossGlyph
              ) : (
                <span style={{ width: 5, height: 5, borderRadius: '50%', background: 'currentColor' }} />
              )}
            </span>
            <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <span style={{ fontSize: 12.5, fontWeight: 600, whiteSpace: 'nowrap', color: TONE[n.state].label }}>{n.label}</span>
              {/* nowrap is the inverse of the retired card's overflowWrap:'anywhere': the strip
                  never wraps and never ellipsises, the container scrolls instead
                  (invoice-surfaces.spec.ts "no strip caption is ellipsised"). */}
              <span
                data-testid="strip-actor"
                className={n.actor?.mono ? 'mono' : undefined}
                style={{ fontSize: 11, whiteSpace: 'nowrap', color: 'var(--fg-3)' }}
              >
                {n.caption}
              </span>
            </span>
          </div>
        </Fragment>
      ))}
    </div>
  )
}
