// Field-mapping editor for one connector, opened from the connector detail view's Edit
// button. Structurally mirrors EntityFormModal (fixed backdrop, stopPropagation'd panel,
// header + close, ghost/primary footer) — it edits a local draft of the mapping rows and
// only lifts them to the workspace (ctx.saveConnectorMapping) on Save, so Cancel and the
// backdrop both discard cleanly.

import { useState } from 'react'

import type { ConnectorDef } from '../data'
import { mappingFor } from '../lib/connectors'
import { closeGlyph } from '../glyphs'
import type { FieldMapRow, PlatformCtx } from '../types'

const MONO_INPUT: React.CSSProperties = { fontFamily: 'var(--font-mono)', fontSize: 12, height: 34 }

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
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'rgba(20,23,26,0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Edit field mapping"
        style={{ width: 620, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', boxShadow: '0 24px 60px -20px rgba(20,23,26,0.4)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 15, fontWeight: 600 }}>Edit field mapping</div>
            <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.06em', marginTop: 3 }}>
              ERP FIELD → FIRS UBL PATH
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="pf-btn"
            aria-label="Close"
            style={{ flex: 'none', width: 34, height: 34, borderRadius: 'var(--radius-lg)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflow: 'auto', padding: '6px 20px 16px' }}>
          {rows.map((r, i) => (
            <div key={i} style={{ padding: '12px 0', borderBottom: i === rows.length - 1 ? 0 : '1px solid var(--line-1)' }}>
              <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) 14px minmax(0, 1.35fr)', gap: 8, alignItems: 'end' }}>
                <label style={{ minWidth: 0 }}>
                  <div className="label" style={{ marginBottom: 5 }}>
                    ERP source field
                  </div>
                  <input className="pf-input" value={r.erp} onChange={(e) => updateRow(i, 'erp', e.target.value)} style={{ ...MONO_INPUT, color: 'var(--fg-1)' }} />
                </label>
                <span className="mono" style={{ fontSize: 11, color: 'var(--line-3)', paddingBottom: 10, textAlign: 'center' }} aria-hidden="true">
                  →
                </span>
                <label style={{ minWidth: 0 }}>
                  <div className="label" style={{ marginBottom: 5 }}>
                    FIRS UBL target
                  </div>
                  <input className="pf-input" value={r.ubl} onChange={(e) => updateRow(i, 'ubl', e.target.value)} style={{ ...MONO_INPUT, color: 'var(--accent)' }} />
                </label>
              </div>
            </div>
          ))}
        </div>

        <div style={{ flex: 'none', display: 'flex', justifyContent: 'flex-end', gap: 9, padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
          <button type="button" onClick={onClose} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 36, fontSize: 13 }}>
            Cancel
          </button>
          <button type="button" onClick={save} className="v2-btn v2-btn-primary pf-btn" style={{ height: 36, fontSize: 13 }}>
            Save mapping
          </button>
        </div>
      </div>
    </div>
  )
}
