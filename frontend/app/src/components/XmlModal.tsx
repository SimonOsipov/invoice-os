// UBL / XML modal — renders the generated UBL 2.1 document for the current detail
// invoice. Ported from Platform.dc.html ~L957-976. Backdrop click closes; inner click
// is stopped (mirrors the prototype's `stop` handler).

import { defaultDraft } from '../lib/clients'
import { ublXml } from '../lib/xml'
import { closeGlyph, docGlyph2, downloadGlyph } from '../glyphs'
import type { Invoice, PlatformCtx } from '../types'

export function XmlModal({ ctx }: { ctx: PlatformCtx }) {
  const { active, selectedId } = ctx
  const invList = active.invoices
  const fallback = defaultDraft(active)
  const detailInv: Invoice = invList.find((i) => i.number === selectedId) || invList[0] || { ...fallback, status: 'Draft', items: fallback.items }
  const xmlText = ublXml(detailInv)

  return (
    <div onClick={ctx.closeXml} style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'rgba(20,23,26,0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}>
      <div onClick={(e) => e.stopPropagation()} style={{ width: 760, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', boxShadow: '0 24px 60px -20px rgba(20,23,26,0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ color: 'var(--accent)', display: 'inline-flex' }}>{docGlyph2}</span>
            <div>
              <div style={{ fontSize: 15, fontWeight: 600 }}>UBL 2.1 document</div>
              <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                PEPPOL BIS 3.0 · {detailInv.number}
              </div>
            </div>
          </div>
          <div style={{ display: 'flex', gap: 9 }}>
            <button className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, fontSize: 13 }}>
              <span style={{ display: 'inline-flex' }}>{downloadGlyph}</span> Download .xml
            </button>
            <button onClick={ctx.closeXml} className="pf-btn" style={{ width: 34, height: 34, borderRadius: 'var(--radius-lg)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}>
              {closeGlyph}
            </button>
          </div>
        </div>
        <div style={{ flex: 1, overflow: 'auto', background: 'var(--bg-1)', padding: '18px 20px' }}>
          <pre className="mono" style={{ margin: 0, fontSize: 12, lineHeight: 1.6, color: 'var(--fg-1)', whiteSpace: 'pre', tabSize: 2 }}>
            {xmlText}
          </pre>
        </div>
      </div>
    </div>
  )
}
