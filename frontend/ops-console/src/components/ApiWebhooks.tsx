// API & webhooks — the client-facing integration screen (prototype lines 354-447).
// Mock UI only: no key generation, no persistence, no network. The real API-backed
// console is M7.

import {
  API_KEYS,
  COPY_ICON,
  DELIVERIES,
  EYE_ICON,
  EYE_OFF_ICON,
  LINK_ICON,
  METHOD_BG,
  METHOD_FG,
  PLUS_ICON,
  RATE_LIMIT,
  REDRIVE_ICON,
  REQ_LOG,
  WEBHOOKS,
} from '../data'
import { httpCodeColor } from '../charts'
import type { Env } from '../types'

type Props = {
  env: Env
  reveal: Record<string, boolean>
  onToggleReveal: (id: string) => void
  onCopyKey: (name: string) => void
  onRotate: (tag: string) => void
  onAddWebhook: () => void
}

// NOTE ON THE TWO GRID CLASSNAMES BELOW — they are deliberately "wrong" and must not be
// "corrected". `.ops-keys-table` and `.ops-webhook-table` are a binding cross-subtask
// contract (M4-20-08's full-surface audit greps for both names verbatim), but the names
// describe the wrong elements: API keys are cards inside `.ops-api-grid`, not a table,
// and webhook endpoints are a flex card stack with no grid at all. The column values
// each name carries match the *deliveries* table and the *request log* respectively, so
// the classnames are applied there. Renaming would break the audit for a cosmetic gain.

// Shared between the header row and every body row so the two grids cannot drift apart
// (proto:413/415). minWidth guards the ~320px phone case only: both panels sit in an
// `.ops-api-grid` half (~576px at 1440) and go full-width below the @1024 reflow.
const DELIVERY_GRID = '1fr 56px 74px 66px'
// proto:435. Note this one has `gap: 8px` and the deliveries table has none — the
// prototype does not unify them.
const REQLOG_GRID = '48px 1fr 44px 60px'
const TABLE_MIN_WIDTH = 380

