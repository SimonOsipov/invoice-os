import { readFileSync } from 'node:fs'
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
  it('content_hasElevenGroupsTwentyEightFeaturesSixStagesSevenTourStops', () => {
    expect([GROUPS.length, FEATURES.length, STAGES.length, TOUR.length]).toEqual([11, 28, 6, 7])
    expect(FEATURES).toEqual(GROUPS.flatMap((g) => g.feats))
    expect(GROUPS.map((g) => g.n)).toEqual(['01', '02', '03', '04', '05', '06', '07', '08', '09', '10', '11'])
    expect(GROUPS.map((g) => g.feats.length)).toEqual([3, 3, 3, 2, 2, 2, 2, 3, 4, 2, 2])
  })

  it('content_membersGroupEndsWithTheTwoDeletionFeatures', () => {
    const members = GROUPS.find((g) => g.id === 'members')!
    expect(members.feats.map((f) => f.id)).toEqual(['invite-members', 'roles', 'delete-account', 'delete-workspace'])
  })

  it('content_everySceneIsOneOfTheSixKinds', () => {
    expect(FEATURES.length).toBeGreaterThan(0)
    const counts: Record<SceneKind, number> = { list: 0, form: 0, flow: 0, feed: 0, metrics: 0, toggles: 0 }
    for (const f of FEATURES) counts[f.sc.kind]++
    expect(counts).toEqual({ list: 6, form: 10, flow: 5, feed: 2, metrics: 2, toggles: 3 })
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

  it('content_noShippedFeatureNamesAnErpOrFirs', () => {
    const vendor = /\b(Sage|Odoo|QuickBooks|SAP|Dynamics|FIRS)\b/i
    const shipped = FEATURES.filter((f) => f.status === 'shipped')
    const liveGroups = GROUPS.filter((g) => g.feats.some((f) => f.status === 'shipped'))
    expect(shipped.length).toBeGreaterThan(0)
    expect(liveGroups.length).toBeGreaterThan(0)
    expect(TOUR.length).toBeGreaterThan(0)
    for (const f of shipped) expect(JSON.stringify(f), f.id).not.toMatch(vendor)
    for (const g of liveGroups) expect(`${g.name} ${g.one} ${g.intro}`, g.id).not.toMatch(vendor)
    for (const t of TOUR) expect(`${t.t} ${t.d}`, t.f).not.toMatch(vendor)
    const lead = /className="t-lead"[^>]*>([\s\S]*?)<\/p>/.exec(readFileSync(new URL('./components/Home.tsx', import.meta.url), 'utf8'))?.[1] ?? ''
    expect(lead.length).toBeGreaterThan(0)
    expect(lead).not.toMatch(vendor)
    expect(JSON.stringify(byId('erp-connectors'))).toMatch(vendor)
  })

  it('content_namesNrsNeverFirs', () => {
    const all = JSON.stringify([GROUPS, FEATURES, STAGES, TOUR])
    expect(all.length).toBeGreaterThan(0)
    expect(all).not.toMatch(/\bFIRS\b/)
    expect(all).toMatch(/\bNRS\b/)
  })

  it('content_aGroupWithNoShippedFeatureSaysComingSoon', () => {
    const none = GROUPS.filter((g) => g.feats.every((f) => f.status === 'soon'))
    expect(none.map((g) => g.id)).toEqual(['clearance', 'notifications'])
    for (const g of none) {
      expect(g.one, g.id).toMatch(/coming soon/i)
      expect(g.intro, g.id).toMatch(/coming soon/i)
    }
  })

  it('content_marksExactlyTheFiveComingSoonFeatures', () => {
    const soon = FEATURES.filter((f) => f.status === 'soon').map((f) => f.id).sort()
    expect(soon).toEqual(['alerts', 'channels', 'erp-connectors', 'fiscal-outcomes', 'submit-clear'])
    expect(COMING_SOON_IDS).toHaveLength(5)
    for (const id of COMING_SOON_IDS) expect(FEATURES.map((f) => f.id)).toContain(id)
  })

  it('content_marksTheOtherTwentyThreeShipped', () => {
    const shipped = FEATURES.filter((f) => f.status === 'shipped').map((f) => f.id)
    expect(shipped).toEqual(['import-files', 'create-invoice', 'invoice-status', 'read-documents', 'review-fields', 'learns',
      'rule-library', 'validate', 'custom-rules', 'approval-queue', 'workflow-builder', 'overview', 'reports', 'audit-trail',
      'evidence-bundle', 'portfolio', 'onboard-client', 'contacts', 'invite-members', 'roles', 'delete-account', 'delete-workspace', 'company-profile'])
    for (const f of FEATURES) expect(['shipped', 'soon']).toContain(f.status)
  })

  it('content_shippedFeaturesHaveTheirViewsAppPath', () => {
    const shipped = FEATURES.filter((f) => f.status === 'shipped')
    expect(shipped).toHaveLength(23)
    for (const f of shipped) expect(f.path, f.id).toBe(APP_PATHS[f.view])
    expect(byId('overview').path).toBe('/')
    expect(byId('import-files').path).toBe('/create')
    expect(byId('workflow-builder').path).toBe('/workflows')
    expect(byId('reports').path).toBe('/reports')
    expect(byId('contacts').path).toBe('/customers')
    expect(byId('company-profile').path).toBe('/settings')
    expect(byId('delete-account').path).toBe('/account')
    expect(byId('delete-workspace').path).toBe('/settings')
  })

  it('content_comingSoonFeaturesHaveNoPath', () => {
    const soon = FEATURES.filter((f) => f.status === 'soon')
    expect(soon).toHaveLength(5)
    expect(byId('submit-clear').view).toBe('invoices')
    expect(byId('alerts').view).toBe('settings')
    for (const f of soon) expect('path' in f, f.id).toBe(false)
  })

  it('content_usesExactlyTheTwelveAppViews', () => {
    expect([...new Set(FEATURES.map((f) => f.view))].sort()).toEqual(
      ['account', 'approvals', 'audit', 'clients', 'create', 'customers', 'dashboard', 'invoices', 'reports', 'rules', 'settings', 'workflows'])
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

  it('content_aTourStopSaysComingSoonExactlyWhenItsFeatureIsComingSoon', () => {
    const says = TOUR.map((t) => /coming soon/i.test(t.t + t.d))
    TOUR.forEach((t, n) => expect(says[n], t.f).toBe(byId(t.f).status === 'soon'))
    expect(says.flatMap((v, n) => (v ? [n] : []))).toEqual([4])
  })

  it('content_everyStageNamesAGroup', () => {
    expect(STAGES.length).toBeGreaterThan(0)
    const gids = new Set(GROUPS.map((g) => g.id))
    const named = STAGES.map((s) => s[1])
    for (const id of named) expect(gids.has(id), id).toBe(true)
    expect(new Set(named).size).toBe(named.length)
  })

  it('content_deletionScenesAreAFormAndAFlow', () => {
    const acct = byId('delete-account').sc
    const ws = byId('delete-workspace').sc
    expect(acct.kind).toBe('form')
    expect(acct.steps.length).toBeGreaterThanOrEqual(3)
    expect(acct.steps.length).toBeLessThanOrEqual(4)
    expect(ws.kind).toBe('flow')
    expect(ws.steps.length).toBeGreaterThanOrEqual(3)
    expect(ws.steps.length).toBeLessThanOrEqual(4)
    if (ws.kind !== 'flow') return
    const at = ws.steps.map((s) => s.at)
    for (const a of at) expect(a).toBeLessThan(ws.nodes.length)
    expect(at).toEqual([...at].sort((a, b) => a - b))
  })

  it('content_deletionEntriesNameWhoTheyAreFor', () => {
    expect(byId('delete-account').who).toEqual(['Everyone'])
    expect(byId('delete-workspace').who).toEqual(['Admin'])
  })

  it('content_deletionCopyNamesTheExport', () => {
    const ws = byId('delete-workspace')
    expect(JSON.stringify(ws)).toMatch(/Download export|downloads the export/i)
    expect(ws.sc.steps[0].cap).toMatch(/downloads the export/i)
    expect(ws.desc).toMatch(/ticks that it is downloaded/i)
    expect(ws.desc).toMatch(/every company's evidence, every original upload and the full audit log/i)
    expect(ws.sc.steps[1].cap).toMatch(/admin can download the export again/i)
    expect(byId('delete-account').desc).toMatch(/downloads the export/i)
  })

  // Add the emailed cancel link claim only once the app has it: edit this regex first.
  it('content_deletionCopyClaimsOnlyWhatIsLive', () => {
    const unshipped = /cancel link|e-?mail/i
    expect('cancel link by email').toMatch(unshipped)
    for (const id of ['delete-account', 'delete-workspace']) expect(JSON.stringify(byId(id)), id).not.toMatch(unshipped)
  })
})
