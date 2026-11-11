// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/check/" }
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { CHECK_FIELD_KEYS } from './fields'
import { MAPPING_SENTENCE } from './messages'
import { GLYPHS } from './ds'
import { MAP_SUMMARY, NEED_INVOICE_NO } from './copy'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const store = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  },
})

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const suggestion = (columns: string[], mapping: Record<string, string>) => ({
  columns,
  sample_rows: [columns.map((_, i) => `v${i}`)],
  header_row: 1,
  rows_total: 1,
  mapping,
  source: 'ai',
})

const withRows = (columns: string[], mapping: Record<string, string>, sample_rows: string[][]) => ({ ...suggestion(columns, mapping), sample_rows })

let container: HTMLDivElement
let root: Root

async function toMap(...answers: ReturnType<typeof suggestion>[]) {
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async () => json(200, answers.length > 1 ? answers.shift() : answers[0])),
  )
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App />))
  await pickFile()
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://api.example.test')
  store.set('asc_consent', JSON.stringify({ analytics: false, ts: '2026-01-01T00:00:00Z', v: 1 }))
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  store.clear()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))))
}
async function pickFile() {
  const input = container.querySelector<HTMLInputElement>('input[type="file"]')!
  Object.defineProperty(input, 'files', { value: [new File(['aaaa'], 'a.csv')], configurable: true })
  await act(async () => void input.dispatchEvent(new Event('change', { bubbles: true })))
  await settle()
}
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const sel = (key: string) => container.querySelector<HTMLSelectElement>(`#check-field-${key}`)
const must = (key: string) => sel(key)!
const header = (gid: string) => container.querySelector<HTMLButtonElement>(`.check-acc[aria-controls="check-g-${gid}"]`)!
const panel = (gid: string) => container.querySelector(`#check-g-${gid}`)
const subhead = (gid: string) => panel(gid)?.querySelector('.check-subhead') ?? null
const subheadText = (gid: string) => subhead(gid)?.querySelector('span')?.textContent
const reviewButton = (gid: string) => subhead(gid)!.querySelector('button')!
const count = (gid: string) => header(gid).querySelector('.check-count')!.textContent
const subtitle = (gid: string) => header(gid).querySelector('.check-acc-sub')!.textContent
const listIds = (gid: string) => [...panel(gid)!.querySelectorAll('.check-rows:not(.check-subhead ~ .check-rows) select')].map((s) => s.id.replace('check-field-', ''))
const reviewIds = (gid: string) => [...panel(gid)!.querySelectorAll('.check-subhead ~ .check-rows select')].map((s) => s.id.replace('check-field-', ''))
async function choose(key: string, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!
  await act(async () => {
    set.call(must(key), value)
    must(key).dispatchEvent(new Event('change', { bubbles: true }))
  })
}
const leave = (key: string) => act(async () => void must(key).dispatchEvent(new FocusEvent('focusout', { bubbles: true })))

const COLS = ['Inv', 'Date', 'Due', 'Note', 'Time']
const INVOICE_DATES = { invoice_number: 'Inv', issue_date: 'Date', due_date: 'Due' }

