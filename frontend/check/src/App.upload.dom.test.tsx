// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/check/" }
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { DROP_HELP, DROP_TITLE, DROP_TITLE_OVER, FILE_PRIVACY, READING_TEXT, SIZE_SENTENCE, TYPE_SENTENCE, UPLOAD_STEP_NO, UPLOAD_TITLE } from './copy'
import { MAX_UPLOAD_BYTES } from './fields'
import { ARMOR_SENTENCE, COLUMNS_SENTENCE, SLOW_UPLOAD_SENTENCE, UNAVAILABLE_SENTENCE, UNREADABLE_SENTENCE } from './messages'

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

const MAPPING = '/readiness-check/mapping'
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let container: HTMLDivElement
let root: Root
let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>

function stubFetch(answers: Record<string, () => Response | Promise<Response>>) {
  fetchMock = vi.fn<typeof fetch>(async (input) => {
    const route = Object.keys(answers).find((r) => String(input).endsWith(r))
    if (!route) throw new Error(`unexpected request ${String(input)}`)
    return answers[route]()
  })
  vi.stubGlobal('fetch', fetchMock)
}
async function mount() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App />))
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
const text = () => container.querySelector('main')?.textContent ?? ''
const q = <T extends Element>(sel: string) => container.querySelector<T>(sel)
const zone = () => q<HTMLElement>('.check-drop')!
const input = () => q<HTMLInputElement>('input[type="file"]')!
const slot = () => q<HTMLElement>('#check-file-err')
const file = (name: string, size = 4) => new File(['a'.repeat(size)], name)
const held = () => {
  let release!: (r: Response) => void
  stubFetch({ [MAPPING]: () => new Promise<Response>((r) => (release = r)) })
  return (r: Response) => act(async () => release(r))
}

async function pick(f: File) {
  Object.defineProperty(input(), 'files', { value: [f], configurable: true })
  await act(async () => void input().dispatchEvent(new Event('change', { bubbles: true })))
  await settle()
}
// jsdom has no DragEvent; a plain Event carries the dataTransfer React reads.
function fire(target: Element, type: string, files: File[] = [], types: string[] = ['Files']) {
  const e = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(e, 'dataTransfer', { value: { types, files } })
  act(() => void target.dispatchEvent(e))
  return e
}
async function drop(files: File[]) {
  fire(zone(), 'drop', files)
  await settle()
}

