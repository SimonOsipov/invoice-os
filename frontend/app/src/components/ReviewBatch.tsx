// Create flow · step "review" — the import review shell (INVCR-01-09, §7.1/§7.2/§7.5,
// Core AC 6/8/9). REPLACES CreateReport.tsx, which rendered the POST's 201 payload held
// in memory and therefore rendered a blank body under a step strip on any reload.
//
// D4's ruling is what this file is: the review screen is addressable (`#review/<uuid>`)
// and REVISITABLE, so it re-derives everything from the server on every arrival instead
// of reading a frozen import-time payload. Two consequences worth stating, because both
// look like extra work until you know why:
//
//  1. It re-fetches the batch it may have created a second ago. The POST-arrival path
//     and a deep-link revisit therefore run ONE derivation (reviewShellState) over ONE
//     source (the batch GET), rather than two that are free to disagree. Seeding from
//     the 201 body would need an ImportReport -> ImportBatch shim — a second shape of the
//     same facts, which is exactly the drift D4 removed.
//  2. The left channel's counts are LIVE `pagination.total`s off filtered list queries,
//     not the batch's frozen counters, so a tile MOVES when a row is fixed (AC-2).
//
// ONE useAsync over a Promise.all of four requests, not four hooks: the shell either has
// its numbers or it does not, and four independent hooks give four partial renders where
// the header shows one channel before the other. Every list query goes through
// `reviewQuery` — this is its first real caller, which is what finally cashes 06's
// required-`batchId` guard (an empty batch id would otherwise list the whole tenant).
//
// NO `entity_id` and NO `gateByActiveEntity`, unlike InvoicesList. The batch id already
// narrows to one entity and RLS bounds the tenant; narrowing AGAIN by the workspace
// switcher would render a partially-empty batch — the filter-after-narrow lie
// [entity-id-cut] names. Recorded consequence: deep-linking a sibling entity's batch
// renders it without switching the workspace.
//
// NO StrictMode/mount-fetch guard. useAsync already bumps `runId` in its effect cleanup,
// so the first of StrictMode's two runs resolves into a discarded dispatch; App's
// `reqInFlight` guards the WRITE path (a duplicate POST is a duplicate import), and a
// duplicate idempotent GET in dev is the shipped, accepted InvoicesList behaviour.

import { useState } from 'react'

import { ApiError, EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { getImportBatch, type ImportBatch } from '../lib/importApi'
import { listInvoices, shouldFetchInvoices } from '../lib/invoices'
import {
  channelTiles,
  reviewHeader,
  reviewQuery,
  reviewShellState,
  reviewTabs,
  unreadableRows,
  type ReviewTab,
} from '../lib/reviewBatch'
import { ReviewUnreadableTab } from './ReviewUnreadableTab'
import type { PlatformCtx } from '../types'

interface ReviewShellData {
  batch: ImportBatch
  allTotal: number
  cleanTotal: number
  failingTotal: number
}

// One channel tile. `dashed` and `muted` are RENDER decisions taken here, in the
// component, not fields on the view-model: the "Not imported" channel is always dashed
// and greys at zero, and putting those two constants inside a data structure would be
// smuggling a styling literal into a value 08's specs pin.
function Tile({
  value,
  caption,
  bg,
  border,
  text,
  dashed = false,
}: {
  value: string
  caption: string
  bg: string
  border: string
  text: string
  dashed?: boolean
}) {
  return (
    <div style={{ flex: 1, minWidth: 150, background: bg, border: `1px ${dashed ? 'dashed' : 'solid'} ${border}`, borderRadius: 'var(--radius-md)', padding: '12px 14px' }}>
      <div className="mono" style={{ fontSize: 15, fontWeight: 600, color: text }}>{value}</div>
      <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 4, lineHeight: 1.5 }}>{caption}</div>
    </div>
  )
}

