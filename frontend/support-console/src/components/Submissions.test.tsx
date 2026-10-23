import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import type { StaffTenant } from '../tenantsApi'
import { Submissions } from './Submissions'

const tenant = (id: string, name: string): StaffTenant => ({
  id, name, kind: 'firm', created_at: '2026-07-18T09:00:00Z', demo: false, active_members: 1, invoice_count: 2, last_activity_at: null,
})
const TENANTS = [tenant('T1', 'Okafor & Partners'), tenant('T2', 'Yaba Freight')]
const render = (selected: string) =>
  renderToStaticMarkup(
    <Submissions status="ready" jobs={[]} state={undefined} onState={() => {}} tenant={selected} onTenant={() => {}} tenants={TENANTS} onRetry={() => {}} onOpen={() => {}} />,
  )

describe('Submissions', () => {
  it('Submissions shows Open in Sentry for a selected tenant', () => {
    const html = render('T2')
    expect(html.match(/<a /g)).toHaveLength(1)
    const a = html.match(/<a [^>]*>(?:(?!<\/a>).)*<\/a>/)?.[0] ?? ''
    expect(a).toContain('href="https://as-comply.sentry.io/issues/?query=tenant_id:T2&amp;project=4512190351474768&amp;statsPeriod=14d"')
    expect(a).toContain('target="_blank"')
    expect(a).toContain('rel="noopener noreferrer"')
    expect(a).toMatch(/Open in Sentry<\/a>$/)
  })

  it('Submissions shows no Sentry link for All tenants', () => {
    const html = render('')
    expect(html).not.toContain('<a ')
    expect(html).not.toContain('Open in Sentry')
  })

  it('Submissions keeps the tenant select labelled and populated', () => {
    const select = render('T2').match(/<select[^>]*aria-label="Tenant"[^>]*>(?:(?!<\/select>).)*<\/select>/)?.[0] ?? ''
    expect(select).not.toBe('')
    expect(select.match(/<option[^>]*>[^<]*<\/option>/g)?.map((o) => o.replace(/<[^>]+>/g, ''))).toEqual(['All tenants', 'Okafor &amp; Partners', 'Yaba Freight'])
    expect(select).toMatch(/<option value="T2" selected="">Yaba Freight<\/option>/)
  })
})
