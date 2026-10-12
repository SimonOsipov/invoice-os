// @vitest-environment jsdom
// The Rules screen over a fake authedFetch: golden set, per-client custom rules, admin gating.
import { ApiError } from '@invoice-os/api-client'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  ADMIN_ONLY_REASON,
  ruleInputOf,
  type CustomRule,
  type GoldenRule,
  type GoldenRules,
} from '../lib/rulesApi'
import type { PlatformCtx } from '../types'
import { RulesView } from './RulesView'

const GW = 'https://gateway.test'
const A = 'aaaaaaaa-0000-4000-8000-00000000000a'
const B = 'bbbbbbbb-0000-4000-8000-00000000000b'

function golden(key: string, over: Partial<GoldenRule> = {}): GoldenRule {
  return { key, type: 'required', target: 'buyer.tin', params: {}, severity: 'error', when: null, message: `${key} message`, ...over }
}

const GOLDEN_KEYS = ['buyer-tin-format', 'currency-allowed', 'invoice-number-required']

function goldenPayload(over: Partial<GoldenRules> = {}): GoldenRules {
  return {
    today: '2026-10-12',
    versions: [
      { rule_set_version_id: 'v5', version: 5, state: 'scheduled', effective_from: '2027-01-01', rule_count: 67 },
      { rule_set_version_id: 'v4', version: 4, state: 'in_force', effective_from: '2026-08-06', rule_count: 3 },
      { rule_set_version_id: 'v3', version: 3, state: 'superseded', effective_from: '2026-03-01', rule_count: 19 },
    ],
    rule_set_version_id: 'v4',
    version: 4,
    effective_from: '2026-08-06',
    rules: GOLDEN_KEYS.map((k) => golden(k)),
    ...over,
  }
}

function custom(id: string, entity: string | null, over: Partial<CustomRule> = {}): CustomRule {
  return {
    id,
    key: `custom:${id}`,
    revision: 1,
    revision_id: `rev-${id}`,
    entity_id: entity,
    type: 'required',
    target: 'buyer.email',
    params: {},
    severity: 'warning',
    when: null,
    message: `message of ${id}`,
    enabled: true,
    updated_at: '2026-10-12T00:00:00Z',
    updated_by: 'u1',
    ...over,
  }
}

const FIRM = custom('r-firm', null)
const RA = custom('r-a', A)
const RB = custom('r-b', B)

type Handler = (url: string, init?: { method?: string; body?: unknown }) => unknown

function fakeFetch(opts: { golden?: GoldenRules | Error; custom?: CustomRule[] | Error; write?: Handler } = {}) {
  return vi.fn(async (url: string, init?: { method?: string; body?: unknown }) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET' && url.endsWith('/api/validation/v1/rules')) {
      const g = opts.golden ?? goldenPayload()
      if (g instanceof Error) throw g
      return g
    }
    if (method === 'GET' && url.endsWith('/api/invoice/v1/custom-rules')) {
      const c = opts.custom ?? [FIRM, RA, RB]
      if (c instanceof Error) throw c
      return { custom_rules: c }
    }
    return opts.write ? opts.write(url, init) : undefined
  })
}

type FetchMock = ReturnType<typeof fakeFetch>
const writes = (f: FetchMock) => f.mock.calls.filter(([, init]) => init?.method && init.method !== 'GET')
const reads = (f: FetchMock, suffix: string) => f.mock.calls.filter(([u, init]) => !init?.method && u.endsWith(suffix))

const ADMIN = [{ id: 'u1', isYou: true, role: 'admin', status: 'active', name: 'Ada', initials: 'A', email: null }]
const PREPARER = [{ id: 'u1', isYou: true, role: 'preparer', status: 'active', name: 'Ada', initials: 'A', email: null }]

function ctxOf(authedFetch: unknown, over: Record<string, unknown> = {}): PlatformCtx {
  return {
    mode: 'firm',
    active: { short: 'Adaeze Ventures', initials: 'AV', tin: '', entityId: A },
    activeEntity: { id: A, name: 'Adaeze Ventures Ltd' },
    authedFetch,
    members: ADMIN,
    membersState: 'ready',
    openRuleKey: null,
    openRule: vi.fn(),
    closeRule: vi.fn(),
    ...over,
  } as unknown as PlatformCtx
}

