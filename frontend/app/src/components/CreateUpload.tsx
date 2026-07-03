// Create flow · step 1 — import a document (sample-file picker + upload/skip panel).
// Ported from Platform.dc.html ~L407-448.

import { SAMPLE_FILES } from '../data'
import { importGlyph, tickGlyph13 } from '../glyphs'
import type { PlatformCtx } from '../types'

export function CreateUpload({ ctx }: { ctx: PlatformCtx }) {
  const { active, uploadFile } = ctx
  const selFile = SAMPLE_FILES.find((f) => f.id === uploadFile) || null
  const hasFile = !!selFile

  return (
    <div className="pf-create-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 320px', gap: 16, alignItems: 'start' }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span style={{ fontSize: 15, fontWeight: 600 }}>Import a document · {active.short}</span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
            CSV · PDF · IMAGE
          </span>
        </div>
        <div style={{ padding: 20 }}>
          <div style={{ border: '1.5px dashed var(--line-3)', borderRadius: 10, padding: '30px 20px', display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center', background: 'var(--bg-1)', marginBottom: 22 }}>
            <span style={{ width: 48, height: 48, borderRadius: 10, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center', marginBottom: 14 }}>{importGlyph}</span>
            <div style={{ fontSize: 15, fontWeight: 600, marginBottom: 5 }}>Drag a file here, or pick a sample below</div>
            <p style={{ fontSize: 13, color: 'var(--fg-3)', margin: 0, maxWidth: 380, lineHeight: 1.55 }}>
              The parser extracts buyer details, line items and totals, then pre-fills the invoice for validation.
            </p>
          </div>
          <div className="label" style={{ marginBottom: 12 }}>
            Sample files
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {SAMPLE_FILES.map((f) => {
              const sel = uploadFile === f.id
              return (
                <button
                  key={f.id}
                  onClick={() => ctx.selectFile(f.id)}
                  className="pf-upcard"
                  style={{ display: 'flex', alignItems: 'center', gap: 13, padding: '12px 14px', border: `1px solid ${sel ? 'var(--accent)' : 'var(--line-2)'}`, background: sel ? 'var(--accent-tint)' : 'var(--bg-2)', borderRadius: 8, width: '100%' }}
                >
                  <span style={{ flex: 'none', width: 38, height: 38, borderRadius: 8, background: f.iconBg, color: f.iconColor, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700, letterSpacing: '0.02em' }}>{f.ext}</span>
                  <div style={{ flex: 1, textAlign: 'left' }}>
                    <div style={{ fontSize: 13.5, fontWeight: 500, color: 'var(--fg-1)' }}>{f.name}</div>
                    <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                      {f.meta}
                    </div>
                  </div>
                  <span style={{ flex: 'none', color: 'var(--accent)', display: 'inline-flex' }}>{sel ? tickGlyph13 : ''}</span>
                </button>
              )
            })}
          </div>
        </div>
      </div>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: 20, position: 'sticky', top: 0 }}>
        <div className="label" style={{ marginBottom: 14 }}>
          Selected file
        </div>
        {hasFile && selFile && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 11, padding: 12, border: '1px solid var(--line-1)', borderRadius: 8, background: 'var(--bg-1)', marginBottom: 18 }}>
            <span style={{ flex: 'none', width: 34, height: 34, borderRadius: 7, background: selFile.iconBg, color: selFile.iconColor, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 9, fontWeight: 700 }}>{selFile.ext}</span>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontSize: 13, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{selFile.name}</div>
              <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', marginTop: 2 }}>
                {selFile.meta}
              </div>
            </div>
          </div>
        )}
        {!hasFile && (
          <p style={{ fontSize: 12.5, color: 'var(--fg-3)', margin: '0 0 18px', lineHeight: 1.55 }}>Pick a file to parse, or skip and build the invoice manually.</p>
        )}
        <button
          onClick={ctx.parseFile}
          className="v2-btn v2-btn-primary pf-btn"
          style={{ width: '100%', justifyContent: 'center', height: 42, background: hasFile ? 'var(--accent)' : 'var(--bg-3)', color: hasFile ? '#fff' : 'var(--fg-4)', cursor: hasFile ? 'pointer' : 'not-allowed' }}
        >
          <span style={{ display: 'inline-flex' }}>{importGlyph}</span> Upload &amp; parse
        </button>
        <button onClick={ctx.skipUpload} className="v2-btn v2-btn-ghost pf-btn" style={{ width: '100%', justifyContent: 'center', height: 38, marginTop: 10 }}>
          Skip — enter manually
        </button>
        <p style={{ fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'center', margin: '12px 0 0', lineHeight: 1.5 }}>Parsed fields are editable before validation.</p>
      </div>
    </div>
  )
}