export function ReviewBatch({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const batchId = ctx.reviewBatchId
  const [tab, setTab] = useState<ReviewTab['id']>('invoices')

  const shell = useAsync<ReviewShellData>(
    () =>
      base != null && batchId != null
        ? Promise.all([
            getImportBatch(ctx.authedFetch, base, batchId),
            // `limit: 1`, never 0 — ListHandler 400s on `limit < 1`. Only
            // `pagination.total` is read; the one returned row is discarded.
            //
            // THREE list queries, not two: `all` cannot be `clean + failing`, because
            // `needs_fix` and `status=validated` are two independent server predicates
            // and neither covers queued/submitted/accepted. Deriving the tab count as a
            // sum would silently under-count the moment subtask 11's bulk submit moves a
            // row past `validated`.
            listInvoices(ctx.authedFetch, base, reviewQuery(batchId, 'all', { limit: 1 })),
            listInvoices(ctx.authedFetch, base, reviewQuery(batchId, 'ready', { limit: 1 })),
            listInvoices(ctx.authedFetch, base, reviewQuery(batchId, 'needs-fix', { limit: 1 })),
          ]).then(([batch, all, ready, fix]) => ({
            batch,
            allTotal: all.pagination.total,
            cleanTotal: ready.pagination.total,
            failingTotal: fix.pagination.total,
          }))
        : Promise.reject(new Error('no gateway configured')),
    { immediate: shouldFetchInvoices(base) && batchId != null, deps: [batchId] },
  )

  // An ERROR, not an empty review surface. Reachable by editing the hash to something
  // parseReviewHash rejects, which lands on the review step with no batch to show —
  // CreateReport's `if (!report) return null` rendered a blank body there.
  if (batchId == null) {
    return <ErrorState error={new ApiError('network', 'There is no import to review. Start an import, or open one from the invoices list.')} />
  }
  // The no-gateway showcase build: zero network by construction, so there is nothing to
  // report an error about.
  if (base == null) {
    return <EmptyState title="No gateway configured" message="This build has no invoice service to read an import from." />
  }
  // Also the stale/foreign deep-link path: GET /v1/imports/{id} 404s "not found" for a
  // nonexistent AND a cross-tenant id alike, so a well-formed uuid from someone else's
  // workspace lands here rather than rendering an empty batch as if it were real.
  if (shell.error) return <ErrorState error={shell.error} onRetry={shell.run} />
  if (shell.data == null) return <Loading label="Reading the import…" />

  const { batch, allTotal, cleanTotal, failingTotal } = shell.data

  // The SOLE owner of §7.5-vs-batch, keyed on the batch GET alone — never on
  // routeAfterImport's `kind`, which answers a different question ("is there ONE invoice
  // to open") and legitimately says `rejected` for an all-quarantined batch that renders
  // here as the full batch surface with an empty Invoices tab and a populated Unreadable
  // tab. That is more honest than §7.5's "the parser reached the end of the file at row
  // 2" for an import the server completed.
  if (reviewShellState(batch) === 'rejected') {
    return <RejectedFile ctx={ctx} batch={batch} allTotal={allTotal} />
  }

  const rows = unreadableRows(batch.errors)
  const tiles = channelTiles(batch, { cleanTotal, failingTotal })
  const header = reviewHeader(batch, { allTotal })
  const tabs = reviewTabs({ invoices: allTotal, unreadable: tiles.frozen.unreadable })
  // The Unreadable tab can DISAPPEAR under a selected `tab` (a retry that now reports
  // zero structural errors), which would otherwise render a body with no tab above it.
  const activeTab = tabs.some((t) => t.id === tab) ? tab : 'invoices'

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
      {/* §7.1 header — ONE card, TWO channels split by a vertical hairline. */}
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '18px 20px' }}>
        {/* The title takes NO filename, on EITHER arrival path. import_batches has no
            filename column and D4 forbids a migration, so an in-session filename would
            make one URL render two different titles depending on how it was reached.
            There is also no teal "STORED & VALIDATED" pill: it asserts a verdict that
            contradicts the red tile 40px below it whenever anything failed, and the
            sub-line already carries the honest version. */}
        <h2 style={{ fontSize: 19, fontWeight: 600, letterSpacing: '-0.02em', margin: '0 0 6px' }}>{header.title}</h2>
        <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em', textTransform: 'uppercase' }}>{header.subline}</div>
        <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-4)', letterSpacing: '0.05em', marginTop: 3, wordBreak: 'break-all' }}>
          BATCH {header.batchId}
        </div>

        <div style={{ display: 'flex', gap: 22, marginTop: 18, flexWrap: 'wrap' }}>
          <div style={{ flex: '1 1 340px', minWidth: 280 }}>
            <div className="label" style={{ marginBottom: 9 }}>Imported · stored in the ledger</div>
            <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
              <Tile
                value={`${tiles.live.cleanTotal} valid`}
                caption="Passed every rule. Ready to submit."
                bg="var(--status-green-bg)"
                border="var(--status-green-border)"
                text="var(--status-green-text)"
              />
              <Tile
                value={`${tiles.live.failingTotal} failed a rule`}
                caption="Read fine, stored, but not compliant yet."
                bg="var(--status-red-bg)"
                border="var(--status-red-border)"
                text="var(--status-red-text)"
              />
            </div>
            <p style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: '9px 0 0', lineHeight: 1.55 }}>
              Built from {batch.rows_valid} rows. Every one of these exists in the ledger — fixing and submitting is what is left.
            </p>
          </div>

          {/* The hairline is what makes "two channels" a structure rather than a claim. */}
          <div style={{ width: 1, alignSelf: 'stretch', background: 'var(--line-1)', flex: 'none' }} />

          <div style={{ flex: '1 1 280px', minWidth: 240 }}>
            <div className="label" style={{ marginBottom: 9 }}>Not imported</div>
            {/* DASHED as well as amber, and STILL DASHED at zero — only greyed. The
                channel stays visible at zero so its absence is a fact and not an
                omission, which is also why `atZero` is an explicit field on the
                view-model rather than `count === 0` inferred here. */}
            <Tile
              value={`${tiles.frozen.unreadable} unreadable rows`}
              caption={tiles.atZero ? 'Every row in the file became part of an invoice.' : 'No invoice exists for them.'}
              bg={tiles.atZero ? 'var(--bg-3)' : 'var(--status-amber-bg)'}
              border={tiles.atZero ? 'var(--line-2)' : 'var(--status-amber-border)'}
              text={tiles.atZero ? 'var(--fg-3)' : 'var(--status-amber-text)'}
              dashed
            />
            <p style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: '9px 0 0', lineHeight: 1.55 }}>
              {tiles.atZero
                ? 'This channel stays visible even at zero, so its absence is a fact and not an omission.'
                : 'A structural failure, not a compliance one: no rule was ever run. Nothing was stored.'}
            </p>
          </div>
        </div>
      </div>

      {/* §7.2 tabs. SettingsView.tsx's precedent: .pf-tab, NOT .pf-btn — the button
          classes force a full pill radius, which bends this 2px underline into an arc.
          The second tab is absent from `tabs` entirely at zero, never hidden with CSS. */}
      <div style={{ display: 'flex', gap: 26, borderBottom: '1px solid var(--line-1)' }}>
        {tabs.map((t) => {
          const a = activeTab === t.id
          return (
            <button
              key={t.id}
              onClick={() => setTab(t.id)}
              className="pf-tab"
              style={{ border: 0, background: 'transparent', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: a ? 600 : 500, color: a ? 'var(--fg-1)' : 'var(--fg-3)', padding: '0 0 12px', borderBottom: `2px solid ${a ? 'var(--action)' : 'transparent'}`, marginBottom: -1 }}
            >
              {t.label}
            </button>
          )
        })}
      </div>

      {activeTab === 'invoices' && (
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', padding: '14px 18px', fontSize: 13, color: 'var(--fg-3)' }}>
          {/* Subtask 10 replaces this line with the invoice table + filter rail. It is a
              true statement, not a placeholder apology: the rows ARE stored (§10.10). */}
          {allTotal} invoices are stored in this batch.
        </div>
      )}

      {activeTab === 'unreadable' && (
        <ReviewUnreadableTab rows={rows} rowsTotal={batch.rows_total} batchId={batch.id} onImportCorrected={ctx.restartImport} />
      )}

      {/* Footer. `N kept as-is` is OMITTED ENTIRELY rather than rendered as 0 —
          `kept_as_is_at` reaches the wire in subtask 15, and a counter the server does
          not yet send, printed as a zero, is exactly the false zero this story's counter
          discipline exists to prevent. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap', paddingTop: 4 }}>
        <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>
          {allTotal} invoices stored · {cleanTotal} ready to submit · {failingTotal} awaiting a fix
        </span>
        {/* NAVIGATION ONLY. The invoices were persisted at import time (§10.10), so a
            "Finish writes the batch" step would be a lie about when the data landed. */}
        <button onClick={ctx.closeCreate} className="v2-btn v2-btn-ghost pf-btn" style={{ marginLeft: 'auto', height: 38, padding: '0 16px', fontSize: 13 }}>
          Finish · go to invoices
        </button>
      </div>
    </div>
  )
}