async function mount(f: FetchMock, over: Record<string, unknown> = {}) {
  const ctx = ctxOf(f, over)
  const view = render(<RulesView ctx={ctx} />)
  await screen.findByText(/^INHERITED/)
  return { ctx, view }
}

const rowKeys = () => [...document.querySelectorAll('.pf-row')].map((r) => r.children[0].textContent)

beforeEach(() => vi.stubEnv('VITE_GATEWAY_URL', GW))
afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
})

describe('RulesView, golden set', () => {
  it('rulesView_rendersTheServerGoldenSet', async () => {
    await mount(fakeFetch())

    const rail = screen.getByText('Golden ruleset').parentElement!
    expect(within(rail).getByText('v4').parentElement!.textContent).toContain('IN USE')
    expect(within(rail).getByText('since 2026-08-06 · 3 rules')).toBeTruthy()
    expect(within(rail).getByText('v5').parentElement!.textContent).toContain('SCHEDULED')
    expect(within(rail).getByText('starts 2027-01-01 · 67 rules')).toBeTruthy()
    expect(within(rail).getByText('v3').parentElement!.textContent).toContain('SUPERSEDED')

    const goldenRows = [...document.querySelectorAll('.pf-row')].slice(0, 3)
    expect(goldenRows.map((r) => r.children[0].textContent)).toEqual(GOLDEN_KEYS)
    goldenRows.forEach((r) => {
      expect(within(r as HTMLElement).getByText('LOCKED')).toBeTruthy()
      expect(within(r as HTMLElement).queryByRole('switch')).toBeNull()
      expect(within(r as HTMLElement).getByText('GOLDEN')).toBeTruthy()
    })
    expect(screen.getByText('GOLDEN v4 + 2 CUSTOM')).toBeTruthy()
  })

  it('the version states carry their tones: in force green, the others muted', async () => {
    await mount(fakeFetch())
    const tone = (tag: string) => (screen.getByText(tag).parentElement as HTMLElement).style.background
    expect(tone('IN USE')).toBe('var(--status-green-bg)')
    expect(tone('SCHEDULED')).toBe('var(--status-muted-bg)')
    expect(tone('SUPERSEDED')).toBe('var(--status-muted-bg)')
  })

  it('rulesView_rendersOnlyWhatTheServerSent', async () => {
    const f1 = fakeFetch({ golden: goldenPayload({ rules: [golden('only-golden')] }), custom: [custom('only-custom', null)] })
    const first = await mount(f1)
    expect(rowKeys()).toEqual(['only-golden', 'Custom rule'])
    expect(document.querySelectorAll('.pf-row')[1].children[0].getAttribute('title'), 'the full key stays reachable').toBe('custom:only-custom')
    expect(screen.getByText('message of only-custom')).toBeTruthy()
    first.view.unmount()

    await mount(fakeFetch({ golden: goldenPayload({ rules: [golden('x-1'), golden('x-2')] }), custom: [] }))
    expect(rowKeys()).toEqual(['x-1', 'x-2'])
    expect(screen.queryByText('only-golden')).toBeNull()
    expect(screen.getByText('No custom rules yet — the golden ruleset alone is running.')).toBeTruthy()
  })

  it('rulesView_noMockOrSuggestionText', async () => {
    await mount(fakeFetch())
    const text = document.body.textContent!
    for (const gone of ['Suggested for you', 'NG-MBS', 'v8', 'Add as custom rule']) expect(text).not.toContain(gone)
  })

  it('a row shows the server type, target, severity and message; an empty target reads whole invoice', async () => {
    await mount(
      fakeFetch({
        golden: goldenPayload({ rules: [golden('g-1', { type: 'enum', target: 'currency', severity: 'info' }), golden('g-2', { target: '', severity: 'warning' })] }),
        custom: [],
      }),
    )
    const cells = (i: number) => [...document.querySelectorAll('.pf-row')[i].children].map((c) => c.textContent)
    expect(document.querySelectorAll('.pf-row')).toHaveLength(2)
    expect(cells(0)).toEqual(['g-1', 'enum', 'currency', 'INFO', 'GOLDEN', 'g-1 message', 'LOCKED'])
    expect(cells(1)).toEqual(['g-2', 'required', 'whole invoice', 'WARN', 'GOLDEN', 'g-2 message', 'LOCKED'])
  })

  it('the group headers and the rail and table footers read as written', async () => {
    await mount(fakeFetch())
    expect(screen.getByText('INHERITED · GOLDEN RULESET v4')).toBeTruthy()
    expect(screen.getByText('CUSTOM · Adaeze Ventures')).toBeTruthy()
    expect(screen.getByText('Published and maintained by ASComply. New versions arrive automatically — you never edit these.')).toBeTruthy()
    const footer = screen.getByText(/^Custom rules evaluate after the golden ruleset/)
    expect(footer.textContent).toContain('A golden rule can never be disabled or edited.')
    expect(footer.style.padding).toBe('12px 16px')
    expect(footer.style.lineHeight).toBe('1.5')
    expect(footer.style.background).toBe('')
  })

  it('a golden row opens its rule; the switch click does not open the row', async () => {
    const f = fakeFetch()
    const { ctx } = await mount(f)

    fireEvent.click(screen.getByText('currency-allowed'))
    expect(ctx.openRule).toHaveBeenCalledWith('currency-allowed')
    fireEvent.click(screen.getAllByRole('switch')[0])
    expect(ctx.openRule).toHaveBeenCalledTimes(1)
  })
})

