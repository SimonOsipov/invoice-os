import { GLYPHS, Icon, type GlyphName } from '../../icons'

type IconTileProps = {
  name: GlyphName
  tone?: 'primary' | 'accent'
  size?: number
  iconSize?: number
}

export function IconTile({ name, tone = 'primary', size = 40, iconSize }: IconTileProps) {
  return (
    <span className={`ds-icontile ds-icontile--${tone}`} aria-hidden="true" style={{ width: size, height: size }}>
      <Icon paths={GLYPHS[name]} size={iconSize ?? Math.round(size * 0.5)} strokeWidth={2} />
    </span>
  )
}