describe('map groups', () => {
  it('map_showsTheFiveGroupsUnderTheLegend', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    expect(container.querySelector('.check-groups-head .check-sub')!.textContent).toBe('Invoice fields')
    expect(container.querySelector('.check-legend')!.textContent).toBe('* Required. Leave a field on “Not in this file” if your file has no column for it.')
    expect(container.querySelector('.check-legend .check-req')!.textContent).toBe('*')
    expect([...container.querySelectorAll('.check-acc-title')].map((t) => t.textContent)).toEqual(['Invoice', 'Amounts', 'Line items', 'Buyer', 'Supplier'])
    for (const b of container.querySelectorAll('.check-acc')) {
      const open = b.getAttribute('aria-expanded') === 'true'
      expect(['true', 'false']).toContain(b.getAttribute('aria-expanded'))
      if (open) expect(container.querySelector(`#${b.getAttribute('aria-controls')}`)).not.toBeNull()
    }
    const chevrons = [...container.querySelectorAll('.check-chev path')].map((c) => c.getAttribute('d'))
    expect(chevrons.length).toBe(5)
    expect(chevrons).toEqual([GLYPHS['chevron-up'][0], GLYPHS['chevron-down'][0], GLYPHS['chevron-down'][0], GLYPHS['chevron-down'][0], GLYPHS['chevron-up'][0]])
  })

  it('map_invoiceAndSupplierOpenOnArrival', async () => {
    await toMap(suggestion(['Inv', 'Cur', 'Net', 'BName'], { invoice_number: 'Inv', currency: 'Cur', line_total: 'Net', buyer_name: 'BName' }))
    const state = ['invoice', 'amounts', 'line', 'buyer', 'supplier'].map((g) => header(g).getAttribute('aria-expanded'))
    expect(state).toEqual(['true', 'false', 'false', 'false', 'true'])
    for (const k of ['currency', 'line_total', 'buyer_name']) expect(sel(k), k).toBeNull()
    for (const g of ['amounts', 'line', 'buyer']) expect(panel(g), g).toBeNull()
    expect(panel('invoice')).not.toBeNull()
  })

  it('map_aHeaderPressTogglesItsGroup', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    await click(header('amounts'))
    expect(header('amounts').getAttribute('aria-expanded')).toBe('true')
    expect(sel('currency')).not.toBeNull()
    await click(header('amounts'))
    expect(sel('currency')).toBeNull()
    await click(header('invoice'))
    expect(header('invoice').getAttribute('aria-expanded')).toBe('false')
    expect(sel('invoice_number')).toBeNull()
  })

  it('map_anOpenGroupListsItsGapsAndInvoiceNumber', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    expect(listIds('invoice').sort()).toEqual(['invoice_kind', 'invoice_number', 'issue_time', 'payment_status', 'tax_point_date'])
    expect(must('invoice_number').required).toBe(true)
    expect(must('invoice_number').closest('.check-row')!.querySelector('.check-req')!.textContent).toBe('*')
    expect(sel('issue_date')).toBeNull()
    expect(sel('due_date')).toBeNull()
    expect(subheadText('invoice')).toBe('2 matched from your file')
    expect(reviewButton('invoice').textContent).toBe('Review')
    expect(reviewButton('invoice').getAttribute('aria-expanded')).toBe('false')
  })

  it('map_reviewShowsTheMatchedFieldsAndReadsHide', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    await click(reviewButton('invoice'))
    expect(must('issue_date').value).toBe('Date')
    expect(must('due_date').value).toBe('Due')
    expect(reviewButton('invoice').textContent).toBe('Hide')
    expect(reviewButton('invoice').getAttribute('aria-expanded')).toBe('true')
    await click(reviewButton('invoice'))
    expect(sel('issue_date')).toBeNull()
    expect(sel('due_date')).toBeNull()
  })

  it('map_aGroupWithNothingMatchedHasNoSubhead', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    expect(subhead('supplier')).toBeNull()
    expect(listIds('supplier')).toEqual(['supplier_name', 'supplier_tin'])
  })
})

