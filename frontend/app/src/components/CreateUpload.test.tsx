// @vitest-environment jsdom
// The picker must not contradict itself. EXTR-09-04 widened `accept` and the ACCEPTED
// copy to all seven types but left three selection-time gates reading
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
import { CreateUpload } from './CreateUpload'

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
