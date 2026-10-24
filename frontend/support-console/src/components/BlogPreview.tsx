import type { CSSProperties } from 'react'
import type { EditorState, Preview, PreviewBlock } from '../blog'
import { G } from './BlogList'
import { Linked } from '@invoice-os/api-client/linked'

type Props = {
  model: Preview
  tab: EditorState['preview']
  cover: string
  onTab: (tab: EditorState['preview']) => void
}

const TABS: { key: EditorState['preview']; label: string }[] = [
  { key: 'article', label: 'Article' },
  { key: 'card', label: 'Card' },
]

const COVER_FILL: CSSProperties = { position: 'absolute', inset: 0 }

function CoverBox({ model, cover }: { model: Preview; cover: string }) {
  if (cover !== 'none')
    return <div role="img" aria-label="Cover image" style={{ ...COVER_FILL, width: '100%', height: '100%', backgroundImage: cover, backgroundSize: 'cover', backgroundPosition: 'center' }} />
  const { tone } = model
  return (
    <div style={{ ...COVER_FILL, background: tone.bg, color: tone.fg, display: 'grid', placeItems: 'center', overflow: 'hidden' }}>
      <div className="hero-grid" style={{ ...COVER_FILL, opacity: 0.45 }} />
      <span style={{ position: 'relative', width: 48, height: 48, borderRadius: 6, display: 'grid', placeItems: 'center', background: tone.tile }}>
        <G name={model.icon} size={22} />
      </span>
    </div>
  )
}

function Block({ b }: { b: PreviewBlock }) {
  switch (b.type) {
    case 'p':
      return (
        <p className="t-body" style={{ margin: 0, fontSize: 15.5, color: 'var(--foreground)', textWrap: 'pretty' }}>
          <Linked text={b.text} />
        </p>
      )
    case 'h':
      return <h2 style={{ margin: '12px 0 0', fontSize: 23, lineHeight: 1.25, letterSpacing: '-0.04em', fontWeight: 700, color: 'var(--ink)' }}>{b.text}</h2>
    case 'ul':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {b.items.map((it, k) => (
            <div key={k} style={{ display: 'flex', gap: 10, alignItems: 'flex-start' }}>
              <span style={{ color: 'var(--teal)', display: 'inline-flex', marginTop: 4 }}>
                <G name="circle-check" size={16} />
              </span>
              <span className="t-body" style={{ fontSize: 15.5, color: 'var(--foreground)' }}>
                <Linked text={it} />
              </span>
            </div>
          ))}
        </div>
      )
    case 'note':
      return (
        <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start', padding: '18px 20px', background: b.bg, border: `1px solid ${b.border}`, borderRadius: 6 }}>
          <span style={{ flex: 'none', width: 34, height: 34, borderRadius: 6, display: 'grid', placeItems: 'center', background: b.tile, color: 'var(--primary)' }}>
            <G name={b.icon} size={17} />
          </span>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
            <span className="t-step" style={{ color: b.labelColor }}>
              {b.label}
            </span>
            <span style={{ fontSize: 15.5, fontWeight: 700, letterSpacing: '-0.01em', lineHeight: 1.35 }}>{b.title}</span>
            <span className="t-body-sm" style={{ color: 'var(--foreground)' }}>
              <Linked text={b.text} />
            </span>
          </div>
        </div>
      )
    case 'quote':
      return (
        <figure style={{ margin: '4px 0', display: 'flex', flexDirection: 'column', gap: 12, padding: '22px 0', borderTop: '1px solid var(--border)', borderBottom: '1px solid var(--border)' }}>
          <blockquote style={{ margin: 0, fontSize: 20, fontWeight: 600, letterSpacing: '-0.03em', lineHeight: 1.4, color: 'var(--ink)' }}>“<Linked text={b.text} />”</blockquote>
          <figcaption className="t-step">{b.who}</figcaption>
        </figure>
      )
    case 'demo':
      return (
        <div style={{ borderRadius: 10, background: 'var(--surface)', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow-elegant)' }}>
          <span className="mono" style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.1em', color: 'var(--accent)' }}>
            FEATURE LIBRARY · DEMO
          </span>
          <div style={{ height: 120, borderRadius: 6, background: 'var(--card)', display: 'grid', placeItems: 'center' }}>
            <span style={{ width: 40, height: 40, borderRadius: '50%', background: 'var(--accent)', color: 'var(--accent-foreground)', display: 'grid', placeItems: 'center' }}>
              <G name="play" size={16} />
            </span>
          </div>
          <span style={{ fontSize: 14, fontWeight: 700, color: 'var(--surface-foreground)' }}>{b.title}</span>
        </div>
      )
  }
}