describe('map lists follow the match', () => {
  it('map_aFilledRowHoldsUntilItsSelectBlurs', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    const kind = must('invoice_kind')
    await choose('invoice_kind', 'Note')
    expect(sel('invoice_kind')).toBe(kind)
    expect(listIds('invoice')).toContain('invoice_kind')
    expect(count('invoice')).toBe('4 of 7 matched')
    expect(subheadText('invoice')).toBe('2 matched from your file')
    await leave('invoice_kind')
    expect(sel('invoice_kind')).toBeNull()
    expect(subheadText('invoice')).toBe('3 matched from your file')
    await click(reviewButton('invoice'))
    expect(reviewIds('invoice')).toContain('invoice_kind')
    expect(must('invoice_kind').selectedOptions[0].textContent).toBe('D · Note')
  })

  it('map_aClearedReviewRowJoinsTheListOnBlur', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    await click(reviewButton('invoice'))
    await choose('issue_date', '')
    expect(reviewIds('invoice')).toContain('issue_date')
    expect(subheadText('invoice')).toBe('1 matched from your file')
    await leave('issue_date')
    expect(listIds('invoice')).toContain('issue_date')
    expect(reviewIds('invoice')).toEqual(['due_date'])
  })

  it('map_anotherChangeReleasesTheHold', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    await choose('invoice_kind', 'Note')
    await choose('issue_time', 'Time')
    expect(sel('invoice_kind')).toBeNull()
    expect(listIds('invoice')).toContain('issue_time')
  })

  it('map_subtitleNamesTheMatchedFields', async () => {
    await toMap(suggestion(['Inv', 'BT', 'BN', 'Cur'], { invoice_number: 'Inv', buyer_tin: 'BT', buyer_name: 'BN' }))
    expect(subtitle('buyer')).toBe('Buyer TIN, Buyer name')
    expect(count('buyer')).toBe('2 of 10 matched')
    expect(subtitle('amounts')).toBe('No columns matched yet')
    expect(count('amounts')).toBe('0 of 5 matched')
    await click(header('amounts'))
    await choose('currency', 'Cur')
    expect(subtitle('amounts')).toBe('Currency')
    expect(count('amounts')).toBe('1 of 5 matched')
  })

  it('map_aFieldOnTheSuggestionIsTagged', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    await click(reviewButton('invoice'))
    const tag = () => must('issue_date').closest('.check-row')!.querySelector('.check-tag')
    expect(tag()!.textContent).toBe('Suggested')
    expect(must('issue_date').getAttribute('aria-describedby')).toBe(tag()!.id)
    expect(subheadText('invoice')).toBe('2 matched from your file')
    await choose('issue_date', '')
    expect(tag()).toBeNull()
    expect(must('issue_date').hasAttribute('aria-describedby')).toBe(false)
    expect(subheadText('invoice')).toBe('1 matched from your file')
    await leave('issue_date')
    await choose('issue_date', 'Date')
    expect(listIds('invoice')).toContain('issue_date')
    expect(tag()!.textContent).toBe('Suggested')
    await choose('issue_date', 'Note')
    expect(tag()).toBeNull()
    expect(must('issue_date').hasAttribute('aria-describedby')).toBe(false)
  })

  it('map_invoiceNumberOnTheSuggestionIsTagged', async () => {
    await toMap(suggestion(COLS, { invoice_number: 'Inv' }), suggestion(COLS, {}))
    const tag = () => must('invoice_number').closest('.check-row')!.querySelector('.check-tag')
    expect(tag()!.textContent).toBe('Suggested')
    expect(must('invoice_number').getAttribute('aria-describedby')).toBe(tag()!.id)
    await choose('invoice_number', '')
    expect(tag()).toBeNull()
    await click([...container.querySelectorAll('button')].find((b) => b.textContent === 'Choose another file')!)
    await pickFile()
    expect(tag()).toBeNull()
    await choose('invoice_number', 'Inv')
    expect(tag()).toBeNull()
  })
})

const rows = () => [...container.querySelectorAll('.check-columns tbody tr')]
const unusedLine = () => container.querySelector('.check-unused-line')?.textContent
const statusTags = () => [...container.querySelectorAll('.check-columns .check-unused')].map((t) => t.textContent)
const fill = () => container.querySelector<HTMLElement>('.check-meter i')!
const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent === name)
const toAll = (...keys: string[]) => Object.fromEntries(keys.map((k) => [k, 'Inv']))

