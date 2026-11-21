// BUG-172: the Approvals Role cell squeezed the role name to "C…" beside the long unstaffed note.
// jsdom does no layout, so this renders the real ApprovalsView to markup and measures it in Chromium.
import { type Browser, chromium } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
// react is not an e2e dependency: reach the copy ApprovalsView itself resolves, so hooks share one instance.
import { createElement } from '../frontend/app/node_modules/react'
import { renderToStaticMarkup } from '../frontend/app/node_modules/react-dom/server.node.js'

import { ApprovalsView } from '../frontend/app/src/components/ApprovalsView'

type Reason = 'unstaffed' | 'nobody_can_approve' | null
const ROLE = 'Compliance Officer'

function invoice(id: string, warn: boolean, reason: Reason) {
  return {
    id, entity_id: 'ent-1', invoice_number: id.toUpperCase(), status: 'validated', issue_date: '2026-07-01T00:00:00Z',
    buyer_name: 'Beta Ltd', currency: 'NGN', total: '1075.00', violations: [], rejection_reasons: [],
    can_approve: true, approve_blocked_reason: null, approve_blocked_by_creator: false,
    approval: { run_state: 'open', pending_ord: 0, pending_role_title: ROLE, pending_holder_warn: warn, pending_holder_reason: reason, due_at: null, overdue: false },
  }
}

// Only the data source is faked; the row JSX and its inline styles are the shipped ones.
vi.mock('@invoice-os/api-client', async (orig) => ({
  ...(await orig<object>()),
  gatewayBase: () => 'http://gateway.test',
  useAsync: () => ({
    status: 'ready', error: null, run: () => {},
    data: {
      fetchedEntityId: 'ent-1',
      pagination: { limit: 50, offset: 0, total: 3 },
      invoices: [invoice('inv-no', true, 'nobody_can_approve'), invoice('inv-un', true, 'unstaffed'), invoice('inv-ok', false, null)],
    },
  }),
}))

const REPO = join(dirname(fileURLToPath(import.meta.url)), '..')
const PLATFORM_CSS = readFileSync(join(REPO, 'frontend/app/src/styles/platform.css'), 'utf8')

let browser: Browser
beforeAll(async () => { browser = await chromium.launch() })
afterAll(async () => { await browser.close() })

const ctx = { mode: 'firm', active: { entityId: 'ent-1' }, user: { tenantName: 'Acme Co' }, authedFetch: () => Promise.reject(new Error('unused')), openImportedInvoice: () => {} }

async function measureRows() {
  const html = renderToStaticMarkup(createElement(ApprovalsView, { ctx: ctx as never }))
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  await page.setContent(`<style>${PLATFORM_CSS}</style><body style="margin:0"><main>${html}</main></body>`)
  const rows = await page.$$eval('[data-testid=approval-row]', (els) =>
    els.map((row) => {
      const role = [...row.querySelectorAll('span')].find((s) => s.textContent === 'Compliance Officer')!
      const note = row.querySelector('[data-testid=approval-unstaffed-warning]')
      const r = role.getBoundingClientRect()
      const n = note?.getBoundingClientRect()
      return {
        truncated: role.scrollWidth > role.clientWidth,
        overlaps: n != null && n.left < r.right && n.right > r.left && n.top < r.bottom && n.bottom > r.top,
        hasNote: note != null,
      }
    }),
  )
  await page.close()
  return rows
}

describe('Approvals Role cell layout (BUG-172)', () => {
  it('keeps the whole role name beside the "Nobody here can approve this invoice" note, without overlap', async () => {
    const [nobody] = await measureRows()
    expect(nobody.hasNote).toBe(true)
    expect(nobody.truncated, 'role name is ellipsised (scrollWidth > clientWidth)').toBe(false)
    expect(nobody.overlaps, 'note box overlaps the role name box').toBe(false)
  })

  it('keeps the whole role name on an "Unstaffed seat" row and on a row with no note', async () => {
    const [, unstaffed, calm] = await measureRows()
    expect(unstaffed.hasNote).toBe(true)
    expect(calm.hasNote).toBe(false)
    for (const row of [unstaffed, calm]) {
      expect(row.truncated).toBe(false)
      expect(row.overlaps).toBe(false)
    }
  })
})
