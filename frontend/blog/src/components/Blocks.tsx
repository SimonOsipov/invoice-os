import type { Block } from '../data'
import { Icon } from '../icons'
import { noteKindOf } from '../model'
import { DemoBlock } from './DemoBlock'
import { Linked } from '@invoice-os/api-client/linked'

// An unknown block type renders nothing.
export function Blocks({ blocks }: { blocks: Block[] }) {
  let heading = 0
  return (
    <>
      {blocks.map((b, k) => {
        const id = b.type === 'h' ? heading++ : 0
        return (
          <div key={k} style={{ display: 'flex', flexDirection: 'column' }}>
            {b.type === 'p' && (
              <p className="t-body" style={{ margin: 0, fontSize: 17, color: 'var(--foreground)', textWrap: 'pretty' }}>
                <Linked text={b.text} />
              </p>
            )}
            {b.type === 'h' && (
              <h2
                id={`sec-${id}`}
                style={{
                  margin: '18px 0 0',
                  fontSize: 28,
                  lineHeight: 1.25,
                  letterSpacing: '-0.04em',
                  fontWeight: 700,
                  color: 'var(--ink)',
                }}
              >
                {b.text}
              </h2>
            )}
            {b.type === 'ul' && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                {b.items.map((it, i) => (
                  <div key={i} style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
                    <span style={{ color: 'var(--teal)', display: 'inline-flex', marginTop: 4 }}>
                      <Icon name="circle-check" size={18} />
                    </span>
                    <span className="t-body" style={{ fontSize: 17, color: 'var(--foreground)' }}><Linked text={it} /></span>
                  </div>
                ))}
              </div>
            )}
            {b.type === 'note' && <Note kind={b.kind} title={b.title} text={b.text} />}
            {b.type === 'demo' && <DemoBlock name={b.demo} />}
            {b.type === 'quote' && (
              <figure
                style={{
                  margin: '8px 0',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 14,
                  padding: '28px 0',
                  borderTop: '1px solid var(--border)',
                  borderBottom: '1px solid var(--border)',
                }}
              >
                <blockquote
                  style={{
                    margin: 0,
                    fontSize: 24,
                    fontWeight: 600,
                    letterSpacing: '-0.03em',
                    lineHeight: 1.4,
                    color: 'var(--ink)',
                    textWrap: 'pretty',
                  }}
                >
                  “<Linked text={b.text} />”
                </blockquote>
                <figcaption className="t-step">{b.who}</figcaption>
              </figure>
            )}
          </div>
        )
      })}
    </>
  )
}

function Note({ kind, title, text }: { kind: string; title: string; text: string }) {
  const n = noteKindOf(kind)
  return (
    <div
      style={{
        display: 'flex',
        gap: 16,
        alignItems: 'flex-start',
        padding: '22px 24px',
        background: n.bg,
        border: `1px solid ${n.bd}`,
        borderRadius: 6,
      }}
    >
      <span
        style={{
          flex: 'none',
          width: 36,
          height: 36,
          borderRadius: 6,
          display: 'grid',
          placeItems: 'center',
          background: n.tileBg,
          color: 'var(--primary)',
        }}
      >
        <Icon name={n.icon} size={18} />
      </span>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, minWidth: 0 }}>
        <span className="t-step" style={{ color: n.labelColor }}>{n.label}</span>
        <span className="blog-wrap" style={{ fontSize: 17, fontWeight: 700, letterSpacing: '-0.01em', lineHeight: 1.35 }}>{title}</span>
        <span className="t-body-sm" style={{ color: 'var(--foreground)' }}><Linked text={text} /></span>
      </div>
    </div>
  )
}
