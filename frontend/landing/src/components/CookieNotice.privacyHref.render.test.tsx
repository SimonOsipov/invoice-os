/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import { CookieNotice } from './CookieNotice'

const render = (privacyHref?: string) =>
  renderToStaticMarkup(
    <CookieNotice current={null} suppressed={false} onChoose={() => {}} privacyHref={privacyHref} />,
  )

describe('CookieNotice privacyHref', () => {
  it('CN-PH-01 the policy link follows privacyHref', () => {
    const html = render('https://l.example/privacy')
    const links = html.match(/<a class="lnk cn-link"[^>]*>[^<]*<\/a>/g) ?? []
    expect(links).toEqual([
      '<a class="lnk cn-link" href="https://l.example/privacy">Read the privacy &amp; cookie policy</a>',
    ])
    expect(render()).toContain('<a class="lnk cn-link" href="/privacy">')
  })
})
