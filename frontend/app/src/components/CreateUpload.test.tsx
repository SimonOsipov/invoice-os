// @vitest-environment jsdom
// The picker must not contradict itself. EXTR-09-04 widened `accept` and the ACCEPTED
// copy to every accepted type but left three selection-time gates reading
// hasImportableExtension, so a picked PDF was listed as "Unsupported file type" one line
// under copy saying PDF is accepted. Authored RED against that state; EXTR-09-07 ended it.
//
// Both halves are asserted every time: a PDF must produce NO unsupported note, no invalid
// dropzone and a live primary, while a genuinely unlisted type (.zip) must still produce
// all three. Without the .zip half these specs pass on a component that simply never
// complains about anything.
//
// Drag-and-drop bypasses `accept` entirely (CreateUpload.tsx hands dropped files to
// addPickedFiles unfiltered), so the dropped path is exercised too, not only the picked
// one — that is where a stale predicate would survive unnoticed.
import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { PickedFile } from '../lib/importRun'
import type { PlatformCtx } from '../types'
import { capRefusal } from '../lib/importRun'
import { MAX_UPLOAD_BYTES } from '../lib/importFlow'
import { AMBER_COPY, CreateUpload } from './CreateUpload'

const UNSUPPORTED_NOTE = /Unsupported file type/i

// The dropzone's invalid cue is an inline border painted with --status-red-border. Read as
// raw attribute text: jsdom's cssstyle does not resolve a var() inside a border shorthand,
// so `label.style.border` is empty here while the attribute is not.
const INVALID_BORDER = 'status-red-border'

function picked(name: string, type: string): PickedFile {
  return { id: `pf-${name}`, file: new File([], name, { type }), documentId: null }
}

// Handler surface enumerated by grepping `ctx\.` in CreateUpload.tsx. Nothing here mounts
// an effect or fetches, so no network mock is needed.
function uploadCtx(pickedFiles: PickedFile[], addPickedFiles = vi.fn()): PlatformCtx {
  const ctx = {
    active: { short: 'Lagos Freight', tin: '20184412-0001' },
    pickedFiles,
    filesRefusal: null,
    importError: null,
    // A resolved entity, so the amber no-entity panel never renders and cannot be what a
    // selector below is matching.
    activeEntity: { id: 'e1', name: 'Lagos Freight', tin: '20184412-0001' },
    entitiesState: 'ready',
    entities: [{ id: 'e1' }],
    clients: [{ id: 'e1' }],
    mode: 'firm',
    runKind: null,
    addPickedFiles,
    removePickedFile: () => {},
    setSettingsTab: () => {},
    nav: () => {},
    readAllColumns: () => {},
    skipUpload: () => {},
  }
  return ctx as unknown as PlatformCtx
}

function surface(container: HTMLElement) {
  const label = container.querySelector('label[for="pf-import-file"]')
  const primary = container.querySelector('button.v2-btn-primary')
  return {
    note: container.textContent ?? '',
    dropzoneStyle: label?.getAttribute('style') ?? '',
    primaryDisabled: (primary as HTMLButtonElement | null)?.disabled ?? null,
    label,
  }
}

