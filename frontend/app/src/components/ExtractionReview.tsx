// The review screen: the detail fetch, the state ladder, and the artboard's two-pane flex row.
// Zoom and the page handles stay inside ExtractionCanvas; this shell owns only the selection.
// Pinned by ExtractionReview.test.tsx.

import { useRef, useState, type CSSProperties, type ReactNode } from 'react'

import { ErrorState, Loading, gatewayBase, useAsync } from '@invoice-os/api-client'

import { applyDraft, getExtractionDetail, postFieldCorrection, savableCorrections } from '../lib/extractionReview'
import type { DraftEntries, ExtractionCandidate, ExtractionDetail } from '../lib/extractionReview'
import type { PlatformCtx } from '../types'
import { ExtractionCanvas } from './ExtractionCanvas'
import { ExtractionFields } from './ExtractionFields'

const STILL_READING = 'This document is still being read.'
const COULD_NOT_READ = 'This document could not be read.'
const SAVE = 'Save what you settled'

// River retries a `failed` job, so it is not terminal and takes the still-reading sentence.
const UNSETTLED = ['queued', 'extracting', 'failed']

// `height: '100%'` is load-bearing: App.tsx's `.pf-scroll` is a BLOCK container, so at
// `height: auto` this root grows to its content, the body's `flex: 1` fills nothing and the
// page scrolls instead of the ground.
const SCREEN: CSSProperties = { height: '100%', display: 'flex', flexDirection: 'column' }

// SourceDocumentModal.tsx:224-232's containment idiom: without `flex: 1` + `minHeight: 0` a
// flex item takes a content-based automatic minimum and the panes grow to their content.
const BODY: CSSProperties = { display: 'flex', flex: 1, minHeight: 0, overflow: 'hidden' }

const PAD: CSSProperties = { padding: '30px 36px' }

const SENTENCE: CSSProperties = { ...PAD, fontSize: 13, color: 'var(--fg-2)' }

// The artboard's right column is header / scrolling body / `flex: none` footer (`:406`). Outside
// the panes, so Save takes the shipped button recipe verbatim and the pane stays at two children.
const FOOTER: CSSProperties = {
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  gap: 14,
  padding: '12px 20px',
  borderTop: '1px solid var(--line-1)',
  background: 'var(--bg-2)',
}

// `.v2-btn-primary:hover` brightens with no `:disabled` guard (app-layer.css:213), so a disabled
// Save would light up under the cursor and read pressable. EvidenceBundleDrawer.tsx's spread.
const SAVE_DISABLED: CSSProperties = {
  background: 'var(--bg-3)',
  color: 'var(--fg-4)',
  cursor: 'not-allowed',
  filter: 'none',
}

const WRITE_ERROR: CSSProperties = { flex: 1, minWidth: 0, fontSize: 12, color: 'var(--status-red-text)' }

