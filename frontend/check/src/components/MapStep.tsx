import { useEffect, useRef, useState } from 'react'
import { CHECK_FIELD_KEYS, FIELD_GROUPS, canSubmitMapping, fieldLabel, groupOf, previewColumns, type GroupId, type Mapping, type Suggestion } from '../fields'
import {
  CHOOSE_ANOTHER, CONTINUE, FIELDS_LEGEND, FIELDS_TITLE, GROUP_NONE, HIDE, MAP_ALL_USED, MAP_COLUMNS, MAP_STEP_NO, MAP_SUMMARY, MAP_TITLE, MAP_UNUSED,
  MATCHED_LINE, NEED_INVOICE_NO, NOT_IN_FILE, NOT_USED, PREVIEW_LABEL, PREVIEW_LESS, PREVIEW_MORE, PREVIEW_TITLE, REVIEW, SUGGESTED, SUPPLIER_HINT,
} from '../copy'
import { GLYPHS, Icon } from '../ds'
import { CHECK_GLYPHS } from '../glyphs'

interface Props {
  fileName: string
  suggestion: Suggestion
  fields: Mapping
  onField: (key: string, header: string | null) => void
  onBack: () => void
  onContinue: () => void
}

export function MapStep({ fileName, suggestion, fields, onField, onBack, onContinue }: Props) {
  const columns = previewColumns(suggestion, 3)
  const offered = columns.filter((c, i) => c.mappable && columns.findIndex((d) => d.header === c.header) === i)
  const used = new Set(Object.values(fields).filter((h) => h !== null))
  const unused = offered.filter((c) => !used.has(c.header))
  const matchedCount = CHECK_FIELD_KEYS.filter((k) => fields[k] !== null).length
  const total = CHECK_FIELD_KEYS.length
  const canContinue = canSubmitMapping(fields)
  const [allColumns, setAllColumns] = useState(false)
  const [open, setOpen] = useState<Record<GroupId, boolean>>({ invoice: true, amounts: false, line: false, buyer: false, supplier: true })
  const [review, setReview] = useState<Record<GroupId, boolean>>({ invoice: false, amounts: false, line: false, buyer: false, supplier: false })
  // The last-changed row keeps its list until its select blurs, so a change never unmounts the focused select.
  const [held, setHeld] = useState<{ key: string; listed: boolean } | null>(null)
  const pressing = useRef(false)
  const releaseOnPressEnd = useRef(false)
  const isListed = (k: string) => k === 'invoice_number' || (held?.key === k ? held.listed : fields[k] === null)

  // A blur caused by a press waits for the press to end, so the row under the pointer does not move first (map_aSecondDropDownOpensWhileTheFirstHolds).
  useEffect(() => {
    const end = () => {
      pressing.current = false
      if (releaseOnPressEnd.current) {
        releaseOnPressEnd.current = false
        setHeld(null)
      }
    }
    document.addEventListener('pointerup', end)
    document.addEventListener('pointercancel', end)
    return () => {
      document.removeEventListener('pointerup', end)
      document.removeEventListener('pointercancel', end)
    }
  }, [])

  const renderRow = (key: string) => {
    const tagged = fields[key] !== null && fields[key] === suggestion.mapping[key]
    return (
      <div key={key} className="check-row">
        <span className="check-row-lbl">
          <label htmlFor={`check-field-${key}`}>{fieldLabel(key)}</label>
          {key === 'invoice_number' && (
            <span className="check-req" aria-hidden="true">
              *
            </span>
          )}
          {tagged && (
            <span id={`check-tag-${key}`} className="check-tag">
              {SUGGESTED}
            </span>
          )}
        </span>
        <select
          id={`check-field-${key}`}
          className={fields[key] === null ? 'check-fld is-empty' : 'check-fld'}
          value={fields[key] ?? ''}
          required={key === 'invoice_number'}
          aria-describedby={tagged ? `check-tag-${key}` : undefined}
          onChange={(e) => {
            if (held?.key !== key) setHeld({ key, listed: isListed(key) })
            onField(key, e.target.value === '' ? null : e.target.value)
          }}
          onBlur={() => {
            if (pressing.current) releaseOnPressEnd.current = true
            else setHeld(null)
          }}
        >
          <option value="">{NOT_IN_FILE}</option>
          {offered.map((c) => (
            <option key={c.header} value={c.header}>
              {c.letter} · {c.header}
            </option>
          ))}
        </select>
      </div>
    )
  }

  return (
    <section className="check-card check-map">
      <div className="check-map-head">
        <div className="check-step-head">
          <span className="check-mono check-step-no">{MAP_STEP_NO}</span>
          <h2 className="check-step-title">{MAP_TITLE}</h2>
        </div>
        <p className="check-map-file">
          <Icon paths={GLYPHS['file-text']} size={16} />
          <span className="check-map-name">{fileName}</span>
          <span>{MAP_COLUMNS(columns.length)}</span>
        </p>
      </div>
      <div className="check-sum">
        <span className="check-sum-n">
          {matchedCount}
          <small> of {total}</small>
        </span>
        <p className="check-sum-text">{MAP_SUMMARY(matchedCount, total)}</p>
        <div className="check-sum-foot">
          <span className="check-meter" aria-hidden="true">
            <i style={{ width: `${Math.round((matchedCount / total) * 100)}%` }} />
          </span>
          {offered.length > 0 && <p className="check-unused-line">{unused.length > 0 ? MAP_UNUSED(unused, offered.length) : MAP_ALL_USED}</p>}
        </div>
      </div>
      <div className="check-preview">
        <div className="check-preview-head">
          <span className="check-sub">{PREVIEW_TITLE}</span>
          {columns.length > 4 && (
            <button type="button" className="check-link" aria-expanded={allColumns} onClick={() => setAllColumns(!allColumns)}>
              {allColumns ? PREVIEW_LESS : PREVIEW_MORE(columns.length)}
            </button>
          )}
        </div>
        <div className="check-scroll" role="region" aria-label={PREVIEW_LABEL} tabIndex={0}>
          <table className="check-columns">
            <thead>
              <tr>
                <th scope="col">Column</th>
                <th scope="col">Header</th>
                <th scope="col">Examples</th>
                <th scope="col">
                  <span className="check-sr">Status</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {(allColumns ? columns : columns.slice(0, 4)).map((c, i) => (
                <tr key={i}>
                  <td className="check-col">{c.letter}</td>
                  <td className="check-col-head">{c.header}</td>
                  <td className="check-col-ex">{c.samples.filter((s) => s !== '').join(' · ')}</td>
                  <td>{unused.includes(c) && <span className="check-unused">{NOT_USED}</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
      <div className="check-groups" onPointerDown={() => (pressing.current = true)}>
        <div className="check-groups-head">
          <span className="check-sub">{FIELDS_TITLE}</span>
          <span className="check-legend">
            <span className="check-req" aria-hidden="true">
              *
            </span>{' '}
            {FIELDS_LEGEND}
          </span>
        </div>
        {FIELD_GROUPS.map(([gid, title]) => {
          const keys = CHECK_FIELD_KEYS.filter((k) => groupOf(k) === gid)
          const matchedKeys = keys.filter((k) => fields[k] !== null)
          const listed = keys.filter(isListed)
          const reviewRows = keys.filter((k) => !isListed(k))
          const shown = review[gid]
          const renderRows = (rows: string[]) =>
            rows.length > 0 && <div className="check-rows">{rows.map(renderRow)}</div>
          return (
            <div key={gid} className="check-grp">
              <button
                type="button"
                className="check-acc"
                aria-expanded={open[gid]}
                aria-controls={`check-g-${gid}`}
                onClick={() => setOpen({ ...open, [gid]: !open[gid] })}
              >
                <span className="check-acc-text">
                  <span className="check-acc-title">{title}</span>
                  <span className="check-acc-sub">{matchedKeys.length > 0 ? matchedKeys.map(fieldLabel).join(', ') : GROUP_NONE}</span>
                </span>
                <span className="check-count">
                  <b>{matchedKeys.length}</b> of {keys.length} matched
                </span>
                <span className="check-chev" aria-hidden="true">
                  <Icon paths={GLYPHS[open[gid] ? 'chevron-up' : 'chevron-down']} size={18} strokeWidth={2} />
                </span>
              </button>
              {open[gid] && (
                <div id={`check-g-${gid}`}>
                  {renderRows(listed)}
                  {reviewRows.length > 0 && (
                    <div className="check-subhead">
                      <span>{MATCHED_LINE(reviewRows.filter((k) => fields[k] !== null).length)}</span>
                      <button type="button" className="check-link" aria-expanded={shown} onClick={() => setReview({ ...review, [gid]: !shown })}>
                        {shown ? HIDE : REVIEW}
                      </button>
                    </div>
                  )}
                  {shown && renderRows(reviewRows)}
                  {gid === 'supplier' && (
                    <div className="check-hint">
                      <Icon paths={GLYPHS['triangle-alert']} size={16} strokeWidth={2} />
                      <span>{SUPPLIER_HINT}</span>
                    </div>
                  )}
                </div>
              )}
            </div>
          )
        })}
      </div>
      <div className="check-sticky">
        <div className="check-actions">
          <button type="button" className="check-btn check-btn--outline" onClick={onBack}>
            {CHOOSE_ANOTHER}
          </button>
          <div className="check-actions-end">
            {!canContinue && (
              <span id="check-map-need" className="check-need">
                {NEED_INVOICE_NO}
              </span>
            )}
            <button type="button" className="check-btn check-btn--next" disabled={!canContinue} aria-describedby={canContinue ? undefined : 'check-map-need'} onClick={onContinue}>
              {CONTINUE}
              <Icon paths={CHECK_GLYPHS['arrow-right']} size={16} strokeWidth={2} />
            </button>
          </div>
        </div>
      </div>
    </section>
  )
}
