// The PDF and image canvases.
//
// Neither fetches and neither owns the object URL: the prop is a `blob:` string, so the
// shell stays the only place that can revoke it. That is the type, not a convention.
//
// No page rail, thumbnail badge or page jump. Nothing in a browser exposes the page count
// of a `blob:` PDF, the server records none, and reading the bytes to find one would be
// parsing. The platform viewer paginates instead — its own download/print chrome is the
// browser's, not an affordance this build offers.

import { useState, type CSSProperties } from 'react'

import { describeSourceRows } from '../lib/sourceDocument'

const IMAGE_W = 520
const ZOOMS = [0.5, 1, 1.5]

const FRAME: CSSProperties = { display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }

const TOOLBAR: CSSProperties = {
  flex: 'none',
  padding: '9px 14px',
  borderBottom: '1px solid var(--line-1)',
  background: 'var(--bg-2)',
  display: 'flex',
  alignItems: 'center',
  gap: 10,
}

/** design §6: without these the toolbar labels wrap at narrow widths. */
const CONTROL: CSSProperties = { whiteSpace: 'nowrap', flex: 'none' }

const NOTE: CSSProperties = { ...CONTROL, fontSize: 12.5, color: 'var(--fg-3)' }

const SEGMENT: CSSProperties = {
  ...CONTROL,
  height: 26,
  padding: '0 12px',
  // A plain button, never `.pf-btn`/`.pf-chip`: both force `border-radius` with `!important`.
  borderRadius: 999,
  border: '1px solid transparent',
  fontFamily: 'var(--font-sans)',
  fontSize: 12.5,
  fontWeight: 500,
  cursor: 'pointer',
}

export function SourceDocumentPdf({ url }: { url: string }) {
  return (
    <div data-testid="source-document-pdf" style={FRAME}>
      <div data-testid="pdf-toolbar" style={TOOLBAR}>
        <span style={NOTE}>{describeSourceRows(null, 'pdf')}</span>
        <span
          className="mono"
          style={{ ...CONTROL, marginLeft: 'auto', fontSize: 10.5, letterSpacing: '0.05em', color: 'var(--fg-3)' }}
        >
          RENDERED FROM THE STORED PDF · NO EDITS POSSIBLE
        </span>
      </div>
      <embed
        data-testid="pdf-embed"
        src={url}
        type="application/pdf"
        style={{ flex: 1, minHeight: 0, width: '100%', border: 0 }}
      />
    </div>
  )
}

export function SourceDocumentImage({ url, filename }: { url: string; filename: string | null }) {
  const [zoom, setZoom] = useState(1)

  return (
    <div data-testid="source-document-image" style={FRAME}>
      {/* No mono right-hand stamp: the design writes none for images, and the header's
          IMMUTABLE RECORD pill already carries immutability in every state. */}
      <div data-testid="image-toolbar" style={TOOLBAR}>
        <div
          style={{ ...CONTROL, display: 'flex', alignItems: 'center', gap: 2, padding: 2, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 999 }}
        >
          {ZOOMS.map((z) => (
            <button
              key={z}
              type="button"
              data-testid={`zoom-${z * 100}`}
              aria-pressed={zoom === z}
              onClick={() => setZoom(z)}
              style={{ ...SEGMENT, background: zoom === z ? 'var(--action)' : 'transparent', color: zoom === z ? 'var(--text-on-dark)' : 'var(--fg-2)' }}
            >
              {`${z * 100}%`}
            </button>
          ))}
        </div>
        <span style={NOTE}>{describeSourceRows(null, 'image')}</span>
      </div>

      {/* The design's literal, same idiom as the modal scrim's: no token is this dark. */}
      <div
        data-testid="image-ground"
        style={{ flex: 1, minHeight: 0, overflow: 'auto', display: 'flex', alignItems: 'flex-start', justifyContent: 'center', padding: 36, background: 'oklch(28% .015 210)' }}
      >
        {/* Zoom moves `width`, never `transform: scale()`: a scale leaves the layout box
            alone, so the ground would never scroll to reveal the enlarged photograph. */}
        <img
          data-testid="source-image"
          src={url}
          alt={filename ?? 'Source document'}
          style={{ display: 'block', width: IMAGE_W * zoom, maxWidth: 'none', transform: 'rotate(-1.1deg)', boxShadow: '0 18px 44px -14px oklch(20% .02 210 / 0.55)' }}
        />
      </div>
    </div>
  )
}
