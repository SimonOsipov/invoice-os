import type { CSSProperties, ReactNode } from 'react'
import { Icon } from '../icons'

export type ButtonVariant = 'primary' | 'outline' | 'outlineDark'
export type ButtonSize = 'sm' | 'md' | 'lg'

type ButtonProps = {
  variant?: ButtonVariant
  size?: ButtonSize
  arrow?: boolean
  href?: string
  onClick?: () => void
  style?: CSSProperties
  children: ReactNode
}

export function Button({ variant = 'primary', size = 'md', arrow, href, onClick, style, children }: ButtonProps) {
  const className = `ds-btn ds-btn--${variant} ds-btn--${size}`
  const content = (
    <>
      {children}
      {arrow && <Icon name="arrow-right" size={16} />}
    </>
  )
  if (href) {
    return (
      <a className={className} href={href} onClick={onClick} style={style}>
        {content}
      </a>
    )
  }
  return (
    <button type="button" className={className} onClick={onClick} style={style}>
      {content}
    </button>
  )
}
