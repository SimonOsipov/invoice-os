import { EXAMPLE_MAIL, WHAT_CTA, WHAT_EMAIL, WHAT_EYEBROW, WHAT_H2, WHAT_LIMITS, WHAT_RULES } from '../copy'
import { Button, Eyebrow, Icon } from '../ds'
import { CHECK_GLYPHS } from '../glyphs'

export function WhatYouGet() {
  return (
    <section id="what" className="check-what" aria-labelledby="check-what-h">
      <div className="check-wrap check-what-grid">
        <div className="check-what-copy">
          <Eyebrow>{WHAT_EYEBROW}</Eyebrow>
          <h2 id="check-what-h" className="t-h2 check-what-h">
            {WHAT_H2[0]}
            <br />
            <span className="check-what-accent">{WHAT_H2[1]}</span>
          </h2>
          {[WHAT_RULES, WHAT_EMAIL].map((b) => (
            <div key={b.title} className="check-what-block">
              <h3 className="t-card-title">{b.title}</h3>
              <p className="t-body">{b.text}</p>
            </div>
          ))}
          <div className="check-limits">
            {WHAT_LIMITS.map((l) => (
              <div key={l.caption}>
                <b>{l.n}</b>
                <span className="t-caption">{l.caption}</span>
              </div>
            ))}
          </div>
          <Button href="#check">
            {WHAT_CTA}
            <Icon paths={CHECK_GLYPHS['arrow-right']} size={16} strokeWidth={2} />
          </Button>
        </div>
        <figure className="check-example" aria-label="Example results email">
          <div className="check-mail">
            <div className="check-mail-head">
              <div className="check-mail-from">
                <span>
                  From <b>{EXAMPLE_MAIL.from}</b>
                </span>
                <span className="check-mail-tag">Example</span>
              </div>
              <div className="check-mail-subject">{EXAMPLE_MAIL.subject}</div>
            </div>
            {EXAMPLE_MAIL.invoices.map((inv) => (
              <div key={inv.number} className="check-mail-inv">
                <div className="check-mail-no">{inv.number}</div>
                {inv.rows.map((r) => (
                  <div key={r.label} className="check-mail-row">
                    <b>{r.label}</b>
                    <span>{r.value}</span>
                  </div>
                ))}
                {inv.rows.length === 0 && (
                  <div className="check-mail-row">
                    <b>Result</b>
                    <span>{EXAMPLE_MAIL.pass}</span>
                  </div>
                )}
              </div>
            ))}
            <div className="check-mail-line">{EXAMPLE_MAIL.line}</div>
          </div>
          <figcaption className="t-caption">{EXAMPLE_MAIL.caption}</figcaption>
        </figure>
      </div>
    </section>
  )
}
