import type { CSSProperties } from 'react'

import { DOC_TYPE_DEFS } from '../data'
import { CODE_LIST_OF, type CodeEntry, type CodeListField } from '../lib/codeLists'
import type { AuthedFetch } from '../lib/portfolio'
import { CodePicker } from './CodePicker'

const PAYMENT_STATUSES = ['PENDING', 'PAID', 'REJECTED', 'PARTIAL']

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
      <select {...rest} className="pf-input" value={value} disabled={disabled} style={style} onChange={(e) => onChange(e.target.value)}>
        <option value=""></option>
        {unknown && <option value={value}>{value}</option>}
        {options.map(([v, text]) => (
          <option key={v} value={v}>
            {text}
          </option>
        ))}
      </select>
    )
  }
  return <input {...rest} className="pf-input" value={value} disabled={disabled} style={style} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
}
