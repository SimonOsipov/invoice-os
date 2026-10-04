import type { CSSProperties, MouseEventHandler, ReactNode } from 'react'

import { GLYPHS, Icon } from '../../icons'

export type ButtonVariant = 'primary' | 'accent' | 'outline' | 'ghostDark' | 'text'
export type ButtonSize = 'sm' | 'md' | 'lg'

type ButtonProps = {
  variant?: ButtonVariant
  size?: ButtonSize
  href?: string
  type?: 'button' | 'submit' | 'reset'
  disabled?: boolean
  onClick?: MouseEventHandler<HTMLElement>
  className?: string
  style?: CSSProperties
  'aria-label'?: string
  target?: string
  rel?: string
  children?: ReactNode
}

export function Button({
  variant = 'primary',
  size,
  href,
  type,
  disabled,
  onClick,
  className,
  style,
  'aria-label': ariaLabel,
  target,
  rel,
  children,
}: ButtonProps) {
  const sized = variant === 'primary' || variant === 'accent' || variant === 'outline'
  const classes = ['ds-btn', `ds-btn--${variant}`]
  if (sized) classes.push(`ds-btn--${size ?? (variant === 'accent' ? 'lg' : 'md')}`)
  const inert = href !== undefined && disabled
  if (inert) classes.push('ds-btn--disabled')
  if (className) classes.push(className)
  const content = (
    <>
      {variant === 'ghostDark' && (
        <span className="ds-btn-play" aria-hidden="true">
          <Icon paths={GLYPHS.play} size={13} strokeWidth={2} />
        </span>
      )}
      {children}
    </>
  )
  if (inert) {
    // An anchor has no disabled state: no href, no target, no handler; .ds-btn--disabled styles it.
    return (
      <a className={classes.join(' ')} style={style} aria-disabled="true" aria-label={ariaLabel}>
        {content}
      </a>
    )
  }
  if (href !== undefined) {
    return (
      <a href={href} className={classes.join(' ')} style={style} aria-label={ariaLabel} target={target} rel={rel} onClick={onClick}>
        {content}
      </a>
    )
  }
  return (
    <button type={type ?? 'button'} className={classes.join(' ')} style={style} disabled={disabled} aria-label={ariaLabel} onClick={onClick}>
      {content}
    </button>
  )
}