describe('map head and summary', () => {
  it('map_showsTheStepLabelTitleAndFile', async () => {
    await toMap(suggestion(['Inv', 'A', 'B', 'C', 'D', 'E'], { invoice_number: 'Inv' }))
    expect(container.querySelector('.check-map .check-step-no')!.textContent).toBe('02 · Match your columns')
    expect(container.querySelector('.check-map h2')!.textContent).toBe('Tell us which column is which')
    expect(container.querySelector('.check-map-name')!.textContent).toBe('a.csv')
    expect(container.querySelector('.check-map-file')!.textContent).toContain('· 6 columns')
  })

  it('map_oneColumnIsSingular', async () => {
    await toMap(suggestion(['Inv'], { invoice_number: 'Inv' }))
    expect(container.querySelector('.check-map-file')!.textContent).toContain('· 1 column')
    expect(container.querySelector('.check-map-file')!.textContent).not.toContain('columns')
  })

  it('map_summaryCountsTheMatchedFields', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    const n = CHECK_FIELD_KEYS.length
    expect(n).toBe(38)
    const text = () => container.querySelector('.check-sum-text')!.textContent
    expect(container.querySelector('.check-sum-n')!.textContent).toBe('3 of 38')
    expect(text()).toBe('We matched 3 of 38 fields to your columns. Review the gaps below.')
    expect(text()).toBe(MAP_SUMMARY(3, n))
    expect(fill().style.width).toBe('8%')
    expect(container.querySelector('.check-meter')!.getAttribute('aria-hidden')).toBe('true')
    await choose('invoice_kind', 'Note')
    expect(container.querySelector('.check-sum-n')!.textContent).toBe('4 of 38')
    expect(fill().style.width).toBe('11%')
    await choose('invoice_number', '')
    expect(container.querySelector('.check-sum-n')!.textContent).toBe('3 of 38')
  })

  it('map_nothingMatched', async () => {
    await toMap(suggestion(['Inv', 'Date', 'Net'], {}))
    expect(container.querySelector('.check-sum-n')!.textContent).toBe('0 of 38')
    expect(container.querySelector('.check-sum-text')!.textContent).toBe(MAP_SUMMARY(0, 38))
    expect(fill().style.width).toBe('0%')
    for (const g of ['amounts', 'line', 'buyer']) await click(header(g))
    for (const g of ['invoice', 'amounts', 'line', 'buyer', 'supplier']) {
      expect(subtitle(g)).toBe('No columns matched yet')
      expect(count(g)).toMatch(/^0 of \d+ matched$/)
      expect(subhead(g)).toBeNull()
    }
    expect(unusedLine()).toBe('3 of your 3 columns are not used yet: A Inv, B Date, C Net. Fields with no column stay on “Not in this file”.')
    expect(button('Continue')!.disabled).toBe(true)
    expect(container.querySelector('#check-map-need')!.textContent).toBe(NEED_INVOICE_NO)
  })

  it('map_everyFieldMatched', async () => {
    await toMap(suggestion(['Inv'], toAll(...CHECK_FIELD_KEYS)))
    expect(container.querySelector('.check-sum-n')!.textContent).toBe('38 of 38')
    expect(fill().style.width).toBe('100%')
    expect(unusedLine()).toBe('Every column in your file is matched to a field.')
    for (const g of ['amounts', 'line', 'buyer']) await click(header(g))
    expect(listIds('invoice')).toEqual(['invoice_number'])
    expect(subheadText('invoice')).toBe('6 matched from your file')
    expect(listIds('supplier')).toEqual([])
    expect(subheadText('supplier')).toBe('2 matched from your file')
    for (const g of ['invoice', 'amounts', 'line', 'buyer', 'supplier']) {
      const [x, y] = count(g).match(/^(\d+) of (\d+) matched$/)!.slice(1)
      expect(x).toBe(y)
    }
  })
})

describe('map unused line', () => {
  it('map_unusedLineNamesTheFreeColumns', async () => {
    await toMap(suggestion(['Inv', 'Date', 'Net', 'Gross', 'Buyer', 'Note'], { invoice_number: 'Inv', issue_date: 'Date', buyer_name: 'Buyer' }))
    expect(unusedLine()).toBe('3 of your 6 columns are not used yet: C Net, D Gross, F Note. Fields with no column stay on “Not in this file”.')
  })

  it('map_unusedLineSingularAndAllUsed', async () => {
    await toMap(suggestion(['Inv', 'Date', 'Note'], { invoice_number: 'Inv', issue_date: 'Date' }))
    expect(unusedLine()).toBe('1 of your 3 columns is not used yet: C Note. Fields with no column stay on “Not in this file”.')
    await choose('invoice_kind', 'Note')
    expect(unusedLine()).toBe('Every column in your file is matched to a field.')
  })

  it('map_aBlankHeaderIsNeverNamedUnused', async () => {
    await toMap(suggestion(['Inv', '', 'Date'], { invoice_number: 'Inv', issue_date: 'Date' }))
    expect(unusedLine()).toBe('Every column in your file is matched to a field.')
    expect(container.querySelector('.check-map-file')!.textContent).toContain('· 3 columns')
    expect(rows()[1].querySelector('td:nth-child(4)')!.textContent).toBe('')
    expect(statusTags()).toEqual([])
    await click(reviewButton('invoice'))
    await choose('issue_date', '')
    expect(unusedLine()).toBe('1 of your 2 columns is not used yet: C Date. Fields with no column stay on “Not in this file”.')
    expect(rows()[1].querySelector('td:nth-child(4)')!.textContent).toBe('')
  })

  it('map_noColumnOffered', async () => {
    await toMap(suggestion(['', ''], {}))
    expect(container.querySelector('.check-map')).not.toBeNull()
    expect(container.querySelector('.check-unused-line')).toBeNull()
    for (const g of ['amounts', 'line', 'buyer']) await click(header(g))
    for (const s of container.querySelectorAll<HTMLSelectElement>('.check-groups select')) expect([...s.options].map((o) => o.textContent)).toEqual(['Not in this file'])
    expect(button('Continue')!.disabled).toBe(true)
    expect(container.querySelector('#check-map-need')!.textContent).toBe(NEED_INVOICE_NO)
  })
})

