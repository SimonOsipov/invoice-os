import { describe, expect, it } from 'vitest'
import { APP_PATHS } from './appPaths.ts'
import { COMING_SOON_IDS, FEATURES, GROUPS, STAGES, TOUR } from './content.ts'
import type { SceneKind } from './types.ts'

const byId = (id: string) => {
  const f = FEATURES.find((x) => x.id === id)
  if (!f) throw new Error(`no feature ${id}`)
  return f
}

describe('content', () => {
  it('content_hasElevenGroupsTwentySixFeaturesSixStagesSevenTourStops', () => {
    expect([GROUPS.length, FEATURES.length, STAGES.length, TOUR.length]).toEqual([11, 26, 6, 7])
    expect(FEATURES).toEqual(GROUPS.flatMap((g) => g.feats))
    expect(GROUPS.map((g) => g.n)).toEqual(['01', '02', '03', '04', '05', '06', '07', '08', '09', '10', '11'])
    expect(GROUPS.map((g) => g.feats.length)).toEqual([3, 3, 3, 2, 2, 2, 2, 3, 2, 2, 2])
  })

  it('content_everySceneIsOneOfTheSixKinds', () => {
    expect(FEATURES.length).toBeGreaterThan(0)
    const counts: Record<SceneKind, number> = { list: 0, form: 0, flow: 0, feed: 0, metrics: 0, toggles: 0 }
    for (const f of FEATURES) counts[f.sc.kind]++
    expect(counts).toEqual({ list: 6, form: 9, flow: 4, feed: 2, metrics: 2, toggles: 3 })
    for (const f of FEATURES) {
      expect(f.sc.win).not.toBe('')
      expect(f.sc.steps.length).toBeGreaterThanOrEqual(3)
      expect(f.sc.steps.length).toBeLessThanOrEqual(4)
      for (const s of f.sc.steps) expect(s.cap).not.toBe('')
    }
  })

  it('content_everyFeatureIsComplete', () => {
    expect(FEATURES.length).toBeGreaterThan(0)
    for (const f of FEATURES) {
      for (const s of [f.id, f.title, f.short, f.desc]) expect(s).not.toBe('')
      expect(f.benefits).toHaveLength(3)
      expect(f.who.length).toBeGreaterThanOrEqual(1)
      expect(f.rel.length).toBeGreaterThanOrEqual(2)
      expect(f.rel.length).toBeLessThanOrEqual(3)
      expect(GROUPS.find((g) => g.feats.includes(f))?.id).toBe(f.gid)
    }
  })

  it('content_keepsThePrototypeCopyVerbatim', () => {
    expect(byId('submit-clear').title).toBe('Submit for FIRS clearance')
    const feed = byId('audit-trail').sc
    if (feed.kind !== 'feed') throw new Error('audit-trail is not a feed')
    expect(feed.items.map((i) => i[1])).toContain('Cleared by FIRS')
    const flow = byId('submit-clear').sc
    if (flow.kind !== 'flow') throw new Error('submit-clear is not a flow')
    expect(flow.steps.map((s) => s.out)).toContain('Response received from FIRS')
    const list = byId('contacts').sc
    if (list.kind !== 'list') throw new Error('contacts is not a list')
    expect(list.steps[0].banner).toBe('Synced 3 contacts from Sage')
    expect(byId('import-files').benefits).toContain('Works with exports from Odoo, Sage, QuickBooks, SAP and Microsoft Dynamics')
    expect(JSON.stringify([GROUPS, FEATURES, STAGES, TOUR])).not.toContain('NRS')
  })

  it('content_marksExactlyTheElevenComingSoonFeatures', () => {
    const soon = FEATURES.filter((f) => f.status === 'soon').map((f) => f.id).sort()
    expect(soon).toEqual(['alerts', 'channels', 'company-profile', 'contacts', 'custom-rules', 'erp-connectors',
      'fiscal-outcomes', 'invite-members', 'learns', 'reports', 'rule-library'])
    expect(COMING_SOON_IDS).toHaveLength(11)
    for (const id of COMING_SOON_IDS) expect(FEATURES.map((f) => f.id)).toContain(id)
  })

  it('content_marksTheOtherFifteenShipped', () => {
    const shipped = FEATURES.filter((f) => f.status === 'shipped').map((f) => f.id)
    expect(shipped).toEqual(['import-files', 'create-invoice', 'invoice-status', 'read-documents', 'review-fields', 'validate',
      'approval-queue', 'workflow-builder', 'submit-clear', 'overview', 'audit-trail', 'evidence-bundle', 'portfolio',
      'onboard-client', 'roles'])
    for (const f of FEATURES) expect(['shipped', 'soon']).toContain(f.status)
  })

  it('content_shippedFeaturesHaveTheirViewsAppPath', () => {
    const shipped = FEATURES.filter((f) => f.status === 'shipped')
    expect(shipped.length).toBeGreaterThan(0)
    for (const f of shipped) expect(f.path, f.id).toBe(APP_PATHS[f.view])
    expect(byId('overview').path).toBe('/')
    expect(byId('import-files').path).toBe('/create')
    expect(byId('workflow-builder').path).toBe('/workflows')
  })

  it('content_comingSoonFeaturesHaveNoPath', () => {
    const soon = FEATURES.filter((f) => f.status === 'soon')
    expect(soon).toHaveLength(11)
    expect(byId('reports').view).toBe('reports')
    expect(byId('contacts').view).toBe('customers')
    for (const f of soon) expect('path' in f, f.id).toBe(false)
  })

  it('content_usesExactlyTheElevenPrototypeViews', () => {
    expect([...new Set(FEATURES.map((f) => f.view))].sort()).toEqual(
      ['approvals', 'audit', 'clients', 'create', 'customers', 'dashboard', 'invoices', 'reports', 'rules', 'settings', 'workflows'])
  })

  it('content_idsAreUnique', () => {
    expect(GROUPS.length).toBeGreaterThan(0)
    expect(new Set(GROUPS.map((g) => g.id)).size).toBe(GROUPS.length)
    expect(new Set(FEATURES.map((f) => f.id)).size).toBe(FEATURES.length)
  })

  it('content_everyRelNamesAFeature', () => {
    const ids = new Set(FEATURES.map((f) => f.id))
    expect(ids.size).toBeGreaterThan(0)
    for (const f of FEATURES) for (const r of f.rel) expect(ids.has(r), `${f.id} -> ${r}`).toBe(true)
  })

  it('content_everyTourStopNamesItsGroupAndFeature', () => {
    expect(TOUR.length).toBeGreaterThan(0)
    const stageIds = STAGES.map((s) => s[1])
    for (const t of TOUR) {
      const g = GROUPS.find((x) => x.id === t.g)
      expect(g, t.g).toBeDefined()
      expect(g!.feats.map((f) => f.id)).toContain(t.f)
      expect(t.stage).toBe(stageIds.indexOf(t.g))
    }
    expect(TOUR[6].stage).toBe(-1)
  })

  it('content_everyStageNamesAGroup', () => {
    expect(STAGES.length).toBeGreaterThan(0)
    const gids = new Set(GROUPS.map((g) => g.id))
    const named = STAGES.map((s) => s[1])
    for (const id of named) expect(gids.has(id), id).toBe(true)
    expect(new Set(named).size).toBe(named.length)
  })
})
