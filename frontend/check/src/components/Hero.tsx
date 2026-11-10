import type { ReactNode } from 'react'
import { CHECK_EYEBROW, CHECK_H1, CHECK_INTRO, TRUST_CUES } from '../copy'
import { Eyebrow, GLYPHS, Icon } from '../ds'
import { CHECK_GLYPHS } from '../glyphs'

const ICONS: Record<string, readonly string[]> = { ...GLYPHS, ...CHECK_GLYPHS }

// `wide` is the Map step: one column, a compact hero.
export function Hero({ wide, children }: { wide: boolean; children: ReactNode }) {
  return (
    <section className="check-hero" aria-labelledby="check-h1">
      <div className="check-wrap check-hero-pad">
        <div className={wide ? 'check-split check-split--wide' : 'check-split'}>
          <div className="check-copy">
            <Eyebrow tone="dark">{CHECK_EYEBROW}</Eyebrow>
            <h1 id="check-h1" className="check-h1">
              {CHECK_H1}
            </h1>
            <p className="check-lead">{CHECK_INTRO}</p>
            <ul className="check-trust" aria-label="About this check">
              {TRUST_CUES.map((c) => (
                <li key={c.title}>
                  <span className="check-trust-ic">
                    <Icon paths={ICONS[c.glyph]} size={18} strokeWidth={2} />
                  </span>
                  <span>
                    <strong>{c.title}</strong>
                    {c.text}
                  </span>
                </li>
              ))}
            </ul>
          </div>
          <div className="check-tool" id="check">
            {children}
          </div>
        </div>
      </div>
    </section>
  )
}
