// The five-node state strip: a pure renderer over stripNodes()' output. The explicit
// ReactNode return type is load-bearing -- TS infers `void`, not `never`, for a declared
// function whose body only throws, and JSX then rejects the component.

import { Fragment, type ReactNode } from 'react'

import { crossGlyph, tickGlyph11 } from '../glyphs'
import type { StripNode, StripState } from '../lib/invoiceStrip'

// Lifted verbatim from ApprovalStateCard's STATE_TONE -- no new hue.
const TONE: Record<StripState, { bg: string; border: string; text: string }> = {
  done: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  failed: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)' },
  current: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  unreached: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
  'not-required': { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
}

export function StatusStrip({ nodes }: { nodes: StripNode[] }): ReactNode {
  return (
    // ViolationsTable's pf-scroll-x recipe: the strip is DESIGNED to overflow, and a scroll
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
        marginBottom: 22,
        background: 'var(--bg-2)',
        border: '1px solid var(--line-1)',
        borderRadius: 'var(--radius-md)',
        padding: '14px 18px',
      }}
    >
      {nodes.map((n, i) => (
        <Fragment key={n.key}>
          {i > 0 && (
            <span aria-hidden="true" style={{ flex: 1, minWidth: 8, height: 1, marginTop: 9, background: 'var(--line-2)' }} />
          )}
          <div
            data-testid="strip-node"
            data-key={n.key}
            data-state={n.state}
            style={{ flex: 'none', minWidth: 'max-content', display: 'flex', alignItems: 'flex-start', gap: 8, padding: '0 10px' }}
          >
            <span
              aria-hidden="true"
              style={{
                flex: 'none',
                width: 18,
                height: 18,
                borderRadius: 99,
                display: 'grid',
                placeItems: 'center',
                background: TONE[n.state].bg,
                border: `1px solid ${TONE[n.state].border}`,
                color: TONE[n.state].text,
              }}
            >
              {n.state === 'done' ? (
                tickGlyph11
              ) : n.state === 'failed' ? (
                crossGlyph
              ) : (
                <span style={{ width: 8, height: 8, borderRadius: 99, background: 'currentColor' }} />
              )}
            </span>
            <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <span style={{ fontSize: 12.5, fontWeight: 600, whiteSpace: 'nowrap', color: TONE[n.state].text }}>{n.label}</span>
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