describe('RulesView, custom group', () => {
  it('rulesView_customGroupFollowsTheActiveClient', async () => {
    const f = fakeFetch()
    const a = await mount(f)
    const customRows = () => [...document.querySelectorAll('.pf-row')].slice(3).map((r) => [r.children[5].textContent, r.children[4].textContent])
    expect(customRows()).toEqual([
      ['message of r-firm', 'FIRM'],
      ['message of r-a', 'CLIENT'],
    ])
    a.view.rerender(<RulesView ctx={ctxOf(f, { activeEntity: { id: B, name: 'Bee Ltd' }, active: { short: 'Bee', initials: 'B', tin: '', entityId: B } })} />)
    expect(customRows()).toEqual([
      ['message of r-firm', 'FIRM'],
      ['message of r-b', 'CLIENT'],
    ])
    expect(screen.getByText('GOLDEN v4 + 2 CUSTOM')).toBeTruthy()
    expect(reads(f, '/custom-rules'), 'the swap is a filter, not a refetch').toHaveLength(1)
  })

  it('an in-house workspace labels every custom rule CUSTOM', async () => {
    await mount(fakeFetch(), { mode: 'inhouse' })
    expect(screen.getAllByText('CUSTOM').filter((e) => e.closest('.pf-row'))).toHaveLength(2)
  })

  it('no resolved client keeps firm-wide rules only', async () => {
    await mount(fakeFetch(), { activeEntity: null })
    expect(screen.queryByText('message of r-a')).toBeNull()
    expect(screen.getByText('message of r-firm')).toBeTruthy()
  })
})

