import { gatewayBase } from '@invoice-os/api-client'
import { useEffect, useId, useRef, useState, type CSSProperties } from 'react'

import { commitCode, searchCodes, type CodeEntry, type CodeList } from '../lib/codeLists'
import type { AuthedFetch } from '../lib/portfolio'

const DEBOUNCE_MS = 200
const LIMIT = 20
const NO_MATCH = 'No matching code.'
const UNAVAILABLE = 'Codes are unavailable right now. Try again later.'

type Props = {
  list: CodeList
  authedFetch: AuthedFetch
  value: string
  onChange: (code: string) => void
  onPick?: (entry: CodeEntry) => void
  disabled?: boolean
  style?: CSSProperties
  'data-line-field'?: string
  'aria-label'?: string
}

export function CodePicker({ list, authedFetch, value, onChange, onPick, disabled, style, ...rest }: Props) {
  const listId = useId()
  const [text, setText] = useState(value)
  const [results, setResults] = useState<CodeEntry[]>([])
  const [notice, setNotice] = useState('')
  const [picked, setPicked] = useState<CodeEntry | null>(null)
  const seq = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const textRef = useRef(value)
  const valueRef = useRef(value)

  useEffect(() => {
    valueRef.current = value
    textRef.current = value
    setText(value)
  }, [value])

  useEffect(() => () => clearTimeout(timer.current), [])

  function commit(entry: CodeEntry) {
    if (entry.code === valueRef.current) return
    valueRef.current = entry.code
    setPicked(entry)
    onChange(entry.code)
    onPick?.(entry)
  }

  async function run(q: string) {
    const id = ++seq.current
    const base = gatewayBase()
    try {
      if (base == null) throw new Error('no gateway configured')
      const codes = await searchCodes(authedFetch, base, list, q, LIMIT)
      if (id !== seq.current) return
      setResults(codes)
      setNotice(codes.length > 0 ? '' : q === '' ? UNAVAILABLE : NO_MATCH)
      const hit = commitCode(textRef.current, codes)
      if (hit) commit(hit)
    } catch {
      if (id === seq.current) setNotice(UNAVAILABLE)
    }
  }

  function onType(next: string) {
    setText(next)
    textRef.current = next
    clearTimeout(timer.current)
    const hit = commitCode(next, results)
    if (hit) commit(hit)
    timer.current = setTimeout(() => void run(next.trim()), DEBOUNCE_MS)
  }

  function onBlur() {
    clearTimeout(timer.current)
    if (text.trim() === '') {
      if (valueRef.current !== '') {
        valueRef.current = ''
        setPicked(null)
        onChange('')
      }
      setText('')
      textRef.current = ''
    } else if (text !== valueRef.current) {
      setText(valueRef.current)
      textRef.current = valueRef.current
    }
  }

  const label = results.find((e) => e.code === value)?.label ?? (picked?.code === value ? picked.label : '')
  const helper = notice || label

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <input
        {...rest}
        className="pf-input"
        list={listId}
        value={text}
        disabled={disabled}
        style={style}
        autoComplete="off"
        onFocus={() => void run(text.trim())}
        onChange={(e) => onType(e.target.value)}
        onBlur={onBlur}
      />
      <datalist id={listId}>
        {results.map((e) => (
          <option key={e.code} value={e.code}>
            {e.label}
          </option>
        ))}
      </datalist>
      {helper !== '' && <span style={{ fontSize: 12, color: 'var(--fg-3)' }}>{helper}</span>}
    </div>
  )
}
