import { useCallback, useEffect, useRef, useState } from 'react'

import { CHECKING, PROBLEMS } from '../data'
import { GLYPHS, Icon } from '../icons'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { IconTile } from './ds/IconTile'
import { Section } from './ds/Section'

const STEP_MS = 520
const TOTAL = PROBLEMS.length
const FAILS = PROBLEMS.filter((p) => p.tag === 'FAIL').length
const DONE_TEXT = `${FAILS} errors · ${TOTAL - FAILS} warnings · Not ready to submit`

// Read at each start, so a mid-session preference change applies.
const reducedMotion = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true
const hasObserver = () => typeof IntersectionObserver === 'function'

export function Problem() {
  // No observer or reduced motion: the first render is the end state, with no empty frame.
  const [n, setN] = useState(() => (hasObserver() && !reducedMotion() ? 0 : TOTAL))
  const [run, setRun] = useState(0)
  const cardRef = useRef<HTMLDivElement>(null)

  const start = useCallback(() => {
    if (reducedMotion()) {
      setN(TOTAL)
      return
    }
    setN(0)
    setRun((r) => r + 1)
  }, [])

  const running = run > 0 && n < TOTAL
  useEffect(() => {
    if (!running) return
    const id = setInterval(() => setN((k) => Math.min(k + 1, TOTAL)), STEP_MS)
    return () => clearInterval(id)
  }, [run, running])

  useEffect(() => {
    const card = cardRef.current
    if (!card || !hasObserver() || reducedMotion()) return
    let inside = false
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting && !inside) start()
          inside = e.isIntersecting
        }
      },
      { rootMargin: '-20% 0px -30% 0px' },
    )
    io.observe(card)
    return () => io.disconnect()
  }, [start])

  const done = n >= TOTAL

  return (
    <Section id="problem">
      <div
        className="split"
        style={{
          gridTemplateColumns: 'minmax(0, 0.92fr) minmax(0, 1.08fr)',
          gap: 'clamp(32px, 5vw, 64px)',
          alignItems: 'center',
        }}
      >
        <div style={{ display: 'grid', gap: 20, justifyItems: 'start' }}>
          <Eyebrow>THE PROBLEM</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0 }}>
            The invoice is becoming a compliance checkpoint.
            <br /> <span style={{ color: 'var(--teal)' }}>Is your business ready?</span>
          </h2>
          <p className="t-body" style={{ margin: 0, maxWidth: 480 }}>
            Nigeria's e-invoicing transition means businesses will need more than PDF invoices and manual approval
            chains.
          </p>
          <p className="t-body" style={{ margin: 0, maxWidth: 480 }}>
            Many companies still manage invoices across accounting software, Excel files, emails, ERP systems and manual
            approvals. This creates errors, delays and audit risk.
          </p>
        </div>

        <div
          id="problem-check"
          ref={cardRef}
          className="card"
          style={{ boxShadow: 'var(--shadow-card)', padding: '22px 24px', display: 'grid', gap: 0 }}
        >
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 12,
              paddingBottom: 16,
              borderBottom: '1px solid var(--border)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              <IconTile name="file-search" tone="primary" size={36} />
              <span className="t-card-title">Invoice check</span>
            </div>
            <span className="t-meta">Sample data</span>
          </div>

          {PROBLEMS.map((p, i) => {
            const s = i < n ? p : CHECKING
            return (
              <div
                key={p.label}
                data-check="row"
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: 16,
                  padding: '12px 0',
                  borderBottom: '1px solid var(--border)',
                  minHeight: 56,
                }}
              >
                <span
                  style={{
                    fontSize: 14,
                    lineHeight: 1.45,
                    color: 'var(--ink)',
                    textWrap: 'pretty',
                    opacity: i <= n ? 1 : 0.55,
                    transition: 'opacity 220ms var(--ease-out)',
                  }}
                >
                  {p.label}
                </span>
                <span
                  style={{
                    flex: 'none',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    height: 26,
                    padding: '0 10px',
                    borderRadius: 'var(--radius-sm)',
                    fontSize: 10,
                    fontWeight: 700,
                    letterSpacing: '0.08em',
                    background: s.bg,
                    color: s.fg,
                    transition: 'background 220ms var(--ease-out), color 220ms var(--ease-out)',
                  }}
                >
                  <Icon paths={GLYPHS[s.icon]} size={13} strokeWidth={2} />
                  {s.tag}
                </span>
              </div>
            )
          })}

          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: '12px 16px',
              flexWrap: 'wrap',
              paddingTop: 16,
            }}
          >
            <span
              data-check="footer"
              style={{ fontSize: 13, fontWeight: 700, color: done ? 'var(--destructive)' : 'var(--muted-foreground)' }}
            >
              {done ? DONE_TEXT : `Checking ${n + 1} of ${TOTAL}`}
            </span>
            <Button variant="text" onClick={start}>
              Run check again
            </Button>
          </div>
        </div>
      </div>
    </Section>
  )
}