describe('RulesView, switch', () => {
  it('the switch mirrors the rule: aria-checked, track tone and knob travel follow enabled', async () => {
    await mount(fakeFetch({ custom: [custom('on', null), custom('off', null, { enabled: false })] }))
    const on = screen.getByRole('switch', { name: 'Disable rule "message of on"' })
    const off = screen.getByRole('switch', { name: 'Enable rule "message of off"' })
    expect([on.getAttribute('aria-checked'), off.getAttribute('aria-checked')]).toEqual(['true', 'false'])
    expect([on.style.background, off.style.background]).toEqual(['var(--action)', 'var(--line-3)'])
    expect([(on.firstElementChild as HTMLElement).style.transform, (off.firstElementChild as HTMLElement).style.transform]).toEqual(['translateX(14px)', 'translateX(0)'])
  })

  it('rulesView_switchSendsAFullPutAndWaits', async () => {
    let release!: (r: CustomRule) => void
    const f = fakeFetch({ write: () => new Promise<CustomRule>((res) => (release = res)) })
    await mount(f)
    const sw = screen.getByRole('switch', { name: `Disable rule "${FIRM.message}"` })

    fireEvent.click(sw)
    await waitFor(() => expect(writes(f)).toHaveLength(1))
    expect(writes(f)[0]).toEqual([
      `${GW}/api/invoice/v1/custom-rules/${FIRM.id}`,
      { method: 'PUT', body: { ...ruleInputOf(FIRM), enabled: false } },
    ])
    expect(sw.getAttribute('aria-checked'), 'unchanged until the answer').toBe('true')
    expect(sw.hasAttribute('disabled')).toBe(true)
    fireEvent.click(sw)
    expect(writes(f), 'a second click while in flight sends nothing').toHaveLength(1)

    release({ ...FIRM, enabled: false, revision: 2 })
    await waitFor(() => expect(screen.getByRole('switch', { name: `Enable rule "${FIRM.message}"` }).getAttribute('aria-checked')).toBe('false'))
    expect(screen.getByRole('switch', { name: `Enable rule "${FIRM.message}"` }).hasAttribute('disabled')).toBe(false)
  })

  it('rulesView_switchFailureKeepsTheRow', async () => {
    const f = fakeFetch({ write: () => Promise.reject(new ApiError('http', 'custom rule not found', 404)) })
    await mount(f)
    const sw = screen.getByRole('switch', { name: `Disable rule "${FIRM.message}"` })

    fireEvent.click(sw)
    expect((await screen.findByTestId('rule-row-error')).textContent).toBe('custom rule not found')
    expect(screen.getByRole('switch', { name: `Disable rule "${FIRM.message}"` }).getAttribute('aria-checked')).toBe('true')
    expect(sw.hasAttribute('disabled')).toBe(false)
  })

  it('a row error clears when the next attempt starts', async () => {
    let fail = true
    const f = fakeFetch({ write: () => (fail ? Promise.reject(new ApiError('http', 'boom', 500)) : Promise.resolve({ ...FIRM, enabled: false })) })
    await mount(f)
    fireEvent.click(screen.getByRole('switch', { name: `Disable rule "${FIRM.message}"` }))
    await screen.findByTestId('rule-row-error')
    fail = false
    fireEvent.click(screen.getByRole('switch', { name: `Disable rule "${FIRM.message}"` }))
    await waitFor(() => expect(screen.queryByTestId('rule-row-error')).toBeNull())
  })
})