describe('map columns preview', () => {
  const six = () => suggestion(['Inv', 'Date', 'Due', 'Note', 'Time', 'Net'], { invoice_number: 'Inv' })

  it('map_previewShowsFourColumnsThenAll', async () => {
    await toMap(six())
    expect(rows().length).toBe(4)
    expect(container.querySelector('.check-preview-head .check-sub')!.textContent).toBe('Your columns')
    expect(button('Show all 6 columns')!.getAttribute('aria-expanded')).toBe('false')
    await click(button('Show all 6 columns')!)
    expect(rows().length).toBe(6)
    expect(button('Show fewer')!.getAttribute('aria-expanded')).toBe('true')
    await click(button('Show fewer')!)
    expect(rows().length).toBe(4)
  })

  it('map_previewHasNoToggleAtFourColumns', async () => {
    await toMap(suggestion(['Inv', 'Date', 'Due', 'Note'], { invoice_number: 'Inv' }))
    expect(rows().length).toBe(4)
    expect(container.querySelector('.check-preview-head button')).toBeNull()
  })

  it('map_previewRowCarriesLetterHeaderExamples', async () => {
    await toMap(withRows(['Inv', 'Date'], { invoice_number: 'Inv' }, [['INV-1', 'x'], ['', 'y'], ['INV-3', 'z'], ['INV-4', 'w']]))
    expect([...rows()[0].children].slice(0, 3).map((c) => c.textContent)).toEqual(['A', 'Inv', 'INV-1 · INV-3'])
  })

  it('map_notUsedFollowsTheMapping', async () => {
    await toMap(suggestion(['Inv', 'Date', 'Note'], { invoice_number: 'Inv', issue_date: 'Date' }))
    const tag = (i: number) => rows()[i].querySelector('.check-unused')
    expect(tag(2)!.textContent).toBe('Not used')
    expect(tag(0)).toBeNull()
    await choose('invoice_kind', 'Note')
    expect(tag(2)).toBeNull()
  })

  it('map_previewIsALabelledScrollRegion', async () => {
    await toMap(six())
    const region = container.querySelector('.check-scroll')!
    expect(region.getAttribute('role')).toBe('region')
    expect(region.getAttribute('aria-label')).toBe('Preview of your columns')
    expect(region.getAttribute('tabindex')).toBe('0')
    const status = region.querySelector('th:nth-child(4) .check-sr')!
    expect(status.textContent).toBe('Status')
  })
})

describe('map action bar', () => {
  it('map_theBarHoldsTheButtonsLast', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    const bar = container.querySelector('.check-map')!.lastElementChild!
    expect(bar.classList.contains('check-sticky')).toBe(true)
    expect([...bar.querySelectorAll('button')].map((b) => b.textContent)).toEqual(['Choose another file', 'Continue'])
  })

  it('map_continueExplainsWhyItIsDisabled', async () => {
    await toMap(suggestion(COLS, { issue_date: 'Date' }))
    const cont = () => button('Continue')!
    expect(cont().disabled).toBe(true)
    expect(container.querySelector('#check-map-need')!.textContent).toBe('Choose the Invoice number column to continue.')
    expect(cont().getAttribute('aria-describedby')).toBe('check-map-need')
    await choose('invoice_number', 'Inv')
    expect(container.querySelector('#check-map-need')).toBeNull()
    expect(cont().hasAttribute('aria-describedby')).toBe(false)
    expect(cont().disabled).toBe(false)
  })

  it('map_continueReadsContinueBesideItsArrow', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    const cont = button('Continue')!
    expect(cont.querySelectorAll('svg[aria-hidden="true"]').length).toBe(1)
    expect(cont.querySelector('title')).toBeNull()
    expect(cont.textContent).toBe('Continue')
  })
})

