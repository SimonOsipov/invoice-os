import type { ReactNode } from 'react'

export type SectionTone = 'cream' | 'dark' | 'dark2' | 'sage' | 'peach'

type SectionProps = {
  tone?: SectionTone
  id?: string
  paddingBlock?: string
  children?: ReactNode
}

export function Section({ tone = 'cream', id, paddingBlock, children }: SectionProps) {
  return (
    <section id={id} className={`ds-section band-${tone}`} style={paddingBlock ? { paddingBlock } : undefined}>
      <div className="container">{children}</div>
    </section>
  )
}
