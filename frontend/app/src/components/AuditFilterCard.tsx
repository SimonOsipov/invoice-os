// The audit filter card's five popover triggers + pills row (AUDIT-07). AUDIT-07-02 wired
// search + date-range, AUDIT-07-04 added event type, AUDIT-07-05 added actor and AUDIT-07-06
// added company. AUDIT-07-07 added the pills row as the card's second row.

import { useCallback, useState, type ReactNode } from 'react'

import type { AuditFacet, AuditFacets } from '../lib/audit'
import { actorLabel } from '../lib/actor'
import { AUDIT_COPY } from '../lib/auditView'
import {
  auditFilterIsDefault,
  auditFilterPills,
  auditRangeIsValid,
  clearAllFilters,
  selectActor,
  selectKind,
  type AuditFilterState,
  type AuditRange,
  type AuditRangePreset,
} from '../lib/auditFilters'
import { AUDIT_EVENTS, auditEventView, type AuditDomain } from '../lib/auditVocabulary'

import { FilterPopover } from './FilterPopover'

export interface AuditFilterCardProps {
  state: AuditFilterState
  facets: AuditFacets
  busy: boolean
  onChange: (next: AuditFilterState) => void
}

const LABEL_ELLIPSIS = { flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const
const COUNT_STYLE = { fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 400, color: 'var(--fg-3)' } as const

export const DATE_PRESETS: { id: AuditRangePreset; label: string }[] = [
  { id: '24h', label: 'Last 24 hours' },
  { id: '7d', label: 'Last 7 days' },
  { id: '30d', label: 'Last 30 days' },
  { id: 'custom', label: 'Custom range' },
]

// Fixed group order + display headings (D-4). Row lists come from AUDIT_EVENTS, never
// hand-typed, so auditVocabulary.test.ts's identifier-count pin is the only place that count lives.
const DOMAIN_ORDER: AuditDomain[] = [
  'invoices',
  'approvals',
  'policies',
  'roles',
  'companies',
  'documents',
  'memberships',
  'validation',
  'submissions',
  'reconciliation',
]

const DOMAIN_LABELS: Record<AuditDomain, string> = {
  invoices: 'Invoices',
  approvals: 'Approvals',
  policies: 'Policies',
  roles: 'Roles',
  companies: 'Companies',
  documents: 'Documents',
  memberships: 'Memberships',
  validation: 'Validation rules',
  submissions: 'Submissions',
  reconciliation: 'Reconciliation',
}

interface EventGroup {
  domain: AuditDomain
  label: string
  ids: string[]
}

const EVENT_GROUPS: EventGroup[] = DOMAIN_ORDER.map((domain) => ({
  domain,
  label: DOMAIN_LABELS[domain],
  ids: Object.entries(AUDIT_EVENTS)
    .filter(([, def]) => def.domain === domain)
    .map(([id]) => id),
}))

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

// Never tallies the loaded page (Core AC 4) -- facets.event is server-computed with every
// OTHER active filter applied (internal/audit/facets.go); the client only looks it up.
function eventCount(facets: AuditFacets, id: string): number {
  return facets.event.find((f) => f.value === id)?.count ?? 0
}

function actorSummary(state: AuditFilterState, facets: AuditFacets): ReactNode | undefined {
  if (state.actorKind === 'people') return 'People only'
  if (state.actorKind === 'system') return 'System only'
  if (state.actors.length === 1) {
    const f = facets.actor.find((a) => a.value === state.actors[0])
    if (f?.name != null) return f.name
    // Q6: no resolvable name falls back to the raw subject, mono like lib/actor.ts.
    return <span style={{ fontFamily: 'var(--font-mono)' }}>{state.actors[0]}</span>
  }
  if (state.actors.length > 1) return `${state.actors.length} selected`
  return undefined
}

function companySummary(state: AuditFilterState): string | undefined {
  if (state.company.mode === 'workspace') return 'Workspace-level only'
  if (state.company.mode === 'named') return state.company.name
  return undefined
}

// Workspace count comes from the value===null bucket, never a client tally (AC#3).
function companyWorkspaceCount(facets: AuditFacets): number {
  return facets.company.find((f) => f.value === null)?.count ?? 0
}

export function AuditFilterCard({ state, facets, busy, onChange }: AuditFilterCardProps) {
  const [openPopover, setOpenPopover] = useState<'search' | 'date' | 'event' | 'actor' | 'company' | null>(null)
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

  const openEvent = useCallback(() => setOpenPopover('event'), [])
  // Every event control applies immediately -- there's no draft/Apply step like date range's
  // custom range, so each handler below is a direct onChange call.
  const toggleEvent = (id: string) => {
    const events = state.events.includes(id) ? state.events.filter((e) => e !== id) : [...state.events, id]
    onChange({ ...state, events })
  }
  const selectGroupAll = (ids: string[]) => {
    onChange({ ...state, events: Array.from(new Set([...state.events, ...ids])) })
  }
  const clearGroup = (ids: string[]) => {
    onChange({ ...state, events: state.events.filter((id) => !ids.includes(id)) })
  }
  const clearAllEvents = () => {
    onChange({ ...state, events: [] })
  }

  const openActor = useCallback(() => setOpenPopover('actor'), [])
  // Selecting a kind or a named actor always goes through selectKind/selectActor (auditFilters.ts) --
  // both mutators clear the other field, so the server's actor+actor_kind 400 is unreachable here.
  const selectKindRow = (kind: 'people' | 'system') => onChange(selectKind(state, kind))
  const selectActorRow = (id: string) => onChange(selectActor(state, id))
  // Anyone is a reset, not a selection -- selectKind's type only takes 'people' | 'system'.
  const selectAnyone = () => onChange({ ...state, actorKind: '', actors: [] })
  // AC#7: a refetch can drop a selected actor from facets.actor; synthesize its row at count 0
  // so an applied filter never goes invisible (auditActorFilter_selectedActorMissingFromFacetKeepsItsPill).
  const actorRows: AuditFacet[] = [
    ...facets.actor.filter((f) => f.value != null),
    ...state.actors
      .filter((id) => !facets.actor.some((f) => f.value === id))
      .map((id): AuditFacet => ({ value: id, name: null, kind: undefined, count: 0 })),
  ]

  const openCompany = useCallback(() => setOpenPopover('company'), [])
  const selectCompanyAll = () => onChange({ ...state, company: { mode: 'all' } })
  const selectCompanyWorkspace = () => onChange({ ...state, company: { mode: 'workspace' } })
  // Name is captured at selection time (AC#7) -- a later refetch dropping the bucket must
  // not blank a pill that already carries the resolved (or deleted-copy) name.
  const selectCompanyRow = (id: string, name: string) => onChange({ ...state, company: { mode: 'named', id, name } })
  const namedCompanyRows = facets.company.filter((f) => f.value != null)

  const pills = auditFilterPills(state, facets)
  const showClearAll = !auditFilterIsDefault(state)

  return (
    <div
      data-testid="audit-filter-card"
      style={{
        marginBottom: 14,
        padding: '13px 14px',
        border: '1px solid var(--line-1)',
        borderRadius: 'var(--radius-md)',
        background: 'var(--bg-2)',
      }}
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 9 }}>
        <FilterPopover
          testId="audit-search"
          label="Search"
          summary={state.q !== '' ? `"${state.q}"` : undefined}
          open={openPopover === 'search'}
          onOpen={openSearch}
          onClose={closePopover}
          disabled={busy}
        >
          <div style={{ padding: 12, width: 338, display: 'flex', flexDirection: 'column', gap: 9 }}>
            <input
              type="text"
              data-testid="audit-search-input"
              className="pf-input"
              style={{ height: 34, fontSize: 13 }}
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
          <div style={{ padding: '4px 0', width: 248 }}>
            {DATE_PRESETS.map(({ id, label }) => {
              const pressed = id === 'custom' ? isCustomActive(state.range) : state.range.preset === id
              return (
                <button
                  key={id}
                  type="button"
                  data-testid={`audit-date-preset-${id}`}
                  aria-pressed={pressed}
                  onClick={() => (id === 'custom' ? setCustomView(true) : applyPreset(id))}
                  className="pf-menu-item"
                  style={{
                    display: 'block',
                    width: '100%',
                    textAlign: 'left',
                    border: 0,
                    background: pressed ? 'var(--bg-3)' : 'transparent',
                    padding: '8px 12px',
                    fontFamily: 'var(--font-sans)',
                    fontSize: 13,
                    fontWeight: pressed ? 600 : 500,
                    color: 'var(--fg-1)',
                    cursor: 'pointer',
                  }}
                >
                  {label}
                </button>
              )
            })}
            {customView && (
              <div style={{ padding: '11px 12px', borderTop: '1px solid var(--line-1)', display: 'flex', flexDirection: 'column', gap: 8 }}>
                <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <span className="label">From</span>
                  <input
                    type="date"
                    data-testid="audit-date-custom-from"
                    className="pf-input"
                    style={{ height: 32, fontSize: 12.5 }}
                    value={dateFrom}
                    onChange={(e) => setDateFrom(e.target.value)}
                  />
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <span className="label">To</span>
                  <input
                    type="date"
                    data-testid="audit-date-custom-to"
                    className="pf-input"
                    style={{ height: 32, fontSize: 12.5 }}
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
                  className="v2-btn v2-btn-primary pf-btn"
                  style={{
                    height: 32,
                    justifyContent: 'center',
                    ...(customValid ? {} : { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }),
                  }}
                >
                  Apply
                </button>
              </div>
            )}
          </div>
        </FilterPopover>

        <FilterPopover
          testId="audit-event"
          label="Event type"
          summary={state.events.length > 0 ? `${state.events.length} selected` : undefined}
          open={openPopover === 'event'}
          onOpen={openEvent}
          onClose={closePopover}
          disabled={busy}
        >
          <div style={{ width: 338, maxHeight: 460, overflowY: 'auto' }}>
            <div
              style={{
                position: 'sticky',
                top: 0,
                zIndex: 1,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '10px 12px 8px',
                background: 'var(--bg-2)',
                borderBottom: '1px solid var(--line-1)',
              }}
            >
              <span style={{ fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>Event type</span>
              <button
                type="button"
                data-testid="audit-event-clear-all"
                onClick={clearAllEvents}
                className="pf-btn"
                style={{ border: 0, background: 'transparent', color: 'var(--action)', fontFamily: 'var(--font-sans)', fontSize: 11.5, fontWeight: 600, cursor: 'pointer' }}
              >
                Clear all
              </button>
            </div>
            {EVENT_GROUPS.map((group) => (
              <div key={group.domain} style={{ borderBottom: '1px solid var(--line-1)', paddingBottom: 4 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '9px 12px 5px' }}>
                  <span
                    data-testid={`audit-event-group-${group.domain}-heading`}
                    style={{
                      flex: 1,
                      fontFamily: 'var(--font-mono)',
                      fontSize: 9.5,
                      fontWeight: 700,
                      color: 'var(--fg-3)',
                      textTransform: 'uppercase',
                      letterSpacing: '0.08em',
                    }}
                  >
                    {group.label}
                  </span>
                  <div style={{ display: 'flex', gap: 10 }}>
                    <button
                      type="button"
                      data-testid={`audit-event-group-${group.domain}-all`}
                      onClick={() => selectGroupAll(group.ids)}
                      className="pf-btn"
                      style={{ border: 0, background: 'transparent', color: 'var(--action)', fontFamily: 'var(--font-sans)', fontSize: 11, fontWeight: 600, cursor: 'pointer' }}
                    >
                      All
                    </button>
                    <button
                      type="button"
                      data-testid={`audit-event-group-${group.domain}-clear`}
                      onClick={() => clearGroup(group.ids)}
                      className="pf-btn"
                      style={{ border: 0, background: 'transparent', color: 'var(--fg-3)', fontFamily: 'var(--font-sans)', fontSize: 11, fontWeight: 600, cursor: 'pointer' }}
                    >
                      Clear
                    </button>
                  </div>
                </div>
                {group.ids.map((id) => {
                  const selected = state.events.includes(id)
                  return (
                    <button
                      key={id}
                      type="button"
                      data-testid={`audit-event-row-${id}`}
                      aria-pressed={selected}
                      onClick={() => toggleEvent(id)}
                      className="pf-menu-item"
                      style={{
                        display: 'flex',
                        width: '100%',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: 10,
                        border: 0,
                        background: selected ? 'var(--bg-3)' : 'transparent',
                        padding: '6px 12px',
                        fontFamily: 'var(--font-sans)',
                        fontSize: 12.5,
                        fontWeight: selected ? 600 : 500,
                        color: selected ? 'var(--action)' : 'var(--fg-1)',
                        textAlign: 'left',
                        cursor: 'pointer',
                      }}
                    >
                      <span data-testid={`audit-event-label-${id}`} style={{ flex: 1, minWidth: 0 }}>
                        {auditEventView(id).label}
                      </span>
                      <span data-testid={`audit-event-count-${id}`} className="mono" style={COUNT_STYLE}>
                        {eventCount(facets, id)}
                      </span>
                    </button>
                  )
                })}
              </div>
            ))}
          </div>
        </FilterPopover>

        <FilterPopover
          testId="audit-actor"
          label="Actor"
          summary={actorSummary(state, facets)}
          open={openPopover === 'actor'}
          onOpen={openActor}
          onClose={closePopover}
          disabled={busy}
        >
          <div style={{ width: 278, maxHeight: 420, overflowY: 'auto', padding: '4px 0' }}>
            <div>
              {(
                [
                  { id: 'anyone', label: 'Anyone', pressed: state.actorKind === '' && state.actors.length === 0, onClick: selectAnyone },
                  { id: 'people', label: 'People only', pressed: state.actorKind === 'people', onClick: () => selectKindRow('people') },
                  { id: 'system', label: 'System only', pressed: state.actorKind === 'system', onClick: () => selectKindRow('system') },
                ] as const
              ).map((row) => (
                <button
                  key={row.id}
                  type="button"
                  data-testid={`audit-actor-kind-${row.id}`}
                  aria-pressed={row.pressed}
                  onClick={row.onClick}
                  className="pf-menu-item"
                  style={{
                    display: 'block',
                    width: '100%',
                    textAlign: 'left',
                    border: 0,
                    background: row.pressed ? 'var(--bg-3)' : 'transparent',
                    padding: '8px 12px',
                    fontFamily: 'var(--font-sans)',
                    fontSize: 13,
                    fontWeight: row.pressed ? 600 : 500,
                    color: 'var(--fg-1)',
                    cursor: 'pointer',
                  }}
                >
                  {row.label}
                </button>
              ))}
            </div>
            {actorRows.length > 0 && (
              <div style={{ marginTop: 4, paddingTop: 4, borderTop: '1px solid var(--line-1)' }}>
                {actorRows
                  .map((f) => {
                    const id = f.value as string
                    const label = actorLabel(f.value, { name: f.name ?? '', kind: f.kind ?? '' })
                    const selected = state.actors.includes(id)
                    return (
                      <button
                        key={id}
                        type="button"
                        data-testid={`audit-actor-row-${id}`}
                        aria-pressed={selected}
                        onClick={() => selectActorRow(id)}
                        className="pf-menu-item"
                        style={{
                          display: 'flex',
                          width: '100%',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          gap: 10,
                          border: 0,
                          background: selected ? 'var(--bg-3)' : 'transparent',
                          padding: '7px 12px',
                          fontFamily: 'var(--font-sans)',
                          fontSize: 12.5,
                          fontWeight: selected ? 600 : 500,
                          color: selected ? 'var(--action)' : 'var(--fg-1)',
                          textAlign: 'left',
                          cursor: 'pointer',
                        }}
                      >
                        <span
                          data-testid={`audit-actor-label-${id}`}
                          style={{
                            ...LABEL_ELLIPSIS,
                            fontFamily: label.mono ? 'var(--font-mono)' : 'var(--font-sans)',
                          }}
                        >
                          {label.text}
                        </span>
                        <span data-testid={`audit-actor-count-${id}`} style={COUNT_STYLE}>
                          {f.count}
                        </span>
                      </button>
                    )
                  })}
              </div>
            )}
          </div>
        </FilterPopover>

        <FilterPopover
          testId="audit-company"
          label="Company"
          summary={companySummary(state)}
          open={openPopover === 'company'}
          onOpen={openCompany}
          onClose={closePopover}
          disabled={busy}
        >
          <div style={{ width: 308, maxHeight: 420, overflowY: 'auto', padding: '4px 0' }}>
            <div>
              <button
                type="button"
                data-testid="audit-company-kind-all"
                aria-pressed={state.company.mode === 'all'}
                onClick={selectCompanyAll}
                className="pf-menu-item"
                style={{
                  display: 'block',
                  width: '100%',
                  textAlign: 'left',
                  border: 0,
                  background: state.company.mode === 'all' ? 'var(--bg-3)' : 'transparent',
                  padding: '8px 12px',
                  fontFamily: 'var(--font-sans)',
                  fontSize: 12.5,
                  fontWeight: state.company.mode === 'all' ? 600 : 500,
                  color: 'var(--fg-1)',
                  cursor: 'pointer',
                }}
              >
                All
              </button>
              <button
                type="button"
                data-testid="audit-company-kind-workspace"
                aria-pressed={state.company.mode === 'workspace'}
                onClick={selectCompanyWorkspace}
                className="pf-menu-item"
                style={{
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'flex-start',
                  gap: 2,
                  width: '100%',
                  textAlign: 'left',
                  border: 0,
                  background: state.company.mode === 'workspace' ? 'var(--bg-3)' : 'transparent',
                  padding: '8px 12px',
                  fontFamily: 'var(--font-sans)',
                  fontSize: 12.5,
                  fontWeight: state.company.mode === 'workspace' ? 600 : 500,
                  color: 'var(--fg-1)',
                  cursor: 'pointer',
                }}
              >
                <span style={{ display: 'flex', width: '100%', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span>Workspace-level only</span>
                  <span data-testid="audit-company-count-workspace" style={COUNT_STYLE}>
                    {companyWorkspaceCount(facets)}
                  </span>
                </span>
                {/* D-7 / contract §3: visible text, never a title= attribute (invisible in Chromium). */}
                <span
                  data-testid="audit-company-workspace-caveat"
                  style={{ fontSize: 11, fontWeight: 400, color: 'var(--fg-3)', lineHeight: 1.4 }}
                >
                  {AUDIT_COPY.companyWorkspaceCaveat}
                </span>
              </button>
            </div>
            {namedCompanyRows.length > 0 && (
              <div style={{ marginTop: 4, paddingTop: 4, borderTop: '1px solid var(--line-1)' }}>
                {namedCompanyRows.map((f) => {
                  const id = f.value as string
                  // Contract §5: a null Name with a non-null id is a deleted company, never blank
                  // and never mislabeled as the workspace row.
                  const label = f.name ?? AUDIT_COPY.companyDeletedLabel
                  const selected = state.company.mode === 'named' && state.company.id === id
                  return (
                    <button
                      key={id}
                      type="button"
                      data-testid={`audit-company-row-${id}`}
                      aria-pressed={selected}
                      onClick={() => selectCompanyRow(id, label)}
                      className="pf-menu-item"
                      style={{
                        display: 'flex',
                        width: '100%',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: 10,
                        border: 0,
                        background: selected ? 'var(--bg-3)' : 'transparent',
                        padding: '7px 12px',
                        fontFamily: 'var(--font-sans)',
                        fontSize: 12.5,
                        fontWeight: selected ? 600 : 500,
                        color: 'var(--fg-1)',
                        textAlign: 'left',
                        cursor: 'pointer',
                      }}
                    >
                      <span data-testid={`audit-company-label-${id}`} style={LABEL_ELLIPSIS}>
                        {label}
                      </span>
                      <span data-testid={`audit-company-count-${id}`} style={COUNT_STYLE}>
                        {f.count}
                      </span>
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        </FilterPopover>
      </div>

      {/* Second row (AUDIT-07-07): one removable pill per applied filter, plus Clear all. */}
      <div
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          alignItems: 'center',
          gap: 7,
          marginTop: 12,
          paddingTop: 12,
          borderTop: '1px solid var(--line-1)',
        }}
      >
        {pills.map((pill) => (
          <button
            key={pill.key}
            type="button"
            data-testid={`audit-pill-${pill.key}`}
            className="pf-chip"
            onClick={() => onChange(pill.onRemove(state))}
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 7,
              padding: '3px 5px 3px 10px',
              fontFamily: pill.mono ? 'var(--font-mono)' : 'var(--font-sans)',
              fontSize: 12,
              fontWeight: 400,
              border: '1px solid var(--line-2)',
              background: 'var(--bg-1)',
              color: 'var(--fg-2)',
              cursor: 'pointer',
            }}
          >
            {pill.label}
            <span aria-hidden style={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 16, height: 16, color: 'var(--fg-3)' }}>
              ×
            </span>
          </button>
        ))}
        {showClearAll && (
          <button
            type="button"
            data-testid="audit-clear-all"
            onClick={() => onChange(clearAllFilters())}
            className="pf-btn"
            style={{ border: 0, background: 'transparent', color: 'var(--action)', fontFamily: 'var(--font-sans)', fontSize: 12, fontWeight: 600, marginLeft: 4, cursor: 'pointer' }}
          >
            Clear all
          </button>
        )}
      </div>
    </div>
  )
}
