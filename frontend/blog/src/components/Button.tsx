import type { CSSProperties, ReactNode } from 'react'

export type ButtonVariant = 'primary' | 'outline' | 'outlineDark'
export type ButtonSize = 'sm' | 'md' | 'lg'

type ButtonProps = {
  variant?: ButtonVariant
  size?: ButtonSize
  href?: string
  disabled?: boolean
  onClick?: () => void
  style?: CSSProperties
  children: ReactNode
}

export function Button({ variant = 'primary', size = 'md', href, disabled, onClick, style, children }: ButtonProps) {
  const className = `ds-btn ds-btn--${variant} ds-btn--${size}`
  if (href) {
    return (
      <a className={className} href={href} onClick={onClick} style={style}>
        {children}
      </a>
    )
  }
  return (
    <button type="button" className={className} disabled={disabled} onClick={onClick} style={style}>
      {children}
    </button>
  )
}
