// Field-mapping editor for one ERP connection, opened from the connector detail's Edit button.
// It edits a local draft and sends only what changed: the mapping PUT, then the lookups PUT.

import { useEffect, useRef, useState } from 'react'

import { ApiError, gatewayBase } from '@invoice-os/api-client'

import { arrowGlyph, closeGlyph } from '../glyphs'
import {
  ADD_LOOKUP,
  CANCEL,
  LOOKUP_VALUE_KEYS,
  MAPPING_SUB,
  MODAL_FIELDS,
  MODAL_LOOKUPS,
  MODAL_TITLE,
  SAVE,
  SAVING,
  USE_DEFAULT,
  lookupChanges,
  lookupKindText,
  lookupValueLabel,
  mappingChanges,
  saveErpLookups,
  saveErpMapping,
  type Lookup,
  type LookupsView,
  type MappingView,
} from '../lib/erpConnectionDetail'
import type { PlatformCtx } from '../types'

const MONO_INPUT: React.CSSProperties = { fontFamily: 'var(--font-mono)', fontSize: 12, height: 34, boxSizing: 'border-box', padding: '0 10px', background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)' }
const FOOT_BTN: React.CSSProperties = { height: 36, padding: '0 16px', borderRadius: 'var(--radius-btn)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500 }

// The value fields each lookup kind takes; mirrors kindFields in internal/erp/mapping_store.go.
const KIND_FIELDS: Record<string, (typeof LOOKUP_VALUE_KEYS)[number][]> = {
  tax_code: ['tax_category', 'exemption_code'],
  item: ['hsn_code', 'service_code'],
  customer: ['tin', 'state', 'lga'],
}
const blankLookup = (): Lookup => ({ kind: 'tax_code', erp_key: '', tax_category: null, exemption_code: null, hsn_code: null, service_code: null, tin: null, state: null, lga: null })
const clearValues = (l: Lookup): Lookup => ({ ...blankLookup(), kind: l.kind, erp_key: l.erp_key })
const SMALL_BTN: React.CSSProperties = { height: 30, padding: '0 10px', borderRadius: 'var(--radius-btn)', border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 12 }

export function FieldMappingModal({
  ctx,
  entityId,
  system,
  systemName,
  externalKey,
  mapping,
  lookups,
  onSaved,
  onClose,
}: {
  ctx: PlatformCtx
  entityId: string
  system: string
  systemName: string
  externalKey: string | null
  mapping: MappingView
  lookups: LookupsView | null
  onSaved: (next: { mapping?: MappingView; lookups?: LookupsView }) => void
  onClose: () => void
}) {
  const [saved, setSaved] = useState(mapping)
  const [savedLookups, setSavedLookups] = useState<Lookup[]>(lookups?.lookups ?? [])
  const [edits, setEdits] = useState<Record<string, string | null>>({})
  const [entries, setEntries] = useState<Lookup[]>(lookups?.lookups ?? [])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const busyRef = useRef(false)
  const aliveRef = useRef(true)
  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
    }
  }, [])

  const fieldChanges = mappingChanges(saved.fields, edits)
  const lookupDiff = externalKey === null ? [] : lookupChanges(savedLookups, entries)
  const dirty = fieldChanges.length > 0 || lookupDiff.length > 0

  function setEntry(i: number, next: Lookup) {
    setEntries((es) => es.map((e, idx) => (idx === i ? next : e)))
  }
  function removeEntry(i: number) {
    const e = entries[i]
    const exists = savedLookups.some((s) => s.kind === e.kind && s.erp_key === e.erp_key)
    setEntries((es) => (exists ? es.map((x, idx) => (idx === i ? clearValues(x) : x)) : es.filter((_, idx) => idx !== i)))
  }

  async function save() {
    const base = gatewayBase()
    if (busyRef.current || !dirty || !base) return
    busyRef.current = true
    setBusy(true)
    setError(null)
    try {
      if (fieldChanges.length > 0) {
        const m = await saveErpMapping(ctx.authedFetch, base, entityId, system, fieldChanges)
        if (!aliveRef.current) return
        setSaved(m)
        setEdits({})
        onSaved({ mapping: m })
      }
      if (externalKey !== null && lookupDiff.length > 0) {
        const l = await saveErpLookups(ctx.authedFetch, base, entityId, system, externalKey, lookupDiff)
        if (!aliveRef.current) return
        setSavedLookups(l.lookups)
        setEntries(l.lookups)
        onSaved({ lookups: l })
      }
      onClose()
    } catch (err) {
      // 401 ends the session in the fetch wrapper; only a 403 means the role changed.
      if (!aliveRef.current) return
      setError(err instanceof Error ? err.message : String(err))
      if (err instanceof ApiError && err.status === 403) ctx.refetchMembers()
    } finally {
      busyRef.current = false
      if (aliveRef.current) setBusy(false)
    }
  }

  const close = () => {
    if (!busy) onClose()
  }

  return (
    <div
      onClick={close}
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'color-mix(in srgb, var(--surface) 55%, transparent)', backdropFilter: 'blur(6px)', WebkitBackdropFilter: 'blur(6px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={MODAL_TITLE}
        data-testid="field-mapping-modal"
        style={{ width: 640, maxWidth: '100%', maxHeight: '100%', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', boxShadow: 'var(--shadow-card)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
            <span style={{ color: 'var(--action)', display: 'inline-flex' }}>{arrowGlyph}</span>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 15, fontWeight: 700 }}>{MODAL_TITLE}</div>
              <div className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                {systemName} · {MAPPING_SUB}
              </div>
            </div>
          </div>
          <button
            type="button"
            onClick={close}
            disabled={busy}
            className="pf-btn"
            aria-label="Close"
            style={{ flex: 'none', width: 30, height: 30, borderRadius: 'var(--radius-btn)', border: 0, background: 'transparent', color: 'var(--fg-3)', cursor: busy ? 'default' : 'pointer', display: 'grid', placeItems: 'center' }}
          >
            {closeGlyph}
          </button>
        </div>

        <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 10 }}>
          <div className="label">{MODAL_FIELDS}</div>
          {saved.fields.map((f) => {
            const edit = edits[f.target]
            const value = edit === undefined ? (f.source_path ?? '') : edit === null ? (f.default_path ?? '') : edit
            return (
              <div key={f.target} data-testid="field-mapping-row" data-target={f.target} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1.2fr) auto', gap: 10, alignItems: 'center' }}>
                <div style={{ minWidth: 0, fontSize: 13 }}>
                  {f.label}
                  {f.line && (
                    <span className="mono" style={{ marginLeft: 6, fontSize: 10, color: 'var(--fg-3)' }}>
                      LINE
                    </span>
                  )}
                </div>
                <input
                  className="pf-input"
                  aria-label={`${f.label} path`}
                  value={value}
                  disabled={busy}
                  onChange={(e) => setEdits((s) => ({ ...s, [f.target]: e.target.value }))}
                  style={{ ...MONO_INPUT, color: 'var(--action)' }}
                />
                {f.origin === 'client' && edit !== null ? (
                  <button type="button" data-testid="field-mapping-default" disabled={busy} onClick={() => setEdits((s) => ({ ...s, [f.target]: null }))} className="pf-btn" style={SMALL_BTN}>
                    {USE_DEFAULT}
                  </button>
                ) : (
                  <span />
                )}
              </div>
            )
          })}

          {externalKey !== null && (
            <>
              <div className="label" style={{ marginTop: 8 }}>
                {MODAL_LOOKUPS}
              </div>
              {entries.map((e, i) => (
                <div key={i} data-testid="lookup-row" style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
                  <select
                    aria-label="Lookup kind"
                    className="pf-input"
                    value={e.kind}
                    disabled={busy}
                    onChange={(ev) => setEntry(i, { ...clearValues(e), kind: ev.target.value })}
                    style={{ ...MONO_INPUT, color: 'var(--fg-1)' }}
                  >
                    {Object.keys(KIND_FIELDS).map((k) => (
                      <option key={k} value={k}>
                        {lookupKindText(k)}
                      </option>
                    ))}
                  </select>
                  <input
                    className="pf-input"
                    aria-label="ERP key"
                    placeholder="ERP key"
                    value={e.erp_key}
                    disabled={busy}
                    onChange={(ev) => setEntry(i, { ...e, erp_key: ev.target.value })}
                    style={{ ...MONO_INPUT, color: 'var(--fg-1)', width: 140 }}
                  />
                  {(KIND_FIELDS[e.kind] ?? []).map((k) => (
                    <input
                      key={k}
                      className="pf-input"
                      aria-label={lookupValueLabel[k]}
                      placeholder={lookupValueLabel[k]}
                      value={e[k] ?? ''}
                      disabled={busy}
                      onChange={(ev) => setEntry(i, { ...e, [k]: ev.target.value === '' ? null : ev.target.value })}
                      style={{ ...MONO_INPUT, color: 'var(--action)', width: 130 }}
                    />
                  ))}
                  <button type="button" aria-label="Remove lookup" disabled={busy} onClick={() => removeEntry(i)} className="pf-btn" style={SMALL_BTN}>
                    {closeGlyph}
                  </button>
                </div>
              ))}
              <div>
                <button type="button" data-testid="lookup-add" disabled={busy} onClick={() => setEntries((es) => [...es, blankLookup()])} className="pf-btn" style={SMALL_BTN}>
                  {ADD_LOOKUP}
                </button>
              </div>
            </>
          )}
        </div>

        {error !== null && (
          <div data-testid="field-mapping-error" role="alert" style={{ flex: 'none', padding: '10px 20px', borderTop: '1px solid var(--line-1)', fontSize: 12.5, color: 'var(--status-red-text)' }}>
            {error}
          </div>
        )}

        <div style={{ flex: 'none', display: 'flex', justifyContent: 'flex-end', gap: 9, padding: '14px 20px', borderTop: '1px solid var(--line-1)' }}>
          <button type="button" onClick={close} disabled={busy} className="pf-btn" style={{ ...FOOT_BTN, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)' }}>
            {CANCEL}
          </button>
          <button type="button" data-testid="field-mapping-save" onClick={save} disabled={busy || !dirty} className="pf-btn" style={{ ...FOOT_BTN, border: '1px solid var(--action)', background: 'var(--action)', color: 'var(--primary-foreground)', opacity: busy || !dirty ? 0.6 : 1 }}>
            {busy ? SAVING : SAVE}
          </button>
        </div>
      </div>
    </div>
  )
}