describe('upload step', () => {
  it('upload_showsTheStepNumberAndTitle', async () => {
    stubFetch({})
    await mount()
    expect(q('.check-step-no')!.textContent).toBe(UPLOAD_STEP_NO)
    expect(UPLOAD_STEP_NO).toBe('01 · Upload')
    expect(q('h2.check-step-title')!.textContent).toBe(UPLOAD_TITLE)
    expect(UPLOAD_TITLE).toBe('Add one invoice file')
  })

  it('upload_zoneIsALabelAroundTheInput', async () => {
    stubFetch({})
    await mount()
    expect(q('label[for="check-file"]')!.textContent).toBe('Invoice file')
    expect(input().id).toBe('check-file')
    expect(input().closest('label.check-drop')).not.toBeNull()
    expect(input().getAttribute('accept')).toBe('.csv,.xlsx,.pdf,.docx')
    expect(input().multiple).toBe(false)
  })

  it('upload_zoneCarriesItsTextAndChips', async () => {
    stubFetch({})
    await mount()
    const z = zone().textContent!
    expect(z).toContain('Drag your invoice file here')
    expect(z).toContain('or')
    expect(z).toContain('Choose a file')
    expect([...zone().querySelectorAll('.check-chip')].map((c) => c.textContent)).toEqual(['CSV', 'XLSX', 'PDF', 'DOCX'])
    expect(DROP_HELP).toBe('One file, up to 15 MiB. We check its first 2 invoices.')
    expect(z).toContain(DROP_HELP)
    expect(q('.check-fake-btn')!.getAttribute('aria-hidden')).toBe('true')
  })

  it('upload_dropTakesTheFile', async () => {
    stubFetch({})
    await mount()
    await drop([file('a.pdf')])
    expect(q('input[type="email"]')).not.toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('upload_dropOfSeveralFilesTakesTheFirst', async () => {
    stubFetch({})
    await mount()
    await drop([file('b.pdf'), file('a.txt')])
    expect(q('input[type="email"]')).not.toBeNull()
    expect(text()).toContain('b.pdf')
    expect(text()).not.toContain(TYPE_SENTENCE)
  })

  it('upload_dropWithNoFileDoesNothing', async () => {
    stubFetch({})
    await mount()
    await drop([])
    expect(zone()).not.toBeNull()
    expect(slot()).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('upload_dragAndDropPreventTheBrowserDefault', async () => {
    stubFetch({})
    await mount()
    for (const type of ['dragenter', 'dragover', 'drop']) expect(fire(zone(), type).defaultPrevented, type).toBe(true)
  })

  it('upload_dragOverTurnsTheZoneMint', async () => {
    stubFetch({})
    await mount()
    fire(zone(), 'dragenter')
    expect(zone().classList.contains('is-drag')).toBe(true)
    expect(zone().textContent).toContain(DROP_TITLE_OVER)
    expect(zone().textContent).not.toContain(DROP_TITLE)
    fire(zone(), 'dragleave')
    expect(zone().classList.contains('is-drag')).toBe(false)
    expect(zone().textContent).toContain(DROP_TITLE)
  })

  it('upload_dragAcrossChildrenKeepsTheState', async () => {
    stubFetch({})
    await mount()
    fire(zone(), 'dragenter')
    fire(q('.check-drop-title')!, 'dragenter')
    fire(zone(), 'dragleave')
    expect(zone().classList.contains('is-drag')).toBe(true)
    fire(q('.check-drop-title')!, 'dragleave')
    expect(zone().classList.contains('is-drag')).toBe(false)
  })

  it('upload_dropClearsTheDragState', async () => {
    stubFetch({})
    await mount()
    fire(zone(), 'dragenter')
    await drop([file('notes.txt')])
    expect(zone().classList.contains('is-err')).toBe(true)
    expect(zone().classList.contains('is-drag')).toBe(false)
  })

  it('upload_aWrongTypeShowsTheSentenceUnderTheZone', async () => {
    stubFetch({})
    await mount()
    await pick(file('notes.txt'))
    expect(slot()!.textContent).toBe(TYPE_SENTENCE)
    expect(slot()!.getAttribute('role')).toBe('alert')
    expect(zone().nextElementSibling).toBe(slot())
    expect(zone().classList.contains('is-err')).toBe(true)
    expect(input().getAttribute('aria-describedby')).toContain('check-file-err')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('upload_anOversizedFileShowsTheSizeSentence', async () => {
    stubFetch({})
    await mount()
    await pick(file('a.csv', MAX_UPLOAD_BYTES + 1))
    expect(slot()!.textContent).toBe(SIZE_SENTENCE)
    await pick(file('a.pdf', MAX_UPLOAD_BYTES))
    expect(slot()).toBeNull()
  })

  it('upload_aRefusedReadShowsItsSentenceInTheSameSlot', async () => {
    const cases: [Response, string][] = [
      [json(422, { error: UNREADABLE_SENTENCE }), UNREADABLE_SENTENCE],
      [json(408, {}), SLOW_UPLOAD_SENTENCE],
      [json(429, {}), ARMOR_SENTENCE],
      [json(503, {}), UNAVAILABLE_SENTENCE],
      [json(200, { columns: [], sample_rows: [], header_row: 1, rows_total: 0, mapping: {}, source: 'heuristic' }), COLUMNS_SENTENCE],
    ]
    for (const [res, sentence] of cases) {
      stubFetch({ [MAPPING]: () => res.clone() })
      await mount()
      await pick(file('a.csv'))
      expect(slot()!.textContent, sentence).toBe(sentence)
      expect(zone().classList.contains('is-err')).toBe(true)
      expect(input().disabled).toBe(false)
      await act(async () => root.unmount())
      container.remove()
    }
    await mount()
  })

  it('upload_aNewPickClearsTheErrorAndTheRedEdge', async () => {
    stubFetch({})
    await mount()
    await pick(file('notes.txt'))
    await pick(file('a.pdf'))
    expect(slot()).toBeNull()
    expect(q('.is-err')).toBeNull()
  })

  it('upload_readingShowsTheFileRowAndBar', async () => {
    held()
    await mount()
    await pick(file('a.csv', 188416))
    const row = q('.check-upload-file')!
    expect(row.textContent).toContain('a.csv')
    expect(row.textContent).toContain('184 KB')
    expect(q('.check-spinner')).not.toBeNull()
    expect(q('.check-bar')).not.toBeNull()
    expect(q('[role="status"]')!.textContent).toBe(READING_TEXT)
    expect(q('.check-drop')).toBeNull()
    expect(q('input[type="file"]')).toBeNull()
  })

  it('upload_theZoneReturnsAfterARefusedRead', async () => {
    const answer = held()
    await mount()
    await pick(file('a.csv'))
    await answer(json(422, { error: UNREADABLE_SENTENCE }))
    await settle()
    expect(zone().classList.contains('is-err')).toBe(true)
    expect(q('.check-upload-file')).toBeNull()
    expect(slot()!.textContent).toBe(UNREADABLE_SENTENCE)
  })

  it('upload_aDraggedTextDoesNotTurnTheZoneMint', async () => {
    stubFetch({})
    await mount()
    const e = fire(zone(), 'dragenter', [], ['text/plain'])
    expect(zone().classList.contains('is-drag')).toBe(false)
    expect(zone().textContent).toContain(DROP_TITLE)
    expect(e.defaultPrevented).toBe(false)
    fire(zone(), 'dragenter')
    fire(zone(), 'dragleave')
    expect(zone().classList.contains('is-drag')).toBe(false)
  })

  it('upload_aStrayFileDropBesideTheZoneIsPrevented', async () => {
    stubFetch({})
    await mount()
    expect(fire(document.body, 'drop', [file('a.pdf')]).defaultPrevented).toBe(true)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(slot()).toBeNull()
    expect(fire(document.body, 'drop', [], ['text/plain']).defaultPrevented).toBe(false)
  })

  it('upload_aStrayFileDropDuringTheReadIsPrevented', async () => {
    held()
    await mount()
    await pick(file('a.csv'))
    expect(fire(document.body, 'drop', [file('b.csv')]).defaultPrevented).toBe(true)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('upload_theDropGuardEndsWithTheStep', async () => {
    stubFetch({})
    await mount()
    await pick(file('a.pdf'))
    expect(fire(document.body, 'drop', [file('b.pdf')]).defaultPrevented).toBe(false)
  })

  it('upload_theNoteShowsInEveryState', async () => {
    const answer = held()
    await mount()
    expect(text()).toContain(FILE_PRIVACY)
    await pick(file('notes.txt'))
    expect(text()).toContain(FILE_PRIVACY)
    await pick(file('a.csv'))
    expect(q('.check-upload-file')).not.toBeNull()
    expect(text()).toContain(FILE_PRIVACY)
    await answer(json(422, {}))
  })
})
