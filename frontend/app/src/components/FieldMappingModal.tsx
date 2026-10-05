// Field-mapping editor for one connector, opened from the connector detail view's Edit
// button. Structurally mirrors EntityFormModal (fixed backdrop, stopPropagation'd panel,
// header + close, ghost/primary footer) — it edits a local draft of the mapping rows and
// only lifts them to the workspace (ctx.saveConnectorMapping) on Save, so Cancel and the
// backdrop both discard cleanly.

import { useState } from 'react'

import type { ConnectorDef } from '../data'
import { mappingFor } from '../lib/connectors'
import { arrowGlyph, closeGlyph } from '../glyphs'
import type { FieldMapRow, PlatformCtx } from '../types'

const MONO_INPUT: React.CSSProperties = { fontFamily: 'var(--font-mono)', fontSize: 12, height: 34, boxSizing: 'border-box', padding: '0 10px', background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)' }
const FOOT_BTN: React.CSSProperties = { height: 36, padding: '0 16px', borderRadius: 'var(--radius-btn)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500 }

export function FieldMappingModal({ ctx, def, onClose }: { ctx: PlatformCtx; def: ConnectorDef; onClose: () => void }) {
  const [rows, setRows] = useState<FieldMapRow[]>(() => mappingFor(def, ctx.connectorMappings))

  function updateRow(i: number, field: keyof FieldMapRow, value: string) {
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, [field]: value } : r)))
  }

  function save() {
    ctx.saveConnectorMapping(def.id, rows)
    onClose()
  }

  return (
    <div
      onClick={onClose}
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Edit field mapping"
        style={{ width: 640, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', boxShadow: 'var(--shadow-card)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
            <span style={{ color: 'var(--action)', display: 'inline-flex' }}>{arrowGlyph}</span>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 15, fontWeight: 700 }}>Edit field mapping</div>
              <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                ERP FIELD → NRS UBL PATH
              </div>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-btn)', border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 10 }}>
          {rows.map((r, i) => (
            <div key={i} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) auto minmax(0, 1fr)', gap: 10, alignItems: 'end' }}>
              <label style={{ minWidth: 0 }}>
                <div className="label" style={{ marginBottom: 5 }}>
                  ERP source field
                </div>
                <input className="pf-input" value={r.erp} onChange={(e) => updateRow(i, 'erp', e.target.value)} style={{ ...MONO_INPUT, color: 'var(--fg-1)' }} />
              </label>
              <span style={{ color: 'var(--fg-4)', display: 'inline-flex', paddingBottom: 10 }} aria-hidden="true">
                {arrowGlyph}
              </span>
              <label style={{ minWidth: 0 }}>
                <div className="label" style={{ marginBottom: 5 }}>
                  NRS UBL target
                </div>
                <input className="pf-input" value={r.ubl} onChange={(e) => updateRow(i, 'ubl', e.target.value)} style={{ ...MONO_INPUT, color: 'var(--action)' }} />
              </label>
            </div>
          ))}
        </div>

        <div style={{ flex: 'none', display: 'flex', justifyContent: 'flex-end', gap: 9, padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
          <button type="button" onClick={onClose} className="pf-btn" style={{ ...FOOT_BTN, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)' }}>
            Cancel
          </button>
          <button type="button" onClick={save} className="pf-btn" style={{ ...FOOT_BTN, border: '1px solid var(--action)', background: 'var(--action)', color: 'var(--primary-foreground)' }}>
            Save mapping
          </button>
        </div>
      </div>
    </div>
  )
}