export function BlogPreview({ model, tab, cover, onTab }: Props) {
  const titleColor = model.titleMuted ? 'var(--muted-foreground)' : 'var(--ink)'
  return (
    <>
      <div
        style={{
          position: 'sticky',
          top: 0,
          zIndex: 2,
          flex: 'none',
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          padding: '10px 20px',
          background: 'var(--header-bg)',
          backdropFilter: 'blur(18px)',
          borderBottom: '1px solid var(--header-border)',
        }}
      >
        <span className="label">Preview</span>
        <div style={{ display: 'flex', padding: 3, gap: 2, background: 'var(--sage)', border: '1px solid var(--sage-card-border)', borderRadius: 6 }}>
          {TABS.map((t) => {
            const on = t.key === tab
            return (
              <button
                key={t.key}
                type="button"
                onClick={() => onTab(t.key)}
                style={{ height: 26, padding: '0 12px', border: 0, borderRadius: 4, cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 700, background: on ? 'var(--primary)' : 'transparent', color: on ? 'var(--primary-foreground)' : 'var(--tab-active-text)' }}
              >
                {t.label}
              </button>
            )
          })}
        </div>
        <span className="mono" style={{ marginLeft: 'auto', minWidth: 0, fontSize: 10.5, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {model.url}
        </span>
      </div>
      {tab === 'article' ? (
        <div style={{ padding: '32px 32px 64px', display: 'flex', flexDirection: 'column', gap: 24, color: 'var(--ink)' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5, fontWeight: 700, color: 'var(--link)' }}>
              <G name="chevron-left" size={14} />
              {model.catName}
            </span>
            <span className="mono" style={{ fontSize: 11.5, color: 'var(--muted-foreground)' }}>
              {model.meta}
            </span>
            <h1 style={{ margin: 0, fontSize: 36, lineHeight: 1.1, letterSpacing: '-0.05em', fontWeight: 700, color: titleColor, textWrap: 'pretty', overflowWrap: 'anywhere' }}>{model.title}</h1>
            <p className="t-lead" style={{ margin: 0, fontSize: 17, textWrap: 'pretty', overflowWrap: 'anywhere' }}>
              {model.dek}
            </p>
          </div>
          <div data-preview-cover style={{ aspectRatio: '21 / 9', position: 'relative', borderRadius: 8, overflow: 'hidden' }}>
            <CoverBox model={model} cover={cover} />
          </div>
          {model.hasToc && (
            <div data-preview-toc style={{ display: 'flex', flexDirection: 'column', gap: 8, paddingBottom: 4 }}>
              <div className="t-eyebrow">On this page</div>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 16px' }}>
                {model.toc.map((t) => (
                  <span key={t.num} data-preview-toc-entry style={{ display: 'inline-flex', gap: 8, alignItems: 'baseline', fontSize: 12.5, color: 'var(--text-copy)', minWidth: 0, overflowWrap: 'anywhere' }}>
                    <span className="mono" style={{ fontSize: 10, fontWeight: 700, color: 'var(--step-label)' }}>
                      {t.num}
                    </span>
                    {t.text}
                  </span>
                ))}
              </div>
            </div>
          )}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 18, maxWidth: 640, overflowWrap: 'anywhere' }}>
            {model.blocks.map((b, k) => (
              <div key={k} style={{ display: 'flex', flexDirection: 'column' }}>
                <Block b={b} />
              </div>
            ))}
            {model.emptyBody && (
              <p className="t-body" style={{ margin: 0, color: 'var(--muted-foreground)' }}>
                Content blocks you add appear here.
              </p>
            )}
          </div>
        </div>
      ) : (
        <div style={{ padding: 32, display: 'flex', flexDirection: 'column', gap: 14, color: 'var(--ink)' }}>
          <span style={{ fontSize: 12, color: 'var(--fg-3)' }}>As it appears in the {model.catName} band and category page.</span>
          <div data-preview-card style={{ maxWidth: 360, display: 'flex', flexDirection: 'column', background: 'var(--card)', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            <div style={{ aspectRatio: '16 / 9', position: 'relative' }}>
              <CoverBox model={model} cover={cover} />
            </div>
            <div style={{ padding: '22px 24px 24px', display: 'flex', flexDirection: 'column', gap: 10 }}>
              <span className="mono" style={{ fontSize: 11, color: 'var(--muted-foreground)' }}>
                {model.date}
              </span>
              <span style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.01em', lineHeight: 1.3, color: titleColor, overflowWrap: 'anywhere' }}>{model.title}</span>
              <span className="t-body-sm" style={{ overflowWrap: 'anywhere' }}>
                {model.dek}
              </span>
              <span style={{ paddingTop: 6, display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 13, fontWeight: 700, color: 'var(--link)' }}>
                Read the post <G name="arrow-right" size={14} />
              </span>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