describe('RulesView, remove', () => {
  it('ruleDrawer_removeDeletesAndRefetches', async () => {
    const f = fakeFetch({ write: () => undefined })
    const { ctx } = await mount(f, { openRuleKey: FIRM.key })

    fireEvent.click(within(screen.getByRole('dialog')).getByText('Remove rule'))
    await waitFor(() => expect(ctx.closeRule).toHaveBeenCalledTimes(1))
    expect(writes(f)).toEqual([[`${GW}/api/invoice/v1/custom-rules/${FIRM.id}`, { method: 'DELETE' }]])
    await waitFor(() => expect(reads(f, '/custom-rules')).toHaveLength(2))
  })

  it('ruleDrawer_removeFailureKeepsTheDrawer', async () => {
    const f = fakeFetch({ write: () => Promise.reject(new ApiError('http', 'custom rule not found', 404)) })
    const { ctx } = await mount(f, { openRuleKey: FIRM.key })

    fireEvent.click(within(screen.getByRole('dialog')).getByText('Remove rule'))
    expect((await screen.findByTestId('rule-drawer-error')).textContent).toBe('custom rule not found')
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(ctx.closeRule).not.toHaveBeenCalled()
    expect(reads(f, '/custom-rules'), 'no refetch').toHaveLength(1)
  })

  it('ruleDrawer_removeIsDisabledInFlight', async () => {
    let release!: () => void
    const f = fakeFetch({ write: () => new Promise<void>((res) => (release = res)) })
    await mount(f, { openRuleKey: FIRM.key })
    const btn = within(screen.getByRole('dialog')).getByText('Remove rule')

    fireEvent.click(btn)
    fireEvent.click(btn)
    await waitFor(() => expect(btn.hasAttribute('disabled')).toBe(true))
    expect(writes(f)).toHaveLength(1)
    release()
  })

  it('an open golden key opens a read-only drawer; a custom key opens the custom banner', async () => {
    const g = await mount(fakeFetch(), { openRuleKey: 'currency-allowed' })
    const dialog = screen.getByRole('dialog')
    expect(dialog.getAttribute('aria-label')).toBe('Rule currency-allowed')
    expect(within(dialog).getByText('Managed by ASComply · golden ruleset v4 · read-only')).toBeTruthy()
    expect(within(dialog).queryByText('Remove rule')).toBeNull()
    g.view.unmount()

    const c = await mount(fakeFetch(), { openRuleKey: RA.key })
    expect(screen.getByText('Custom rule · Adaeze Ventures Ltd · runs after the golden ruleset')).toBeTruthy()
    c.view.unmount()
    await mount(fakeFetch(), { openRuleKey: FIRM.key })
    expect(screen.getByText('Custom rule · FIRM-WIDE · runs after the golden ruleset')).toBeTruthy()
  })

  it('a click on a custom row opens its key; no key and an unknown key open no drawer', async () => {
    const { ctx } = await mount(fakeFetch())
    fireEvent.click(screen.getByText('message of r-a'))
    expect(ctx.openRule).toHaveBeenCalledWith(RA.key)
    expect(screen.getAllByText('LOCKED'), 'control: the view rendered').not.toHaveLength(0)
    expect(screen.queryByRole('dialog')).toBeNull()
    cleanup()

    await mount(fakeFetch(), { openRuleKey: 'no.such.rule' })
    expect(screen.getAllByText('LOCKED'), 'control: the view rendered').not.toHaveLength(0)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('a remove drops the row at once and the refetch agrees', async () => {
    let rules = [FIRM, RA]
    let reads = 0
    let release!: () => void
    const f = vi.fn(async (url: string, init?: { method?: string }) => {
      if (init?.method === 'DELETE') {
        rules = [FIRM]
        return undefined
      }
      if (url.endsWith('/custom-rules')) {
        if (++reads === 2) await new Promise<void>((res) => (release = res))
        return { custom_rules: rules }
      }
      return goldenPayload()
    })
    await mount(f as unknown as FetchMock, { openRuleKey: RA.key })
    const messages = () => [...document.querySelectorAll('.pf-row')].slice(3).map((r) => r.children[5].textContent)
    expect(messages()).toEqual(['message of r-firm', 'message of r-a'])

    fireEvent.click(within(screen.getByRole('dialog')).getByText('Remove rule'))
    await waitFor(() => expect(reads).toBe(2))
    expect(messages(), 'gone while the refetch is still pending').toEqual(['message of r-firm'])
    release()
    await waitFor(() => expect(messages()).toEqual(['message of r-firm']))
    expect(screen.getByText('GOLDEN v4 + 1 CUSTOM')).toBeTruthy()
  })

  it('a key of another client opens no drawer', async () => {
    await mount(fakeFetch(), { openRuleKey: RB.key })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('RulesView, admin gating', () => {
  async function nonAdmin(over: Record<string, unknown> = {}) {
    const f = fakeFetch()
    const m = await mount(f, { members: PREPARER, ...over })
    return { f, ...m }
  }

  it('rulesView_nonAdminSeesDisabledControls', async () => {
    const { f } = await nonAdmin({ openRuleKey: FIRM.key })

    const controls = [screen.getByTestId('rules-new'), ...screen.getAllByRole('switch'), screen.getByTestId('rule-remove')]
    for (const c of controls) {
      expect(c.hasAttribute('disabled')).toBe(true)
      expect(c.getAttribute('title')).toBe(ADMIN_ONLY_REASON)
      fireEvent.click(c)
    }
    expect(screen.getByTestId('rules-admin-reason').textContent).toBe(ADMIN_ONLY_REASON)
    expect(writes(f)).toHaveLength(0)
    expect(rowKeys()).toHaveLength(GOLDEN_KEYS.length + 2)
  })

  it('rulesView_nonAdminCannotOpenTheEditor', async () => {
    const { f } = await nonAdmin({ openRuleKey: FIRM.key })

    const edit = screen.getByTestId('rule-edit')
    expect(edit.hasAttribute('disabled')).toBe(true)
    expect(edit.getAttribute('title')).toBe(ADMIN_ONLY_REASON)
    fireEvent.click(edit)
    fireEvent.click(screen.getByTestId('rules-new'))
    expect(screen.queryByRole('dialog', { name: 'New custom rule' })).toBeNull()
    expect(screen.queryByRole('dialog', { name: 'Edit custom rule' })).toBeNull()
    expect(writes(f)).toHaveLength(0)
  })

  it('an admin sees Edit in the drawer and New rule opens the editor', async () => {
    const { ctx } = await mount(fakeFetch(), { openRuleKey: FIRM.key })
    expect(screen.getByTestId('rule-edit').hasAttribute('disabled')).toBe(false)
    fireEvent.click(screen.getByTestId('rules-new'))
    expect(await screen.findByRole('dialog', { name: 'New custom rule' })).toBeTruthy()
    expect(screen.queryByRole('dialog', { name: `Rule ${FIRM.key}` }), 'the detail drawer makes way').toBeNull()
    expect(ctx.closeRule).not.toHaveBeenCalled()
  })

  it('rulesView_unloadedRosterIsNotANonAdmin', async () => {
    for (const [membersState, members] of [['loading', []], ['ready', []], ['error', []]] as const) {
      const { view } = await mount(fakeFetch(), { membersState, members })
      expect(screen.getByTestId('rules-new').hasAttribute('disabled'), membersState).toBe(true)
      screen.getAllByRole('switch').forEach((s) => expect(s.hasAttribute('disabled')).toBe(true))
      expect(screen.queryByTestId('rules-admin-reason'), membersState).toBeNull()
      expect(screen.getByTestId('rules-new').getAttribute('title')).toBeNull()
      view.unmount()
    }
    await mount(fakeFetch(), { membersState: 'ready', members: ADMIN })
    expect(screen.getByTestId('rules-new').hasAttribute('disabled')).toBe(false)
    screen.getAllByRole('switch').forEach((s) => expect(s.hasAttribute('disabled')).toBe(false))
  })

  it('an admin sees New rule enabled and no reason line', async () => {
    await mount(fakeFetch())
    expect(screen.getByTestId('rules-new').hasAttribute('disabled')).toBe(false)
    expect(screen.queryByTestId('rules-admin-reason')).toBeNull()
  })

  it('a suspended admin is not an admin', async () => {
    await mount(fakeFetch(), { members: [{ ...ADMIN[0], status: 'suspended' }] })
    expect(screen.getByTestId('rules-new').hasAttribute('disabled')).toBe(true)
    expect(screen.getByTestId('rules-admin-reason')).toBeTruthy()
  })
})

describe('RulesView, load states', () => {
  it('rulesView_loadFailureAndNoGateway: a failed golden read shows the error, retry refetches', async () => {
    let fail = true
    const f = vi.fn(async (url: string) => {
      if (url.endsWith('/rules')) {
        if (fail) throw new ApiError('http', 'rules are down', 503)
        return goldenPayload()
      }
      return { custom_rules: [] }
    })
    render(<RulesView ctx={ctxOf(f)} />)
    expect(await screen.findByText('rules are down')).toBeTruthy()
    expect(screen.queryByText(/^INHERITED/)).toBeNull()

    fail = false
    fireEvent.click(screen.getByText('Retry'))
    expect(await screen.findByText(/^INHERITED/)).toBeTruthy()
  })

  it('a failed custom read shows the error too; retry refetches only the custom list', async () => {
    let fail = true
    const f = vi.fn(async (url: string) => {
      if (url.endsWith('/custom-rules')) {
        if (fail) throw new ApiError('http', 'custom rules are down', 500)
        return { custom_rules: [FIRM] }
      }
      return goldenPayload()
    })
    render(<RulesView ctx={ctxOf(f)} />)
    expect(await screen.findByText('custom rules are down')).toBeTruthy()
    expect(screen.queryByText(/^INHERITED/)).toBeNull()

    fail = false
    fireEvent.click(screen.getByText('Retry'))
    expect(await screen.findByText(/^INHERITED/)).toBeTruthy()
    expect(screen.getByText('message of r-firm')).toBeTruthy()
    expect(f.mock.calls.filter(([u]) => u.endsWith('/api/validation/v1/rules')), 'golden was not refetched').toHaveLength(1)
  })

  it('no gateway shows the empty state and fetches nothing', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const f = fakeFetch()
    render(<RulesView ctx={ctxOf(f)} />)
    expect(screen.getByText('Rules load from the server.')).toBeTruthy()
    expect(f).not.toHaveBeenCalled()
  })

  it('the header and subtitle render while loading', () => {
    render(<RulesView ctx={ctxOf(() => new Promise(() => {}))} />)
    expect(screen.getByRole('heading', { name: 'Rules' })).toBeTruthy()
    expect(screen.getByText("ASComply's golden ruleset plus the custom checks you run for Adaeze Ventures")).toBeTruthy()
  })

  it('the in-house subtitle names the workspace', async () => {
    await mount(fakeFetch(), { mode: 'inhouse' })
    expect(screen.getByText("ASComply's golden ruleset plus the custom checks Adaeze Ventures runs internally")).toBeTruthy()
  })
})

describe('RulesView, reskin pins', () => {
  it('pills are 4px, the lock is --fg-3, the toggle track is a 99px pill with a round knob', async () => {
    await mount(fakeFetch())
    const stack = screen.getByText('GOLDEN v4 + 2 CUSTOM')
    expect(stack.style.borderRadius).toBe('var(--radius-sm)')
    screen.getAllByText('LOCKED').forEach((l) => expect(l.style.color).toBe('var(--fg-3)'))
    document.querySelectorAll<HTMLElement>('.pf-toggle').forEach((t) => expect(t.style.borderRadius).toBe('99px'))
    document.querySelectorAll<HTMLElement>('.pf-knob').forEach((k) => expect(k.style.borderRadius).toBe('50%'))
  })

  it('New rule copies the Invite people recipe and sits left of the count', async () => {
    await mount(fakeFetch())
    const btn = screen.getByTestId('rules-new')
    expect(btn.className).toContain('v2-btn-primary')
    expect(btn.nextElementSibling?.textContent).toBe('5 RULES')
  })

  it('a disabled New rule wears the Invite people disabled recipe', async () => {
    await mount(fakeFetch(), { members: PREPARER })
    const btn = screen.getByTestId('rules-new')
    expect(btn.style.opacity).toBe('0.45')
    expect(btn.style.cursor).toBe('not-allowed')
  })

  it('the empty custom box keeps its padding and line height', async () => {
    await mount(fakeFetch({ custom: [] }))
    const box = screen.getByText(/^No custom rules yet/)
    expect(box.style.padding).toBe('18px 16px')
    expect(box.style.lineHeight).toBe('1.55')
  })

  it('headings carry no inline weight; the header row wraps and its count does not shrink', async () => {
    await mount(fakeFetch())
    const h1 = screen.getByRole('heading', { name: 'Rules' })
    const inForce = screen.getByText('Rules in force')
    expect(inForce.classList.contains('card-title')).toBe(true)
    for (const el of [h1, inForce]) {
      expect(el.style.fontWeight).toBe('')
      expect(el.style.fontFamily).toBe('')
    }
    const row = h1.parentElement!.parentElement!
    expect(row.style.flexWrap).toBe('wrap')
    expect(row.style.gap).toBe('20px')
    expect(h1.parentElement!.style.minWidth).toBe('0')
    const count = row.lastElementChild as HTMLElement
    expect(count.textContent).toBe('5 RULES')
    expect(count.style.flex).toBe('0 0 auto')
    expect(count.style.letterSpacing).toBe('')
  })

  it('the version tags are 4px pills and the version row gaps 10px', async () => {
    await mount(fakeFetch())
    const tag = screen.getByText('IN USE').parentElement as HTMLElement
    expect(tag.style.borderRadius).toBe('var(--radius-sm)')
    expect((tag.parentElement as HTMLElement).style.gap).toBe('10px')
    expect(screen.getByText('v4').style.flex).toBe('')
  })
})

describe('RulesView, fix cycle 1', () => {
  it('rulesView_failedRefetchAfterRemoveKeepsTheRules', async () => {
    let reads = 0
    const f = vi.fn(async (url: string, init?: { method?: string }) => {
      if (init?.method === 'DELETE') return undefined
      if (url.endsWith('/custom-rules')) {
        if (++reads === 2) throw new ApiError('http', 'custom rules are down', 500)
        return { custom_rules: [FIRM, RA] }
      }
      return goldenPayload()
    })
    await mount(f as unknown as FetchMock, { openRuleKey: RA.key })

    fireEvent.click(within(screen.getByRole('dialog')).getByText('Remove rule'))
    expect((await screen.findByTestId('rules-refresh-error')).textContent).toContain('custom rules are down')
    expect(screen.getByText(/^INHERITED/)).toBeTruthy()
    expect(screen.getByText('message of r-firm')).toBeTruthy()
    expect(screen.queryByText('message of r-a')).toBeNull()

    fireEvent.click(within(screen.getByTestId('rules-refresh-error')).getByText('Retry'))
    await waitFor(() => expect(screen.queryByTestId('rules-refresh-error')).toBeNull())
    await waitFor(() => expect([...document.querySelectorAll('.pf-row')].map((r) => r.children[5].textContent)).toContain('message of r-a'))
  })

  it('a refetch that starts after several writes is not dropped', async () => {
    let reads = 0
    const NEW = custom('r-new', null)
    const f = vi.fn(async (url: string, init?: { method?: string }) => {
      if (init?.method === 'DELETE') return undefined
      if (init?.method === 'PUT') return { ...FIRM, enabled: false }
      if (url.endsWith('/custom-rules')) return { custom_rules: ++reads === 1 ? [FIRM, RA] : [FIRM, NEW] }
      return goldenPayload()
    })
    await mount(f as unknown as FetchMock, { openRuleKey: RA.key })
    fireEvent.click(screen.getByRole('switch', { name: `Disable rule "${FIRM.message}"` }))
    await screen.findByRole('switch', { name: `Enable rule "${FIRM.message}"` })

    fireEvent.click(within(screen.getByRole('dialog')).getByText('Remove rule'))
    const messages = () => [...document.querySelectorAll('.pf-row')].slice(3).map((r) => r.children[5].textContent)
    await waitFor(() => expect(messages(), 'the post-write refetch lands').toEqual(['message of r-firm', 'message of r-new']))
  })

  it('rulesView_staleRefetchCannotOverwriteAToggle', async () => {
    let reads = 0
    let releaseRead!: () => void
    let releasePut!: (r: CustomRule) => void
    const f = vi.fn(async (url: string, init?: { method?: string }) => {
      if (init?.method === 'DELETE') return undefined
      if (init?.method === 'PUT') return new Promise<CustomRule>((res) => (releasePut = res))
      if (url.endsWith('/custom-rules')) {
        if (++reads === 2) await new Promise<void>((res) => (releaseRead = res))
        return { custom_rules: [FIRM, RA] }
      }
      return goldenPayload()
    })
    await mount(f as unknown as FetchMock, { openRuleKey: FIRM.key })
    const messages = () => [...document.querySelectorAll('.pf-row')].slice(3).map((r) => r.children[5].textContent)

    fireEvent.click(within(screen.getByRole('dialog')).getByText('Remove rule'))
    await waitFor(() => expect(reads).toBe(2))
    fireEvent.click(screen.getByRole('switch', { name: `Disable rule "${RA.message}"` }))
    await waitFor(() => expect(releasePut).toBeTypeOf('function'))
    releasePut({ ...RA, enabled: false })
    await screen.findByRole('switch', { name: `Enable rule "${RA.message}"` })

    releaseRead()
    await waitFor(() => expect(screen.queryByText('Loading rules…')).toBeNull())
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.getByRole('switch', { name: `Enable rule "${RA.message}"` }).getAttribute('aria-checked')).toBe('false')
    expect(messages(), 'the stale list does not bring the removed row back').toEqual(['message of r-a'])
  })

  it('a switch is named from the rule message, never its uuid', async () => {
    await mount(fakeFetch())
    const names = screen.getAllByRole('switch').map((s) => s.getAttribute('aria-label'))
    expect(names).toEqual(['Disable rule "message of r-firm"', 'Disable rule "message of r-a"'])
  })
})
