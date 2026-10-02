import type { ReactNode } from 'react'

export function Eyebrow({ tone = 'light', children }: { tone?: 'light' | 'dark'; children?: ReactNode }) {
  const dark = tone === 'dark'
  return <span className={dark ? 't-eyebrow ds-eyebrow--dark' : 't-eyebrow'}>{children}</span>
}
