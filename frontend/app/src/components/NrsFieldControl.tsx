import type { CSSProperties } from 'react'

import { DOC_TYPE_DEFS } from '../data'
import { chevDownGlyph } from '../glyphs'
import { CODE_LIST_OF, type CodeEntry, type CodeListField } from '../lib/codeLists'
import type { AuthedFetch } from '../lib/portfolio'
import { CodePicker } from './CodePicker'

const PAYMENT_STATUSES = ['PENDING', 'PAID', 'REJECTED', 'PARTIAL']

const SELECT_STYLE: CSSProperties = { width: '100%', height: 38, padding: '0 32px 0 12px', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-input)', backgroundColor: 'var(--bg-2)', color: 'var(--fg-1)', fontSize: 14, boxSizing: 'border-box' }

type Props = {
  fieldKey: string
  value: string
  onChange: (value: string) => void
  onPick?: (entry: CodeEntry) => void
  authedFetch: AuthedFetch
  disabled?: boolean
  style?: CSSProperties
  placeholder?: string
  'data-line-field'?: string
  'aria-label'?: string
  'aria-required'?: boolean
}

function listOf(key: string) {
  return (CODE_LIST_OF as Record<string, (typeof CODE_LIST_OF)[CodeListField] | undefined>)[key]
}

export function NrsFieldControl({ fieldKey, value, onChange, onPick, authedFetch, disabled, style, placeholder, ...rest }: Props) {
  const list = listOf(fieldKey)
  if (list) {
    return <CodePicker {...rest} list={list} authedFetch={authedFetch} value={value} onChange={onChange} onPick={onPick} disabled={disabled} style={style} />
  }
  if (fieldKey === 'invoice_kind' || fieldKey === 'payment_status') {
    const options: [string, string][] =
      fieldKey === 'invoice_kind'
        ? DOC_TYPE_DEFS.map(([kind, label]) => [kind, `${kind} · ${label}`])
        : PAYMENT_STATUSES.map((s) => [s, s])
    // A stored value outside the options stays selectable, never silently changed.
    const unknown = value !== '' && !options.some(([v]) => v === value)
    return (
      <span style={{ position: 'relative', display: 'block' }}>
        <select {...rest} className="pf-select" value={value} disabled={disabled} style={{ ...SELECT_STYLE, ...style }} onChange={(e) => onChange(e.target.value)}>
          <option value=""></option>
          {unknown && <option value={value}>{value}</option>}
          {options.map(([v, text]) => (
            <option key={v} value={v}>
              {text}
            </option>
          ))}
        </select>
        <span aria-hidden="true" style={{ position: 'absolute', right: 9, top: '50%', transform: 'translateY(-50%)', display: 'inline-flex', color: 'var(--fg-3)', pointerEvents: 'none' }}>
          {chevDownGlyph}
        </span>
      </span>
    )
  }
  return <input {...rest} className="pf-input" value={value} disabled={disabled} style={style} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
}