describe('map options and hint', () => {
  const optionsOf = () => [...container.querySelectorAll<HTMLSelectElement>('.check-groups select')].map((s) => [...s.options].map((o) => [o.value, o.textContent]))

  async function openAll() {
    for (const g of ['amounts', 'line', 'buyer']) await click(header(g))
  }

  it('map_optionsAreNotInThisFileAndLetteredHeaders', async () => {
    await toMap(suggestion(['Inv', '', 'Date'], { invoice_number: 'Inv' }))
    await openAll()
    const all = optionsOf()
    expect(all.length).toBeGreaterThan(10)
    for (const o of all) expect(o).toEqual([['', 'Not in this file'], ['Inv', 'A · Inv'], ['Date', 'C · Date']])
  })

  it('map_aRepeatedHeaderIsOfferedOnce', async () => {
    await toMap(suggestion(['Amount', 'Inv', 'Amount'], { invoice_number: 'Inv', total: 'Amount' }))
    await openAll()
    for (const o of optionsOf()) expect(o).toEqual([['', 'Not in this file'], ['Amount', 'A · Amount'], ['Inv', 'B · Inv']])
    expect(unusedLine()).toBe('Every column in your file is matched to a field.')
    expect(statusTags()).toEqual([])
    await click(reviewButton('amounts'))
    await choose('total', '')
    expect(unusedLine()).toBe('1 of your 2 columns is not used yet: A Amount. Fields with no column stay on “Not in this file”.')
    expect(unusedLine()).not.toContain('C Amount')
    expect(statusTags()).toEqual(['Not used'])
    expect(rows()[2].querySelector('td:nth-child(4)')!.textContent).toBe('')
  })

  it('map_supplierGroupCarriesTheHint', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    const HINT = 'A supplier name or TIN that is not in this file fails its rule in your results.'
    expect(panel('supplier')!.textContent).toContain(HINT)
    expect(panel('supplier')!.querySelector('.check-hint svg')).not.toBeNull()
    expect(container.textContent!.split(HINT).length).toBe(2)
    await click(header('supplier'))
    expect(container.textContent).not.toContain(HINT)
  })
})

describe('map hold release', () => {
  it('map_aSecondDropDownOpensWhileTheFirstHolds', async () => {
    await toMap(suggestion(COLS, INVOICE_DATES))
    await choose('invoice_kind', 'Note')
    const first = must('invoice_kind')
    const second = must('issue_time')
    act(() => first.focus())
    const at = listIds('invoice').indexOf('issue_time')
    await act(async () => void second.dispatchEvent(new Event('pointerdown', { bubbles: true })))
    act(() => second.focus())
    expect(sel('issue_time')).toBe(second)
    expect(document.activeElement).toBe(second)
    expect(listIds('invoice').indexOf('issue_time')).toBe(at)
    expect(listIds('invoice')).toContain('invoice_kind')
    await act(async () => void document.dispatchEvent(new Event('pointerup', { bubbles: true })))
    expect(sel('issue_time')).toBe(second)
    expect(document.activeElement).toBe(second)
    expect(sel('invoice_kind')).toBeNull()
    expect(subheadText('invoice')).toBe('3 matched from your file')
  })
})