// §7.5 — the file the server refused. Reached whenever the BATCH's status is not
// 'completed'; the only path that reaches it from a fresh import is the header-only
// spreadsheet, which the handler answers 201 Created with every counter at Go zero.
//
// The tiles read the BATCH, never literal zeros: the importer's other two
// Finalize(..., "failed") calls 500 the request (so routing never sees them) but still
// persist a batch row carrying REAL non-zero counters, and a deep link to one of those
// must not report an import of nothing when 40 rows were stored.
//
// §7.5's parser narration ("read 11 columns, reached the end of the file at row 2") is
// [brief-only] and is NOT built — no field on the wire carries it, and writing it here
// would be the browser narrating a parse it never saw.
function RejectedFile({ ctx, batch, allTotal }: { ctx: PlatformCtx; batch: ImportBatch; allTotal: number }) {
  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--status-red-border)', borderRadius: 'var(--radius-md)', padding: '24px 22px', maxWidth: 720 }}>
      <div style={{ fontSize: 16, fontWeight: 600, color: 'var(--status-red-text)', marginBottom: 8 }}>Nothing was imported</div>
      {/* Hedged, deliberately. The header-only spreadsheet is the usual cause and the
          only one reachable by importing, but the tiles below can show non-zero stored
          rows for a batch that failed another way — and a flat "it has a header row and
          no data rows" sitting above "Rows stored 40" is the same self-contradiction
          that got the teal STORED & VALIDATED pill dropped from the batch header. */}
      <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: '0 0 18px', lineHeight: 1.6 }}>
        The server rejected this file and created no invoices. This usually means it held no data rows — a spreadsheet with only a header row, for example.
      </p>

      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginBottom: 18 }}>
        <Tile value={String(allTotal)} caption="Invoices created" bg="var(--bg-3)" border="var(--line-2)" text="var(--fg-2)" />
        <Tile value={String(batch.rows_valid)} caption="Rows stored" bg="var(--bg-3)" border="var(--line-2)" text="var(--fg-2)" />
        {/* Dashed, matching the not-imported channel on the batch surface: the same
            fact deserves the same visual language on both screens. */}
        <Tile value={String(batch.rows_invalid)} caption="Rows quarantined" bg="var(--bg-3)" border="var(--line-2)" text="var(--fg-2)" dashed />
      </div>

      <div className="label" style={{ marginBottom: 4 }}>Batch id</div>
      <div className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', wordBreak: 'break-all', marginBottom: 18 }}>{batch.id}</div>

      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
        <button onClick={ctx.restartImport} className="v2-btn v2-btn-primary pf-btn" style={{ height: 38, padding: '0 16px', fontSize: 13, background: 'var(--action)', color: 'var(--text-on-dark)' }}>
          Choose another file
        </button>
        <button onClick={ctx.skipUpload} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 38, padding: '0 16px', fontSize: 13 }}>
          Enter one invoice instead
        </button>
      </div>
    </div>
  )
}
