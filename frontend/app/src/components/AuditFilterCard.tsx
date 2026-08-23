// The audit filter card's five popover triggers + pills row (AUDIT-07). This subtask
// (AUDIT-07-02) wires only search + date-range; events/actor/company land in
// AUDIT-07-04..06, the pills row in AUDIT-07-07.

import { useCallback, useState } from 'react'

import type { AuditFacets } from '../lib/audit'
import { AUDIT_COPY } from '../lib/auditView'
import { auditRangeIsValid, type AuditFilterState, type AuditRange, type AuditRangePreset } from '../lib/auditFilters'

import { FilterPopover } from './FilterPopover'

export interface AuditFilterCardProps {
  state: AuditFilterState
  facets: AuditFacets
  busy: boolean
  onChange: (next: AuditFilterState) => void
}

const DATE_PRESETS: { id: AuditRangePreset; label: string }[] = [
  { id: '24h', label: 'Last 24 hours' },
  { id: '7d', label: 'Last 7 days' },
  { id: '30d', label: 'Last 30 days' },
  { id: 'custom', label: 'Custom range' },
]

// The pinned obligation (task-653 QA): preset==='custom' with no from/to is what removing
// the date pill produces (auditFilters.ts REMOVE_RANGE) -- it renders as no date filter
// selected, never as Custom highlighted with two blank inputs.
function isCustomActive(range: AuditRange): boolean {
  return range.preset === 'custom' && !!(range.from || range.to)
}

function dateSummary(range: AuditRange): string | undefined {
  if (range.preset === '24h') return 'Last 24 hours'
  if (range.preset === '7d') return 'Last 7 days'
  if (range.preset === '30d') return 'Last 30 days'
  if (isCustomActive(range)) return `${range.from ?? ''} – ${range.to ?? ''}`
  return undefined
}

export function AuditFilterCard({ state, facets: _facets, busy, onChange }: AuditFilterCardProps) {
  // _facets: wired in AUDIT-07-04..06 (event/actor/company counts). Unused here.
  const [openPopover, setOpenPopover] = useState<'search' | 'date' | null>(null)
  const closePopover = useCallback(() => setOpenPopover(null), [])

  const [searchDraft, setSearchDraft] = useState(state.q)
  const openSearch = useCallback(() => {
    setSearchDraft(state.q)
    setOpenPopover('search')
  }, [state.q])
  const commitSearch = () => {
    if (searchDraft !== state.q) onChange({ ...state, q: searchDraft })
  }

  const [customView, setCustomView] = useState(false)
  const [dateFrom, setDateFrom] = useState('')
  const [dateTo, setDateTo] = useState('')
  const openDate = useCallback(() => {
    setCustomView(isCustomActive(state.range))
    setDateFrom(state.range.from ?? '')
    setDateTo(state.range.to ?? '')
    setOpenPopover('date')
  }, [state.range])

  const applyPreset = (preset: AuditRangePreset) => {
    onChange({ ...state, range: { preset } })
    closePopover()
  }
  const draftRange: AuditRange = { preset: 'custom', from: dateFrom, to: dateTo }
  const customValid = auditRangeIsValid(draftRange)
  const applyCustom = () => {
    onChange({ ...state, range: draftRange })
    closePopover()
  }

  return (
    <div
      data-testid="audit-filter-card"
      style={{
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        gap: 10,
        marginBottom: 16,
        padding: '12px 14px',
        border: '1px solid var(--line-1)',
        borderRadius: 'var(--radius-md)',
        background: 'var(--bg-2)',
      }}
    >
      <FilterPopover
        testId="audit-search"
        label="Search"
        summary={state.q !== '' ? `"${state.q}"` : undefined}
        open={openPopover === 'search'}
        onOpen={openSearch}
        onClose={closePopover}
        disabled={busy}
      >
        <div style={{ padding: 14, width: 280, display: 'flex', flexDirection: 'column', gap: 8 }}>
          <input
            type="text"
            data-testid="audit-search-input"
            className="pf-input"
            placeholder={AUDIT_COPY.searchPlaceholder}
            maxLength={200}
            value={searchDraft}
            onChange={(e) => setSearchDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitSearch()
            }}
            onBlur={commitSearch}
          />
          <p data-testid="audit-search-helper" style={{ margin: 0, fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
            {AUDIT_COPY.searchHelper}
          </p>
        </div>
      </FilterPopover>

      <FilterPopover
        testId="audit-date"
        label="Date range"
        summary={dateSummary(state.range)}
        open={openPopover === 'date'}
        onOpen={openDate}
        onClose={closePopover}
        disabled={busy}
      >
        <div style={{ padding: 6, width: 220 }}>
          {DATE_PRESETS.map(({ id, label }) => (
            <button
              key={id}
              type="button"
              data-testid={`audit-date-preset-${id}`}
              aria-pressed={id === 'custom' ? isCustomActive(state.range) : state.range.preset === id}
              onClick={() => (id === 'custom' ? setCustomView(true) : applyPreset(id))}
              className="pf-menu-item"
              style={{
                display: 'block',
                width: '100%',
                textAlign: 'left',
                border: 0,
                background: 'transparent',
                padding: '9px 12px',
                fontFamily: 'var(--font-sans)',
                fontSize: 13,
                color: 'var(--fg-1)',
                cursor: 'pointer',
              }}
            >
              {label}
            </button>
          ))}
          {customView && (
            <div style={{ padding: '10px 12px 12px', borderTop: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column', gap: 8 }}>
              <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11.5, color: 'var(--fg-3)' }}>
                From
                <input
                  type="date"
                  data-testid="audit-date-custom-from"
                  className="pf-input"
                  value={dateFrom}
                  onChange={(e) => setDateFrom(e.target.value)}
                />
              </label>
              <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11.5, color: 'var(--fg-3)' }}>
                To
                <input
                  type="date"
                  data-testid="audit-date-custom-to"
                  className="pf-input"
                  value={dateTo}
                  onChange={(e) => setDateTo(e.target.value)}
                />
              </label>
              {!customValid && (
                <span data-testid="audit-date-apply-reason" style={{ fontSize: 11.5, color: 'var(--status-red-text)' }}>
                  {AUDIT_COPY.dateRangeInvalidReason}
                </span>
              )}
              <button
                type="button"
                data-testid="audit-date-apply"
                disabled={!customValid}
                onClick={applyCustom}
                className="v2-btn v2-btn-ghost pf-btn"
                style={{ height: 30, fontSize: 12.5, opacity: customValid ? 1 : 0.4, cursor: customValid ? 'pointer' : 'not-allowed' }}
              >
                Apply
              </button>
            </div>
          )}
        </div>
      </FilterPopover>
    </div>
  )
}