describe('rejected mapping', () => {
  const RUN = '/readiness-check/run'
  const SHEET = suggestion(['Inv', 'Seller', 'STIN', 'Net'], { invoice_number: 'Inv', supplier_name: 'Seller' })
  let run: ReturnType<typeof vi.fn<() => Response | Promise<Response>>>
  const mapRejected = () => json(400, { error: 'mapping: column "Inv" not found' })

  async function mountSheet(answer: () => Response | Promise<Response>) {
    run = vi.fn(answer)
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(async (input) => (String(input).endsWith(RUN) ? run() : json(200, SHEET))),
    )
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    await act(async () => root.render(<App />))
    await pickFile()
  }
  async function typeEmail(value: string) {
    const el = container.querySelector<HTMLInputElement>('#check-email')!
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      set.call(el, value)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  async function send() {
    await typeEmail('a@example.com')
    const box = container.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    if (!box.checked) await click(box)
    await click(button('Send')!)
    await settle()
  }
  const alertIn = () => container.querySelector('.check-sticky [role="alert"]')
  async function toRejected() {
    await mountSheet(mapRejected)
    await click(button('Continue')!)
    await send()
  }

  it('map_aRejectedMappingKeepsTheChoicesAndTheForm', async () => {
    await mountSheet(mapRejected)
    await choose('supplier_tin', 'STIN')
    await click(button('Continue')!)
    await send()
    expect(alertIn()).not.toBeNull()
    expect(header('invoice').getAttribute('aria-expanded')).toBe('true')
    expect(header('supplier').getAttribute('aria-expanded')).toBe('true')
    expect(sel('invoice_number')).not.toBeNull()
    await click(reviewButton('supplier'))
    expect(must('supplier_tin').value).toBe('STIN')
    await click(button('Continue')!)
    expect(container.querySelector<HTMLInputElement>('#check-email')!.value).toBe('a@example.com')
    expect(container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(true)
  })

  it('map_aFieldChangeClearsTheMappingAlert', async () => {
    await toRejected()
    await click(header('amounts'))
    await click(header('amounts'))
    expect(alertIn()!.textContent).toBe(MAPPING_SENTENCE)
    await choose('invoice_kind', 'Net')
    expect(container.querySelector('.check-sticky [role="alert"]')).toBeNull()
  })

  it('map_continueClearsTheMappingAlert', async () => {
    await toRejected()
    await click(button('Continue')!)
    expect(container.querySelector('[role="alert"]')).toBeNull()
    expect(container.textContent).not.toContain(MAPPING_SENTENCE)
    await click(button('Back')!)
    expect(alertIn()).toBeNull()
  })

  it('map_chooseAnotherFileClearsTheMappingAlert', async () => {
    await toRejected()
    await click(button('Choose another file')!)
    expect(container.querySelector('input[type="file"]')).not.toBeNull()
    expect(container.querySelector('#check-file-err')).toBeNull()
    expect(container.textContent).not.toContain(MAPPING_SENTENCE)
  })

  it.each([
    ['409', () => json(409, { error: 'This address has used its 3 free checks. Sign up to check every invoice.' })],
    ['429', () => json(429, { error: 'This network has run 10 checks today. Try again tomorrow.' })],
    ['422', () => json(422, { error: 'We could not read this file.' })],
    ['408', () => json(408, { error: 'The upload took too long. Try again on a faster connection.' })],
    ['413', () => json(413, { error: 'too large' })],
    ['400 email', () => json(400, { error: 'email is invalid' })],
    ['400 consent', () => json(400, { error: 'consent_text must be 1 to 500 characters' })],
    ['400 format', () => json(400, { error: 'file must be CSV, XLSX, PDF or DOCX' })],
    ['400 spreadsheet format', () => json(400, { error: 'file must be a CSV or XLSX spreadsheet' })],
    ['503', () => json(503, { error: 'unavailable' })],
    ['network', () => Promise.reject(new TypeError('Failed to fetch'))],
  ])('send_otherSpreadsheetRefusalsStayOnEmail %s', async (_name, answer) => {
    await mountSheet(answer as () => Response | Promise<Response>)
    await click(button('Continue')!)
    await send()
    expect(container.querySelector('#check-email')).not.toBeNull()
    expect(container.textContent).not.toContain('Tell us which column is which')
    expect(container.querySelector('.check-refusal')).not.toBeNull()
    expect(container.textContent).not.toContain(MAPPING_SENTENCE)
  })

  it('send_backAndSendAreLockedWhileTheRunIsInFlight', async () => {
    let release!: () => void
    await mountSheet(() => new Promise<Response>((r) => (release = () => r(mapRejected()))))
    await click(button('Continue')!)
    await send()
    expect(button('Back')!.disabled).toBe(true)
    await act(async () => void container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(run).toHaveBeenCalledTimes(1)
    await act(async () => release())
    await settle()
    expect(alertIn()!.textContent).toBe(MAPPING_SENTENCE)
  })

  it('send_aSecondRejectionReturnsToMapAgain', async () => {
    await toRejected()
    await click(button('Continue')!)
    await click(button('Send')!)
    await settle()
    expect(alertIn()!.textContent).toBe(MAPPING_SENTENCE)
    expect(run).toHaveBeenCalledTimes(2)
  })
})
