import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import * as glyphs from '../glyphs'

const SIDEBAR = resolve(__dirname, '../components/Sidebar.tsx')

// Sidebar builds its groups inline from the NAV_* consts, so the rosters are read out of the
// source rather than by mounting the component: the group arrays are literals and this keeps
// the assertion on the ordering itself, which is what the ACs are about.
function groupItems(source: string, marker: string): string[] {
  const at = source.indexOf(marker)
  if (at === -1) return []
  const open = source.indexOf('items: [', at)
  const close = source.indexOf(']', open)
  return source
    .slice(open + 'items: ['.length, close)
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

const NAV_ID: Record<string, string> = {
  NAV_DASHBOARD: 'dashboard',
  invoicesItem: 'invoices',
  approvalsItem: 'approvals',
  NAV_VALIDATION: 'validation',
  NAV_RULES: 'rules',
  NAV_CUSTOMERS: 'customers',
  NAV_REPORTS: 'reports',
  NAV_WORKFLOWS: 'workflows',
  NAV_CLIENTS: 'clients',
  NAV_AUDIT: 'audit',
  NAV_SETTINGS: 'settings',
}

const idsOf = (items: string[]) => items.map((n) => NAV_ID[n] ?? n)

describe('audit nav registration', () => {
  it('glyphs_navAuditIsInTheUnion', () => {
    expect(glyphs.NAV_AUDIT.id).toBe('audit')
    expect(glyphs.NAV_AUDIT.label.length).toBeGreaterThan(0)
  })

  it('sidebar_auditIsFirmWideInFirmMode', () => {
    const src = readFileSync(SIDEBAR, 'utf8')
    const client = idsOf(groupItems(src, "key: 'client'"))
    const firm = idsOf(groupItems(src, "key: 'firm'"))
    // Guard the reader itself: an empty roster would satisfy every membership assertion.
    expect(client.length).toBeGreaterThan(0)
    expect(firm.length).toBeGreaterThan(0)

    expect(firm).toContain('audit')
    expect(client).not.toContain('audit')
  })

  it('sidebar_auditPrecedesSettingsInBothPersonas', () => {
    const src = readFileSync(SIDEBAR, 'utf8')
    const rosters = {
      firm: idsOf(groupItems(src, "key: 'firm'")),
      inhouse: idsOf(groupItems(src, "key: 'workspace'")),
    }
    for (const [persona, roster] of Object.entries(rosters)) {
      expect(roster.length, `${persona} roster must not be empty`).toBeGreaterThan(0)
      const audit = roster.indexOf('audit')
      const settings = roster.indexOf('settings')
      expect(audit, `${persona} must carry audit`).toBeGreaterThanOrEqual(0)
      expect(settings, `${persona} must carry settings`).toBeGreaterThanOrEqual(0)
      expect(audit, `audit must sit immediately before settings in ${persona}`).toBe(settings - 1)
    }
  })
})
