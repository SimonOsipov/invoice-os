// @vitest-environment jsdom
// task-786 (EXTR-10-04) QA, Mode B adversarial coverage -- Core AC 6, "the stage map is
// empty at the start of every run". CARD-1..8 (ImportProgress.test.tsx) and STAGE-3/4/ADV-4
// (documentRun.test.ts) all pin documentRunRows' JOIN, never the RESET: nothing proved
// `setDocumentStages({})` (App.tsx, startDocumentRun) actually fires. It matters because the
// one call site (CreateUpload.tsx:315) is reachable a second time on the SAME `pickedFiles` --
// ImportProgress.tsx's own header comment says a whole-run failure routes back to the step
// router with the run intact, not through resetImport -- so a second click reuses the same
// crypto.randomUUID() file ids `documentRunRows` joins on. Two runs with DIFFERENT ids could
// never collide regardless of the reset; this is the one path where they don't.
//
// Harness is App.auditPrefilter.test.tsx's ("the App -> AuditView seam"): the real <App/>, a
// stubbed session/localStorage, a stubbed gateway, and ctx captured through a mocked Sidebar.
// The upload leg is XHR, not fetch (importApi.ts's uploadSourceDocument/xhrJson), so a local
// FakeXhr drives it deterministically -- same idiom as importApi.test.ts's FakeXhr, duplicated
// rather than imported (ImportProgress.test.tsx's own stated reason: independently owned test
// fixtures).

import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import type { DocumentRowState } from './lib/documentRun'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

// Minimal fake: only what xhrJson (importApi.ts) touches for one POST /documents round
// trip. `fireError()` is the only driver this file needs -- run 1 only has to leave a
// stage entry behind, not actually succeed.
class FakeXhr {
  static instances: FakeXhr[] = []
  static last(): FakeXhr | undefined {
    return FakeXhr.instances[FakeXhr.instances.length - 1]
  }

  status = 0
  statusText = ''
  responseText = ''
  upload: { onprogress: (() => void) | null; onload: (() => void) | null } = { onprogress: null, onload: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null

  constructor() {
    FakeXhr.instances.push(this)
  }

  open(): void {}
  setRequestHeader(): void {}
  send(): void {}

  fireError(): void {
    this.onerror?.()
  }
}

// Node v25's native localStorage collides with jsdom's (App.standIn.test.tsx:74-75).
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  }
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY_ID = 'aaaaaaaa-0000-4000-8000-000000000001'

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  FakeXhr.instances = []
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

// One real entity (portfolio/v1/entities) so `active.entityId` resolves to something
// startDocumentRun will accept; everything else answers well enough not to crash a
// mounting Workspace (App.auditPrefilter.test.tsx's routeFetch, same fallback shape).
function routeFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.includes('/portfolio/v1/entities')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              entities: [
                {
                  id: ENTITY_ID,
                  name: 'Stale Map Test Co',
                  tin: '12345678-0001',
                  registration: null,
                  sector: null,
                  address: null,
                  status: 'active',
                  created_at: '2026-01-01T00:00:00Z',
                },
              ],
              pagination: { limit: 200, offset: 0, total: 1 },
            }),
        })
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0 }),
      })
    }),
  )
}

async function renderApp() {
  routeFetch()
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

describe('Core AC 6: the stage map is empty at the start of every run', () => {
  it('documentStagesResetsOnASecondRunOverTheSamePickedFiles', async () => {
    await renderApp()
    let ctx = requireCtx()

    await act(async () => {
      ctx.openCreate()
    })
    await waitFor(() => expect(requireCtx().entityId, 'active.entityId never backfilled entityId').toBe(ENTITY_ID))

    const file = new File(['%PDF-1.4 dummy'], 'invoice.pdf', { type: 'application/pdf' })
    await act(async () => {
      requireCtx().addPickedFiles([file])
    })
    ctx = requireCtx()
    expect(ctx.pickedFiles, 'the file was not picked').toHaveLength(1)
    expect(ctx.runKind, 'the picked file did not classify as a document run').toBe('document')
    const fileId = ctx.pickedFiles[0]!.id

    // Vacuity floor: documentStages must start empty (resetImport's own initial state),
    // or run 1's failure below proves nothing about a RESET.
    expect(ctx.documentStages, 'documentStages was not empty before run 1').toEqual({})

    // Run 1: fail it, deterministically, via the upload transport's own onerror -- no
    // wire shape needed for poll/import, since a rejected upload never reaches either.
    act(() => {
      requireCtx().startDocumentRun()
    })
    expect(FakeXhr.instances, 'control: run 1 never reached the upload transport').toHaveLength(1)
    act(() => {
      FakeXhr.last()!.fireError()
    })
    await waitFor(() =>
      expect(requireCtx().documentStages[fileId]?.kind, 'run 1 must leave a stage entry behind to go stale').toBe(
        'failed',
      ),
    )
    const staleReason = (requireCtx().documentStages[fileId] as Extract<DocumentRowState, { kind: 'failed' }>).reason
    expect(staleReason, 'run 1 must carry a real reason, not an empty one').not.toBe('')

    // Run 2, same pickedFiles (same file id) -- the one path where a leftover key COULD
    // leak: CreateUpload's button is the sole call site (grep) and a whole-run failure
    // routes back without resetImport, so this is the reachable second click, not a
    // fixture-only replay. Retried through waitFor: run 1's own Promise.all/finally must
    // clear `reqInFlight` first, and a call while it is still set is a silent no-op
    // (App.tsx's startDocumentRun) -- caught here because that no-op would never create
    // FakeXhr instance #2.
    await waitFor(() => {
      act(() => {
        requireCtx().startDocumentRun()
      })
      expect(FakeXhr.instances, 'control: run 2 never reached the upload transport').toHaveLength(2)
    })

    // The assertion: instance #2 exists but has not been resolved or rejected yet, so
    // nothing but the reset itself could have touched documentStages since run 2 started.
    expect(requireCtx().documentStages, 'a stale entry from run 1 survived into run 2').toEqual({})
  })
})
