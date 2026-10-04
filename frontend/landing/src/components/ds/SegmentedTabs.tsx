import type { ReactNode } from 'react'

import { useTabKeys } from './Tabs'

type SegmentedTabsProps = {
  options: { id: string; label: ReactNode }[]
  value: string
  onChange: (id: string) => void
  'aria-label': string
  idBase?: string
}

export function SegmentedTabs({ options, value, onChange, 'aria-label': ariaLabel, idBase }: SegmentedTabsProps) {
  const { selected, tabIndexOf, onKeyDown, ref } = useTabKeys(
    options.map((o) => o.id),
    value,
    onChange,
  )
  return (
    <div role="tablist" className="ds-seg" aria-label={ariaLabel}>
      {options.map((o, i) => (
        <button
          key={o.id}
          ref={ref(i)}
          type="button"
          role="tab"
          id={idBase ? `${idBase}-tab-${o.id}` : undefined}
          aria-controls={idBase ? `${idBase}-panel-${o.id}` : undefined}
          className="ds-seg-btn"
          aria-selected={i === selected}
          tabIndex={tabIndexOf(i)}
          onClick={() => onChange(o.id)}
          onKeyDown={(e) => onKeyDown(e, i)}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