describe('CreateUpload — the picker no longer contradicts its own ACCEPTED copy (EXTR-09-07)', () => {
  beforeEach(() => {
    // Without a gateway the primary is disabled whatever the file gate says, which would
    // make the enabled-primary assertion below unfalsifiable.
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
  })

  afterEach(() => {
    vi.unstubAllEnvs()
    cleanup()
  })

  it('PICKER-FB-1: a picked PDF is not called unsupported, does not redden the dropzone, and leaves the primary live', () => {
    const { container } = render(<CreateUpload ctx={uploadCtx([picked('scan.pdf', 'application/pdf')])} />)
    const s = surface(container)

    // The file really is on screen, so the absences below are absences from a rendered
    // list rather than from an empty component.
    expect(s.note).toContain('scan.pdf')
    expect(s.primaryDisabled).not.toBeNull()

    expect(s.note).not.toMatch(UNSUPPORTED_NOTE)
    expect(s.dropzoneStyle).not.toContain(INVALID_BORDER)
    expect(s.primaryDisabled).toBe(false)
  })

  it('PICKER-FB-2: a picked .zip is still called unsupported, still reddens the dropzone and still blocks the primary', () => {
    const { container } = render(<CreateUpload ctx={uploadCtx([picked('archive.zip', 'application/zip')])} />)
    const s = surface(container)

    expect(s.note).toContain('archive.zip')
    expect(s.note).toMatch(UNSUPPORTED_NOTE)
    expect(s.dropzoneStyle).toContain(INVALID_BORDER)
    expect(s.primaryDisabled).toBe(true)
  })

  it('PICKER-FB-3: every accepted document type reads as accepted, and .csv/.xlsx are unchanged', () => {
    // The whole document half of ACCEPTED_PICKED_TYPES, plus the two spreadsheet types as
    // the AC-1 control — a fix that special-cases only .pdf fails here.
    const CASES: readonly [string, string][] = [
      ['scan.pdf', 'application/pdf'],
      ['scan.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
      ['ledger.csv', 'text/csv'],
      ['ledger.xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'],
    ]
    expect(CASES).toHaveLength(4)

    for (const [name, type] of CASES) {
      const { container, unmount } = render(<CreateUpload ctx={uploadCtx([picked(name, type)])} />)
      const s = surface(container)
      expect(s.note, name).toContain(name)
      expect(s.note, name).not.toMatch(UNSUPPORTED_NOTE)
      expect(s.dropzoneStyle, name).not.toContain(INVALID_BORDER)
      expect(s.primaryDisabled, name).toBe(false)
      unmount()
    }
  })

  // PN (EXTR-15-03 AC #7/#12): the four types PICKER-FB-3 no longer lists are RETARGETED here,
  // not deleted. They must read exactly like the .zip of PICKER-FB-2 — an unsupported note, a
  // reddened dropzone and a dead primary — because a dropped file bypasses `accept` entirely.
  it('PICKER-FB-3b: a picked image is now unsupported, reddens the dropzone and blocks the primary', () => {
    const NARROWED_OUT: readonly [string, string][] = [
      ['scan.png', 'image/png'],
      ['scan.jpg', 'image/jpeg'],
      ['scan.jpeg', 'image/jpeg'],
      ['scan.webp', 'image/webp'],
    ]
    expect(NARROWED_OUT).toHaveLength(4)

    for (const [name, type] of NARROWED_OUT) {
      const { container, unmount } = render(<CreateUpload ctx={uploadCtx([picked(name, type)])} />)
      const s = surface(container)
      // The file is really on screen, so the refusal below is a refusal of a rendered row.
      expect(s.note, name).toContain(name)
      expect(s.note, name).toMatch(UNSUPPORTED_NOTE)
      expect(s.dropzoneStyle, name).toContain(INVALID_BORDER)
      expect(s.primaryDisabled, name).toBe(true)
      unmount()
    }
  })

  it('PICKER-FB-4: a DROPPED pdf reaches addPickedFiles unfiltered and its feedback is clean too', () => {
    // `accept` gates the file INPUT only. onDrop hands dataTransfer.files straight to
    // addPickedFiles, so a stale predicate on the dropped path is invisible to any spec
    // that only ever exercises the picker.
    const addPickedFiles = vi.fn()
    const dropped = new File([], 'dropped.pdf', { type: 'application/pdf' })
    const first = render(<CreateUpload ctx={uploadCtx([], addPickedFiles)} />)
    const label = first.container.querySelector('label[for="pf-import-file"]')
    expect(label).not.toBeNull()

    fireEvent.drop(label as Element, { dataTransfer: { files: [dropped] } })

    expect(addPickedFiles).toHaveBeenCalledTimes(1)
    expect(addPickedFiles.mock.calls[0][0].map((f: File) => f.name)).toEqual(['dropped.pdf'])
    first.unmount()

    // The selection the drop produced, rendered: the same three gates, same verdict.
    const { container } = render(<CreateUpload ctx={uploadCtx([{ id: 'pf-dropped', file: dropped, documentId: null }])} />)
    const s = surface(container)
    expect(s.note).toContain('dropped.pdf')
    expect(s.note).not.toMatch(UNSUPPORTED_NOTE)
    expect(s.dropzoneStyle).not.toContain(INVALID_BORDER)
    expect(s.primaryDisabled).toBe(false)
  })

  it('PICKER-FB-5: a DROPPED .zip is still accepted into the list and still flagged there', () => {
    // The control half of PICKER-FB-4: a dropped unlisted type must not be silently
    // swallowed (the user has to see and remove it) and must not read as accepted.
    const addPickedFiles = vi.fn()
    const dropped = new File([], 'dropped.zip', { type: 'application/zip' })
    const first = render(<CreateUpload ctx={uploadCtx([], addPickedFiles)} />)
    fireEvent.drop(first.container.querySelector('label[for="pf-import-file"]') as Element, {
      dataTransfer: { files: [dropped] },
    })
    expect(addPickedFiles.mock.calls[0][0].map((f: File) => f.name)).toEqual(['dropped.zip'])
    first.unmount()

    const { container } = render(<CreateUpload ctx={uploadCtx([{ id: 'pf-dropped', file: dropped, documentId: null }])} />)
    const s = surface(container)
    expect(s.note).toContain('dropped.zip')
    expect(s.note).toMatch(UNSUPPORTED_NOTE)
    expect(s.dropzoneStyle).toContain(INVALID_BORDER)
    expect(s.primaryDisabled).toBe(true)
  })

  it('PICKER-FB-6: a mixed selection is flagged on the .zip alone, never on the pdf beside it', () => {
    const { container } = render(
      <CreateUpload ctx={uploadCtx([picked('scan.pdf', 'application/pdf'), picked('archive.zip', 'application/zip')])} />,
    )
    const s = surface(container)

    expect(s.note).toContain('scan.pdf')
    expect(s.note).toContain('archive.zip')
    // Exactly one file is called unsupported, and the dropzone does redden — one bad file
    // blocks the run, which is the shipped aggregate rule (BULK-03-9), unchanged.
    expect(container.querySelectorAll('p')).not.toHaveLength(0)
    const notes = Array.from(container.querySelectorAll('p')).filter((p) => UNSUPPORTED_NOTE.test(p.textContent ?? ''))
    expect(notes).toHaveLength(1)
    expect(s.dropzoneStyle).toContain(INVALID_BORDER)
    expect(s.primaryDisabled).toBe(true)
  })
})

// QA (ROUTE-04-03). The no-entity button moved from the two-call idiom
// (`ctx.setSettingsTab('company')` then `ctx.nav('settings')`) to one `ctx.nav('settings',
// { settingsTab: 'company' })`, and nothing covered either branch of it.
function noEntityCtx(mode: 'firm' | 'inhouse', nav: () => void, setSettingsTab: () => void): PlatformCtx {
  return {
    active: { short: 'Lagos Freight', tin: '20184412-0001' },
    pickedFiles: [],
    filesRefusal: null,
    importError: null,
    // computeNoEntity: no active entity, the fetch settled, the roster is not catching up.
    activeEntity: null,
    entitiesState: 'ready',
    entities: [],
    clients: [],
    mode,
    runKind: null,
    addPickedFiles: vi.fn(),
    removePickedFile: () => {},
    setSettingsTab,
    nav,
    readAllColumns: () => {},
    skipUpload: () => {},
  } as unknown as PlatformCtx
}

// The amber panel's button is the one control whose label ends in an arrow.
function amberButton(container: HTMLElement): HTMLButtonElement {
  const buttons = Array.from(container.querySelectorAll('button')).filter((b) => (b.textContent ?? '').endsWith('→'))
  expect(buttons, 'the no-entity panel did not render its button').toHaveLength(1)
  return buttons[0] as HTMLButtonElement
}

function amberPanel(container: HTMLElement): HTMLElement {
  return amberButton(container).parentElement as HTMLElement
}

// The footnote is the last <p> on the step; it renders only alongside the panel.
function amberFootnote(container: HTMLElement): string {
  const ps = container.querySelectorAll('p')
  expect(ps.length, 'the footnote did not render').toBeGreaterThan(1)
  return ps[ps.length - 1].textContent ?? ''
}

// Literal on purpose: a copy constant alone would let drift pass (6 [c']).
// Apostrophe is ASCII, exactly as Design § Copy writes it.
const BODY =
  "Invoices are filed for a registered company, and this workspace has none yet. Reading a file's columns still works \u2014 filing waits until the company is added."
const FOOTNOTE =
  'Manual entry has the same requirement \u2014 an invoice is filed for a registered company too.'

describe('CreateUpload — the no-entity button names its destination tab in the nav call (ROUTE-04-03)', () => {
  afterEach(cleanup)

  it('theInHouseBranchNavigatesToSettingsCarryingTheCompanyTab', () => {
    const nav = vi.fn()
    const setSettingsTab = vi.fn()
    const { container } = render(<CreateUpload ctx={noEntityCtx('inhouse', nav, setSettingsTab)} />)

    expect(amberPanel(container).firstElementChild?.textContent).toBe('Add your company before you file')
    expect(amberButton(container).textContent).toBe('Add your company →')
    fireEvent.click(amberButton(container))

    expect(nav.mock.calls, 'the tab must arrive with the destination, not before it').toEqual([
      ['settings', { settingsTab: 'company' }],
    ])
    // The old idiom set the tab first and navigated after. Two writes cannot both be the
    // URL, so the second one has to be gone.
    expect(setSettingsTab, 'the separate tab write must be gone').not.toHaveBeenCalled()
  })

  it('theFirmBranchNavigatesToClientsAndNamesNoTab', () => {
    const nav = vi.fn()
    const setSettingsTab = vi.fn()
    const { container } = render(<CreateUpload ctx={noEntityCtx('firm', nav, setSettingsTab)} />)

    expect(amberPanel(container).firstElementChild?.textContent).toBe('Add a client before you file')
    expect(amberButton(container).textContent).toBe('Add a client →')
    fireEvent.click(amberButton(container))

    // The asymmetry is the point: Clients owns no param, so the firm branch passes none.
    expect(nav.mock.calls).toEqual([['clients']])
    expect(setSettingsTab).not.toHaveBeenCalled()
  })
})

describe('CreateUpload — the amber panel copy (AUTH-10-04)', () => {
  afterEach(cleanup)

  it.each(['inhouse', 'firm'] as const)('the panel names the task, not a missing link (%s)', (mode) => {
    const { container } = render(<CreateUpload ctx={noEntityCtx(mode, vi.fn(), vi.fn())} />)
    const panel = amberPanel(container)
    // Positive first: an empty panel would pass every negative below.
    expect(panel.textContent ?? '').not.toBe('')
    const text = `${panel.textContent} ${amberFootnote(container)}`
    for (const gone of ['No linked business entity', 'Link a business entity', 'has none, so']) {
      expect(text).not.toContain(gone)
    }
    // The placeholder name ('Lagos Freight' in this ctx) is no longer interpolated.
    expect(text).not.toContain('Lagos Freight')
  })

  it.each(['inhouse', 'firm'] as const)('the panel says exactly what the task is (%s)', (mode) => {
    const { container } = render(<CreateUpload ctx={noEntityCtx(mode, vi.fn(), vi.fn())} />)
    const body = amberPanel(container).querySelector('p')
    expect(body, 'the panel body did not render').not.toBeNull()
    expect(body?.textContent).toBe(BODY)
    expect(amberFootnote(container)).toBe(FOOTNOTE)
    // The panel renders from the constant, and the constant is the literal.
    expect(AMBER_COPY.body).toBe(BODY)
    expect(AMBER_COPY.footnote).toBe(FOOTNOTE)
    expect(amberPanel(container).firstElementChild?.textContent).toBe(AMBER_COPY.title[mode])
    expect(amberButton(container).textContent).toBe(AMBER_COPY.button[mode])
  })

  it.each(['inhouse', 'firm'] as const)('a workspace with an entity shows no amber panel (%s)', (mode) => {
    const ctx = { ...noEntityCtx(mode, vi.fn(), vi.fn()), activeEntity: { id: 'e1' }, entities: [{ id: 'e1' }], clients: [{}] } as unknown as PlatformCtx
    const { container } = render(<CreateUpload ctx={ctx} />)
    expect(container.textContent).toContain('Skip — enter manually')
    expect(container.textContent).not.toContain(AMBER_COPY.title[mode])
    expect(container.textContent).not.toContain(AMBER_COPY.body)
    expect(container.textContent).not.toContain(AMBER_COPY.footnote)
  })
})

// RESKIN2-04-01: the v2 look of the upload card. Inline styles are the oracle; jsdom
// resolves no stylesheet, so a class-driven value would not show here.
describe('CreateUpload — the v2 card, accepted line and primary (RESKIN2-04-01)', () => {
  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
  })
  afterEach(() => {
    vi.unstubAllEnvs()
    cleanup()
  })

  function extractButton(container: HTMLElement): HTMLButtonElement {
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button.v2-btn-primary'))
    expect(buttons, 'the primary did not render').toHaveLength(1)
    expect(buttons[0].textContent).toContain('Extract invoices')
    return buttons[0]
  }

  function noteP(container: HTMLElement, text: RegExp): HTMLElement {
    const ps = Array.from(container.querySelectorAll<HTMLElement>('p')).filter((p) => text.test(p.textContent ?? ''))
    expect(ps, `no paragraph matched ${text}`).toHaveLength(1)
    return ps[0]
  }

  it('the upload card header follows the prototype', () => {
    const { container } = render(<CreateUpload ctx={uploadCtx([])} />)
    const titles = container.querySelectorAll<HTMLElement>('.card-title')
    expect(titles).toHaveLength(1)
    expect(titles[0].textContent).toContain('Import invoices')
    expect(titles[0].style.fontSize).toBe('15px')
    expect((titles[0].parentElement as HTMLElement).style.gap).toBe('12px')
  })

  it('the accepted-types line is enabled text', () => {
    const { container } = render(<CreateUpload ctx={uploadCtx([])} />)
    const spans = Array.from(container.querySelectorAll<HTMLElement>('span')).filter(
      (s) => (s.textContent ?? '').trim() === 'ACCEPTED · CSV · XLSX · PDF · DOCX',
    )
    expect(spans).toHaveLength(1)
    expect(spans[0].style.color).toBe('var(--fg-3)')
  })

  it('Extract invoices wears the v2 primary', () => {
    const { container } = render(<CreateUpload ctx={uploadCtx([picked('scan.pdf', 'application/pdf')])} />)
    const b = extractButton(container)
    expect(b.disabled).toBe(false)
    expect(b.style.background).toBe('var(--action)')
    expect(b.style.color).toBe('var(--primary-foreground)')
    expect(b.style.opacity).toBe('')
    expect(b.style.filter).toBe('')
  })

  it('a refused file dims Extract invoices (#114)', () => {
    // pdf first fixes the run kind, so the jpg is the refused one and the label stays Extract.
    const ctx = uploadCtx([picked('scan.pdf', 'application/pdf'), picked('photo.jpg', 'image/jpeg')])
    const { container } = render(<CreateUpload ctx={ctx} />)
    const b = extractButton(container)
    expect(b.disabled).toBe(true)
    expect(b.style.background).toBe('var(--action)')
    expect(b.style.opacity).toBe('0.45')
    expect(b.style.cursor).toBe('not-allowed')
    expect(b.style.filter).toBe('none')
    const note = noteP(container, UNSUPPORTED_NOTE)
    expect(note.style.fontSize).toBe('11.5px')
    expect(note.style.color).toBe('var(--status-red-text)')
  })

  it('with no gateway base a ready spreadsheet run is disabled and dimmed', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const { container } = render(<CreateUpload ctx={uploadCtx([picked('ledger.csv', 'text/csv')])} />)
    const b = container.querySelector('button.v2-btn-primary') as HTMLButtonElement
    expect(b, 'the primary did not render').not.toBeNull()
    expect(b.textContent).toContain('Read columns')
    expect(b.disabled).toBe(true)
    expect(b.style.opacity).toBe('0.45')
    expect(b.style.cursor).toBe('not-allowed')
  })

  it('the oversize note keeps the red note recipe', () => {
    const big = new File([], 'big.csv', { type: 'text/csv' })
    Object.defineProperty(big, 'size', { value: 16 * 1024 * 1024 })
    expect(big.size).toBeGreaterThan(MAX_UPLOAD_BYTES)
    const { container } = render(<CreateUpload ctx={uploadCtx([{ id: 'pf-big', file: big, documentId: null }])} />)
    const note = noteP(container, /over the/)
    expect(note.style.fontSize).toBe('11.5px')
    expect(note.style.color).toBe('var(--status-red-text)')
  })

  it('six files: the cap refusal keeps the amber recipe', () => {
    const five = ['a', 'b', 'c', 'd', 'e'].map((n) => picked(`${n}.pdf`, 'application/pdf'))
    const ctx = { ...uploadCtx(five), filesRefusal: capRefusal(1) } as unknown as PlatformCtx
    const { container } = render(<CreateUpload ctx={ctx} />)
    const note = noteP(container, /A run accepts at most/)
    expect(note.style.fontSize).toBe('12.5px')
    expect(note.style.color).toBe('var(--status-amber-text)')
  })

  it('the no-company upload keeps its amber panel and dims Extract', () => {
    // One case with a PDF (label Extract invoices), one with no file (label Read columns).
    for (const files of [[picked('scan.pdf', 'application/pdf')], []]) {
      const ctx = { ...noEntityCtx('firm', vi.fn(), vi.fn()), pickedFiles: files } as unknown as PlatformCtx
      const { container, unmount } = render(<CreateUpload ctx={ctx} />)
      const panel = amberPanel(container)
      expect((panel.firstElementChild as HTMLElement).textContent).toBe('Add a client before you file')
      expect(panel.style.background).toBe('var(--status-amber-bg)')
      expect(panel.style.border).toBe('1px solid var(--status-amber-border)')
      const b = container.querySelector('button.v2-btn-primary') as HTMLButtonElement
      expect(b, 'the primary did not render').not.toBeNull()
      expect(b.disabled).toBe(true)
      expect(b.style.opacity, `files=${files.length}`).toBe('0.45')
      expect(b.style.cursor, `files=${files.length}`).toBe('not-allowed')
      expect(b.style.filter, `files=${files.length}`).toBe('none')
      expect(b.style.background, `files=${files.length}`).toBe('var(--action)')
      unmount()
    }
  })

  it('Read columns wears the same primary: live for a spreadsheet, dimmed with no file or a mixed run', () => {
    const button = (files: PickedFile[]) => {
      const { container } = render(<CreateUpload ctx={uploadCtx(files)} />)
      const b = container.querySelector('button.v2-btn-primary') as HTMLButtonElement
      expect(b, 'the primary did not render').not.toBeNull()
      expect(b.textContent).toContain('Read columns')
      return b
    }

    const live = button([picked('ledger.csv', 'text/csv')])
    expect(live.disabled).toBe(false)
    expect(live.style.background).toBe('var(--action)')
    expect(live.style.color).toBe('var(--primary-foreground)')
    expect(live.style.opacity).toBe('')
    expect(live.style.filter).toBe('')
    cleanup()

    // csv first fixes the run kind, so the pdf is the refused file.
    const mixed = button([picked('ledger.csv', 'text/csv'), picked('scan.pdf', 'application/pdf')])
    cleanup()
    const none = button([])
    for (const b of [mixed, none]) {
      expect(b.disabled).toBe(true)
      expect(b.style.background).toBe('var(--action)')
      expect(b.style.opacity).toBe('0.45')
      expect(b.style.cursor).toBe('not-allowed')
      expect(b.style.filter).toBe('none')
    }
  })
})
