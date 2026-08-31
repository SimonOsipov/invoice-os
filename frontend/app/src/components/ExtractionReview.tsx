// The review screen: the detail fetch, the state ladder, and the artboard's two-pane flex row.
// Zoom and the page handles stay inside ExtractionCanvas; this shell owns only the selection.
// Pinned by ExtractionReview.test.tsx.

import { useState, type CSSProperties, type ReactNode } from 'react'

import { ErrorState, Loading, gatewayBase, useAsync } from '@invoice-os/api-client'

import { getExtractionDetail, type ExtractionDetail } from '../lib/extractionReview'
import type { PlatformCtx } from '../types'
import { ExtractionCanvas } from './ExtractionCanvas'
import { ExtractionFields } from './ExtractionFields'

const STILL_READING = 'This document is still being read.'
const COULD_NOT_READ = 'This document could not be read.'

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

  const data = detail.data
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
    content = (
      <div data-testid="extraction-review-body" style={BODY}>
        <ExtractionCanvas
          ctx={ctx}
          jobId={jobId}
          doc={data.document}
          pages={data.pages}
          fields={data.fields}
          selected={selected}
          scrollNonce={current?.n ?? 0}
        />
        <ExtractionFields
          fields={data.fields}
          selected={selected}
          onSelect={(name) => setPick((p) => ({ jobId, name, n: (p?.n ?? 0) + 1 }))}
        />
      </div>
    )
  }

  return (
    <div data-testid="extraction-review" style={SCREEN}>
      {content}
    </div>
  )
}