export function ApiWebhooks({ env, reveal, onToggleReveal, onCopyKey, onRotate, onAddWebhook }: Props) {
  // proto:833/1089 — the rate meter is the only env-aware element on this screen.
  const rate = RATE_LIMIT[env]
  const envWord = env === 'sandbox' ? 'SANDBOX' : 'LIVE'

  return (
    <div className="ops-screen-pad">
      <div style={{ marginBottom: 20 }}>
        <div className="label" style={{ marginBottom: 8 }}>
          / 04 — INTEGRATION
        </div>
        <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>API &amp; webhooks</h1>
      </div>

      {/* API keys (proto:362-385) */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
        <span style={{ fontSize: 15, fontWeight: 600 }}>API keys</span>
        <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
          SECRET KEYS · SERVER-SIDE ONLY
        </span>
      </div>
      <div className="ops-api-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 28 }}>
        {API_KEYS.map((k) => {
          const revealed = !!reveal[k.id]
          return (
            <div key={k.id} style={{ border: '1px solid ' + k.borderColor, background: 'var(--bg-2)', borderRadius: 10, padding: '16px 18px' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
                <span
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 5,
                    background: k.tagBg,
                    border: '1px solid ' + k.tagBorder,
                    borderRadius: 5,
                    padding: '2px 8px',
                  }}
                >
                  <span style={{ width: 6, height: 6, borderRadius: 99, background: k.tagText }} />
                  <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: k.tagText, letterSpacing: '0.05em' }}>
                    {k.tag}
                  </span>
                </span>
                <span style={{ fontSize: 13, fontWeight: 600 }}>{k.name}</span>
              </div>

              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  background: 'var(--bg-1)',
                  border: '1px solid var(--line-1)',
                  borderRadius: 6,
                  padding: '9px 12px',
                  marginBottom: 12,
                }}
              >
                <span className="mono" style={{ flex: 1, fontSize: 12, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {revealed ? k.full : k.mask}
                </span>
                <button
                  type="button"
                  onClick={() => onToggleReveal(k.id)}
                  className="ops-btn"
                  style={{ border: 0, background: 'transparent', cursor: 'pointer', color: 'var(--fg-3)', display: 'inline-flex', padding: 0 }}
                >
                  {revealed ? EYE_OFF_ICON : EYE_ICON}
                </button>
                {/* proto:998 is `toast(...)` only — the prototype never touches the
                    Clipboard API, and calling it here would reject outside a secure
                    context and surface as a console.error in the smoke run. */}
                <button
                  type="button"
                  onClick={() => onCopyKey(k.name)}
                  className="ops-btn"
                  style={{ border: 0, background: 'transparent', cursor: 'pointer', color: 'var(--fg-3)', display: 'inline-flex', padding: 0 }}
                >
                  {COPY_ICON}
                </button>
              </div>

              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)' }}>
                  Created {k.created} · last used {k.lastUsed}
                </span>
                <button
                  type="button"
                  onClick={() => onRotate(k.tag)}
                  className="ops-btn"
                  style={{
                    flex: 'none',
                    border: '1px solid var(--line-2)',
                    background: 'var(--bg-2)',
                    cursor: 'pointer',
                    height: 28,
                    padding: '0 10px',
                    borderRadius: 5,
                    fontFamily: 'var(--font-sans)',
                    fontSize: 11.5,
                    fontWeight: 600,
                    color: 'var(--fg-1)',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                  }}
                >
                  {REDRIVE_ICON} Rotate
                </button>
              </div>
            </div>
          )
        })}
      </div>

      {/* Webhook endpoints (proto:387-407) — a flex card stack, deliberately carrying no
          grid classname. */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
        <span style={{ fontSize: 15, fontWeight: 600 }}>Webhook endpoints</span>
        <button type="button" onClick={onAddWebhook} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 32 }}>
          {PLUS_ICON} Add endpoint
        </button>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 22 }}>
        {WEBHOOKS.map((w) => (
          <div key={w.url} style={{ border: '1px solid var(--line-1)', background: 'var(--bg-2)', borderRadius: 10, padding: '15px 18px' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
              <span style={{ color: 'var(--accent)', display: 'inline-flex', flex: 'none' }}>{LINK_ICON}</span>
              <span
                className="mono"
                style={{ flex: 1, fontSize: 12.5, fontWeight: 600, color: 'var(--fg-1)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
              >
                {w.url}
              </span>
              <span
                style={{
                  flex: 'none',
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 5,
                  background: w.envBg,
                  border: '1px solid ' + w.envBorder,
                  borderRadius: 5,
                  padding: '2px 7px',
                }}
              >
                <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: w.envText }}>
                  {w.env}
                </span>
              </span>
              {/* Every endpoint in the prototype is active, so the pill is markup, not a
                  seed field (proto:399). */}
              <span
                style={{
                  flex: 'none',
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 5,
                  background: 'var(--status-green-bg)',
                  border: '1px solid var(--status-green-border)',
                  borderRadius: 999,
                  padding: '2px 9px',
                }}
              >
                <span style={{ width: 6, height: 6, borderRadius: 99, background: 'var(--status-green-text)' }} />
                <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: 'var(--status-green-text)' }}>
                  ACTIVE
                </span>
              </span>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 7, flexWrap: 'wrap' }}>
              <span className="label" style={{ marginRight: 4 }}>
                Events
              </span>
              {/* React only needs sibling uniqueness and event names are unique within one
                  endpoint, but `invoice.cleared`/`invoice.rejected` appear in both — the
                  composite key keeps that guarantee local and obvious. */}
              {w.events.map((ev) => (
                <span
                  key={w.url + ':' + ev}
                  className="mono"
                  style={{ fontSize: 10.5, color: 'var(--fg-2)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 4, padding: '3px 8px' }}
                >
                  {ev}
                </span>
              ))}
            </div>
          </div>
        ))}
      </div>

      {/* deliveries + rate limit + request log (proto:409-444) */}
      <div className="ops-api-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', overflow: 'hidden' }}>
          <div
            style={{
              padding: '12px 16px',
              borderBottom: '1px solid var(--line-1)',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
            }}
          >
            <span style={{ fontSize: 13.5, fontWeight: 600 }}>Recent deliveries</span>
            <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)' }}>
              LAST 24H
            </span>
          </div>
          {/* The horizontal scroll lives on this inner wrapper, not the bordered panel, so
              the title bar above stays put when the grid scrolls. */}
          <div style={{ overflowX: 'auto' }}>
            <div
              className="ops-keys-table"
              style={{
                display: 'grid',
                gridTemplateColumns: DELIVERY_GRID,
                padding: '8px 16px',
                background: 'var(--bg-1)',
                borderBottom: '1px solid var(--line-1)',
                minWidth: TABLE_MIN_WIDTH,
              }}
            >
              <span className="label">Event</span>
              <span className="label">Code</span>
              <span className="label">Latency</span>
              <span className="label">Retry</span>
            </div>
            {DELIVERIES.map((d) => (
              <div
                key={d.id}
                className="ops-keys-table"
                style={{
                  display: 'grid',
                  gridTemplateColumns: DELIVERY_GRID,
                  padding: '10px 16px',
                  borderBottom: '1px solid var(--line-1)',
                  alignItems: 'center',
                  minWidth: TABLE_MIN_WIDTH,
                }}
              >
                <span className="mono" style={{ fontSize: 11.5, color: 'var(--fg-1)' }}>
                  {d.event}
                </span>
                <span className="mono" style={{ fontSize: 11, fontWeight: 700, color: httpCodeColor(d.code) }}>
                  {d.code}
                </span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                  {d.latency}
                </span>
                <span className="mono" style={{ fontSize: 11, color: d.retry === '—' ? 'var(--fg-4)' : 'var(--status-amber-text)' }}>
                  {d.retry}
                </span>
              </div>
            ))}
          </div>
        </div>

        {/* minWidth:0 is load-bearing: this is an `.ops-api-grid` item with no overflow
            of its own, so its default `min-width: auto` would let the request log's
            minWidth blow out the 1fr track instead of scrolling inside it. */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16, minWidth: 0 }}>
          {/* rate limit (proto:424-430) */}
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', padding: '16px 18px' }}>
            <div className="label" style={{ marginBottom: 12 }}>
              Rate limit · {envWord}
            </div>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, marginBottom: 10 }}>
              <span className="mono" style={{ fontSize: 26, fontWeight: 700, letterSpacing: '-0.02em' }}>
                {rate.current}
              </span>
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>
                / {rate.limit} req·s
              </span>
            </div>
            <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 3, overflow: 'hidden' }}>
              <div style={{ width: rate.width, height: '100%', background: rate.color, borderRadius: 3 }} />
            </div>
            <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', marginTop: 8 }}>
              {rate.detail}
            </div>
          </div>

          {/* request log (proto:431-442) — title bar only, no column header row. */}
          <div style={{ border: '1px solid var(--line-1)', borderRadius: 9, background: 'var(--bg-2)', overflow: 'hidden', flex: 1 }}>
            <div
              style={{
                padding: '12px 16px',
                borderBottom: '1px solid var(--line-1)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
              }}
            >
              <span style={{ fontSize: 13.5, fontWeight: 600 }}>Recent API requests</span>
            </div>
            <div style={{ overflowX: 'auto' }}>
              {REQ_LOG.map((r) => (
                <div
                  key={r.id}
                  className="ops-webhook-table"
                  style={{
                    display: 'grid',
                    gridTemplateColumns: REQLOG_GRID,
                    gap: 8,
                    padding: '9px 16px',
                    borderBottom: '1px solid var(--line-1)',
                    alignItems: 'center',
                    minWidth: TABLE_MIN_WIDTH,
                  }}
                >
                  <span
                    className="mono"
                    style={{ fontSize: 10, fontWeight: 700, color: METHOD_FG[r.m], background: METHOD_BG[r.m], borderRadius: 4, padding: '2px 5px', textAlign: 'center' }}
                  >
                    {r.m}
                  </span>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {r.ep}
                  </span>
                  <span className="mono" style={{ fontSize: 11, fontWeight: 700, color: httpCodeColor(r.code) }}>
                    {r.code}
                  </span>
                  <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', textAlign: 'right' }}>
                    {r.lat}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
