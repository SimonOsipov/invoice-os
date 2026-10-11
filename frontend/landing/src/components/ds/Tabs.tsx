import { useId, useRef } from 'react'
import type { CSSProperties, KeyboardEvent, ReactNode } from 'react'

export type TabItem = { id: string; label: ReactNode; step?: string }

type TabsProps = {
  tabs: TabItem[]
  value: string
  onChange: (id: string) => void
  className?: string
  'aria-label'?: string
  panelStyle?: CSSProperties
  children?: ReactNode
}

/** Roving-tabindex wiring shared by Tabs and SegmentedTabs: Arrow/Home/End select, wrap, and move focus. */
export function useTabKeys(ids: string[], value: string, onChange: (id: string) => void) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const selected = ids.indexOf(value)
  const tabIndexOf = (i: number) => (i === (selected < 0 ? 0 : selected) ? 0 : -1)
  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, from: number) => {
    if (e.ctrlKey || e.altKey || e.metaKey) return
    const last = ids.length - 1
    const to = { ArrowRight: from === last ? 0 : from + 1, ArrowLeft: from === 0 ? last : from - 1, Home: 0, End: last }[e.key]
    if (to === undefined) return
    e.preventDefault()
    onChange(ids[to])
    refs.current[to]?.focus()
  }
  const ref = (i: number) => (el: HTMLButtonElement | null) => {
    refs.current[i] = el
  }
  return { selected, tabIndexOf, onKeyDown, ref }
}

export function Tabs({ tabs, value, onChange, className, 'aria-label': ariaLabel, panelStyle, children }: TabsProps) {
  const base = useId()
  const panelId = `${base}-panel`
  const tabId = (id: string) => `${base}-tab-${id}`
  const { selected, tabIndexOf, onKeyDown, ref } = useTabKeys(
    tabs.map((t) => t.id),
    value,
    onChange,
  )
  return (
    <div className={className ? `ds-tabs ${className}` : 'ds-tabs'}>
      <div role="tablist" className="ds-tabs-list" aria-label={ariaLabel}>
        {tabs.map((t, i) => (
          <button
            key={t.id}
            ref={ref(i)}
            id={tabId(t.id)}
            type="button"
            role="tab"
            className="ds-tab"
            aria-selected={i === selected}
            aria-controls={panelId}
            tabIndex={tabIndexOf(i)}
            onClick={() => onChange(t.id)}
            onKeyDown={(e) => onKeyDown(e, i)}
          >
            <span className="ds-tab-step">{t.step ?? String(i + 1).padStart(2, '0')}</span>
            {t.label}
          </button>
        ))}
      </div>
      <div
        role="tabpanel"
        id={panelId}
        className="ds-tabs-panel"
        tabIndex={0}
        aria-labelledby={selected < 0 ? undefined : tabId(tabs[selected].id)}
        style={panelStyle}
      >
        {children}
      </div>
    </div>
  )
}