export function ExtractionReview({ ctx, jobId }: { ctx: PlatformCtx; jobId: string }) {
  const base = gatewayBase()
  const detail = useAsync<ExtractionDetail>(
    () => (base ? getExtractionDetail(ctx.authedFetch, base, jobId) : Promise.reject(new Error('no gateway configured'))),
    { isEmpty: () => false, deps: [jobId] },
  )

  // Tagged with its job and dropped when that job changes: a tag that gated only the read
  // handed document 1 its old selection back on the way to it (`does not hand document 1 its
  // old selection back on the way to it`). `n` bumps per click, and ExtractionCanvas's scroll
  // effect keys on it, so re-selecting the same row re-centres it (`D-25`).
  const [pick, setPick] = useState<{ jobId: string; name: string; n: number } | null>(null)
  if (pick && pick.jobId !== jobId) setPick(null)
  const current = pick && pick.jobId === jobId ? pick : null
  const selected = current?.name ?? null

  // One shared draft across the pane and one Save: a chip, a typed value and an Undo all write
  // through it, and nothing reaches the register until Save. Job-tagged and dropped on a job
  // change, the same render-time guard `pick` uses.
  const [draft, setDraft] = useState<{ jobId: string; entries: DraftEntries } | null>(null)
  if (draft && draft.jobId !== jobId) setDraft(null)
  const entries = draft && draft.jobId === jobId ? draft.entries : {}

  // The post-write re-read, held HERE rather than through detail.run(): asyncReducer's start arm
  // returns `data: null` and the shell renders <Loading/> on it, so a run() would unmount both
  // panes, re-fetch every page image and drop the canvas scroll mid-write.
  const [merged, setMerged] = useState<{ jobId: string; detail: ExtractionDetail } | null>(null)
  if (merged && merged.jobId !== jobId) setMerged(null)
  const [writing, setWriting] = useState(false)
  const [writeError, setWriteError] = useState<string | null>(null)
  // The generation both shipped overlays carry: a re-read still in flight when a second Save
  // starts must not land on top of the newer one. A navigation is handled by the job tag above.
  const write = useRef(0)

  const draftField = (name: string, entry: DraftEntries[string]) => {
    setDraft((d) => ({ jobId, entries: { ...(d && d.jobId === jobId ? d.entries : {}), [name]: entry } }))
  }

  const data = merged && merged.jobId === jobId ? merged.detail : detail.data
  let content: ReactNode
  if (detail.status === 'error' && detail.error) {
    content = (
      <div style={PAD}>
        <ErrorState error={detail.error} onRetry={detail.run} />
      </div>
    )
  } else if (data === null) {
    content = (
      <div style={PAD}>
        <Loading />
      </div>
    )
  } else if (UNSETTLED.includes(data.state)) {
    content = <div style={SENTENCE}>{STILL_READING}</div>
  } else if (data.state === 'dead_lettered') {
    // The designed could-not-read screen is EXTR-15's; this is the honest placeholder (`D-9`).
    content = <div style={SENTENCE}>{COULD_NOT_READ}</div>
  } else {
    const wire = data.fields
    // The canvas follows the draft: a chosen chip moves the highlight to that alternative's own
    // box before anything is saved, which is the both-ways binding read the other way round.
    const shown = applyDraft(wire, entries)
    const posts = savableCorrections(wire, entries)

    // N POSTs, awaited ONE AT A TIME in vocabulary order: each opens its own transaction over
    // the same invoice, and the append-only table's seq should follow the order the person
    // reads. The run stops at the first refusal and keeps every entry that did not commit --
    // the user's typing is not the screen's to discard.
    const save = async () => {
      if (!base || writing || posts.length === 0) return
      const mine = write.current + 1
      write.current = mine
      setWriting(true)
      setWriteError(null)

      const committed: string[] = []
      let refusal: string | null = null
      for (const p of posts) {
        try {
          await postFieldCorrection(ctx.authedFetch, base, jobId, p.field, p.body)
          committed.push(p.field)
        } catch (e) {
          // The server's own sentence, verbatim: apiFetch already lifts the error envelope into
          // ApiError.message and the house convention renders it unmodified.
          refusal = e instanceof Error ? e.message : String(e)
          break
        }
      }

      let fresh: ExtractionDetail | null = null
      try {
        fresh = await getExtractionDetail(ctx.authedFetch, base, jobId)
      } catch {
        // A failed re-read leaves the screen on what it already had; the committed half is
        // still in the register and the next read shows it.
      }

      if (write.current !== mine) return
      // A save keeps only what it POSTED and did not commit -- that is the person's typing
      // under a refusal. An entry savableCorrections never posted (a blank, a no-op) was not
      // refused, so keeping it would leave applyDraft laying it over every later read: the
      // cell would deny a value the register holds, with Save disabled and no gesture left to
      // clear it. An entry typed while the POSTs were in flight is a different object, and is
      // not this save's to judge either way.
      const refused = new Set(posts.map((p) => p.field).filter((f) => !committed.includes(f)))
      setDraft((d) => {
        const held = d && d.jobId === jobId ? d.entries : {}
        const kept: DraftEntries = {}
        for (const name of Object.keys(held)) {
          if (refused.has(name) || held[name] !== entries[name]) kept[name] = held[name]
        }
        return { jobId, entries: kept }
      })
      setWriteError(refusal)
      if (fresh !== null) setMerged({ jobId, detail: fresh })
      setWriting(false)
    }

    content = (
      <>
        <div data-testid="extraction-review-body" style={BODY}>
          <ExtractionCanvas
            ctx={ctx}
            jobId={jobId}
            doc={data.document}
            pages={data.pages}
            fields={shown}
            selected={selected}
            scrollNonce={current?.n ?? 0}
          />
          <ExtractionFields
            fields={wire}
            draft={entries}
            selected={selected}
            onSelect={(name) => setPick((p) => ({ jobId, name, n: (p?.n ?? 0) + 1 }))}
            onType={(name, value) => draftField(name, { kind: 'typed', value, region: null })}
            onChoose={(name, a: ExtractionCandidate) =>
              draftField(name, { kind: 'chosen', value: a.value ?? '', region: a.region })
            }
            // The undo posts a value the server ignores -- it applies the extractor's own rank-0
            // reading -- but the boundary 400s a blank one, so it carries the value it replaces.
            onUndo={(name) =>
              draftField(name, { kind: 'undone', value: wire.find((f) => f.name === name)?.value ?? '', region: null })
            }
          />
        </div>
        <div style={FOOTER}>
          {writeError === null ? null : (
            <span data-testid="extraction-write-error" style={WRITE_ERROR}>
              {writeError}
            </span>
          )}
          <button
            type="button"
            data-testid="extraction-save"
            className="v2-btn v2-btn-primary pf-btn"
            disabled={writing || posts.length === 0}
            onClick={save}
            style={writing || posts.length === 0 ? SAVE_DISABLED : undefined}
          >
            {SAVE}
          </button>
        </div>
      </>
    )
  }

  return (
    <div data-testid="extraction-review" style={SCREEN}>
      {content}
    </div>
  )
}
