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
// ONE useAsync over a Promise.all of requests (five, now six with INVCR-01-15's own
// kept-as-is total), not one hook per request: the shell either has its numbers or it
// does not, and independent hooks give partial renders where the header shows one
// channel before the other. Every TOOLBAR list query goes through `reviewQuery` — this
// is its first real caller, which is what finally cashes 06's required-`batchId` guard
// (an empty batch id would otherwise list the whole tenant); the kept-as-is total is
// the one exception, composed directly (see its own comment below) since it is not one
// of the four toolbar pills reviewQuery/filterToQuery cover.
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

import { useEffect, useRef, useState } from 'react'

import { ApiError, EmptyState, ErrorState, gatewayBase, Loading, useAsync } from '@invoice-os/api-client'

import { getImportBatch, type ImportBatch } from '../lib/importApi'
import type { ImportRun } from '../lib/importRun'
import { listInvoices, shouldFetchInvoices } from '../lib/invoices'
import {
  alreadyImportedRowsAll,
  channelTilesAll,
  filesStrip,
  reviewHeaderAll,
  reviewQueryAll,
  reviewShellStateAll,
  reviewTabs,
  unreadableRowsAll,
  type FileStripRow,
  type ReviewTab,
} from '../lib/reviewBatch'
import { ReviewAlreadyImportedTab } from './ReviewAlreadyImportedTab'
import { ReviewInvoicesTab } from './ReviewInvoicesTab'
import { ReviewUnreadableTab } from './ReviewUnreadableTab'
import type { PlatformCtx } from '../types'

interface ReviewShellData {
  // Widened from a single `batch` (BULK-01-07, Core AC 4) -- one entry per id in
  // ctx.reviewBatchIds, fetched concurrently below. The singular reviewQuery/
  // channelTiles/reviewHeader/reviewShellState/unreadableRows this shell used before
  // stay exported in lib/reviewBatch.ts (App.tsx's startRun() N=1 shortcut and
  // reviewBatch.test.ts's own parity specs are their remaining real callers) -- this
  // file switches entirely to their `...All` siblings instead of deleting them.
  batches: ImportBatch[]
  allTotal: number
  cleanTotal: number
  failingTotal: number
  // The fourth filter-pill count (INVCR-01-10). Fetched HERE rather than inside the tab
  // so all four pill counts come from ONE Promise.all with one loading state — a tab
  // that self-fetched `queued` would give four numbers from two sources, and one pill
  // could render while the other three were still unresolved.
  queuedTotal: number
  // The footer's "N kept as-is" count (INVCR-01-15, D6, task-291) -- a real server
  // total (`ListFilter.KeptAsIs`, store.go), never an arithmetic derivation of the
  // other totals ([filters-are-server-side]). NOT one of the four toolbar pills
  // (System Design §7's own table only names All/Needs a fix/Ready to submit/Queued),
  // so it is fetched here rather than through reviewQuery/filterToQuery.
  keptTotal: number
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
  // BULK-01-07: this screen now spans every batch a run created, not just the first —
  // AC #1/#4. `batchIds` is read straight off ctx; App.applyRoute already sets it to
  // the full RunRoute.batchIds array (BULK-01-05), so nothing here re-derives it.
  const batchIds = ctx.reviewBatchIds
  const batchIdsKey = batchIds.join(',')
  const [tab, setTab] = useState<ReviewTab['id']>('invoices')

  const shell = useAsync<ReviewShellData>(
    () =>
      base != null && batchIds.length > 0
        ? Promise.all([
            // Concurrent, deliberately — [parallel-gets-are-not-parallel-uploads]:
            // these are idempotent GETs, unlike startRun()'s sequential upload path
            // ([sequential-not-parallel]), which the ExistingNumbers precheck requires
            // to stay sequential. Nothing here touches that precheck.
            Promise.all(batchIds.map((id) => getImportBatch(ctx.authedFetch, base, id))),
            // `limit: 1`, never 0 — ListHandler 400s on `limit < 1`. Only
            // `pagination.total` is read; the one returned row is discarded.
            //
            // FOUR list queries, not two: `all` cannot be `clean + failing`, because
            // `needs_fix` and `status=validated` are two independent server predicates
            // and neither covers queued/submitted/accepted. Deriving the tab count as a
            // sum would silently under-count the moment subtask 11's bulk submit moves a
            // row past `validated` — which is also exactly what the fourth query counts.
            // Every one of these is now scoped to EVERY batch id in the run via
            // reviewQueryAll, not just the first.
            listInvoices(ctx.authedFetch, base, reviewQueryAll(batchIds, 'all', { limit: 1 })),
            listInvoices(ctx.authedFetch, base, reviewQueryAll(batchIds, 'ready', { limit: 1 })),
            listInvoices(ctx.authedFetch, base, reviewQueryAll(batchIds, 'needs-fix', { limit: 1 })),
            listInvoices(ctx.authedFetch, base, reviewQueryAll(batchIds, 'queued', { limit: 1 })),
            // Sixth leg, NOT built through reviewQueryAll/filterToQuery -- `kept_as_is`
            // is not one of the four ReviewPill toolbar filters those helpers cover
            // (INVCR-01-15, D6), so this composes ListInvoicesOptions directly, the
            // same way `all` above narrows by importBatchIds alone.
            listInvoices(ctx.authedFetch, base, { importBatchIds: batchIds, keptAsIs: true, limit: 1 }),
          ]).then(([batches, all, ready, fix, queued, kept]) => ({
            batches,
            allTotal: all.pagination.total,
            cleanTotal: ready.pagination.total,
            failingTotal: fix.pagination.total,
            queuedTotal: queued.pagination.total,
            keptTotal: kept.pagination.total,
          }))
        : Promise.reject(new Error('no gateway configured')),
    // `batchIdsKey` (batchIds.join(',')), never `batchIds` itself: a fresh array has a
    // new identity every render, and async-state.ts:117 spreads `deps` into a
    // useEffect dep array — an unjoined array here would refetch forever.
    { immediate: shouldFetchInvoices(base) && batchIds.length > 0, deps: [batchIdsKey] },
  )

  // Keep-previous at the SHELL, mirroring the `lastPage` idiom ReviewInvoicesTab ships
  // 40 lines into its own file. Subtask 11's bulk submit calls `shell.run()` to move the
  // four counts, and `dispatch({type:'start'})` nulls `data` on every re-run — so without
  // this ref the <Loading/> branch below would UNMOUNT the tab mid-flow and destroy its
  // filter, page, selection and the receipt panel the operator is reading. Written only
  // in an effect, never during render, and CLEARED on error so a pre-error shell cannot
  // ghost back under a failed refresh.
  const lastShell = useRef<ReviewShellData | null>(null)
  useEffect(() => {
    if (shell.data != null) lastShell.current = shell.data
    else if (shell.error != null) lastShell.current = null
  }, [shell.data, shell.error])

  // An ERROR, not an empty review surface. Reachable by editing the hash to something
  // parseReviewHash rejects, which lands on the review step with no batch to show —
  // CreateReport's `if (!report) return null` rendered a blank body there.
  if (batchIds.length === 0) {
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
  //
  // The error branch stays FIRST and unchanged: a refresh that 500s replaces the screen
  // with a retryable ErrorState and loses the receipt. That is the stated cost of keeping
  // this order — the submit is already durable server-side and the badges are correct on
  // retry, whereas rendering counts we could not verify is the worse failure.
  if (shell.error) return <ErrorState error={shell.error} onRetry={shell.run} />
  const shellData = shell.data ?? lastShell.current
  if (shellData == null) return <Loading label="Reading the import…" />

  const { batches, allTotal, cleanTotal, failingTotal, queuedTotal, keptTotal } = shellData

  // The SOLE owner of §7.5-vs-batch, keyed on the batch GETs alone — never on
  // routeAfterImport's `kind`, which answers a different question ("is there ONE invoice
  // to open") and legitimately says `rejected` for an all-quarantined batch that renders
  // here as the full batch surface with an empty Invoices tab and a populated Unreadable
  // tab. That is more honest than §7.5's "the parser reached the end of the file at row
  // 2" for an import the server completed. reviewShellStateAll is 'batch' iff ANY batch
  // in the run is 'completed' (BULK-01-06) — byte-identical to the singular
  // reviewShellState over exactly one batch.
  if (reviewShellStateAll(batches) === 'rejected') {
    // Single-batch rendering stays byte-identical (the shipped e2e at
    // import-wizard.spec.ts:1002-1014 pins it): the ORIGINAL RejectedFile, unchanged,
    // called with the same two props it always took. Only the genuinely new multi-file
    // case (Core AC 5's gap: reviewShellStateAll can say 'rejected' with several batches
    // in play, e.g. a 3-file run where every file is header-only) routes to RejectedRun,
    // which names every file against filesStrip's own reason instead of silently
    // dropping every batch but the first.
    return batches.length === 1 ? (
      <RejectedFile ctx={ctx} batch={batches[0]} allTotal={allTotal} />
    ) : (
      <RejectedRun ctx={ctx} batches={batches} run={ctx.run} />
    )
  }

  const rows = unreadableRowsAll(batches)
  const alreadyImportedRows = alreadyImportedRowsAll(batches)
  const tiles = channelTilesAll(batches, { cleanTotal, failingTotal })
  const header = reviewHeaderAll(batches, { allTotal })
  const tabs = reviewTabs({ invoices: allTotal, unreadable: tiles.frozen.unreadable, alreadyImported: tiles.frozen.alreadyImported })
  // The Unreadable tab can DISAPPEAR under a selected `tab` (a retry that now reports
  // zero structural errors), which would otherwise render a body with no tab above it.
  const activeTab = tabs.some((t) => t.id === tab) ? tab : 'invoices'
  // The per-file report (AC #3), rendered under the header below. filesStrip is TOTAL
  // over ctx.run (`| null`-safe), but in PRACTICE ctx.run.files is always `[]` by the
  // time this component mounts on the live post-import path: App.applyRoute's 'review'
  // branch resets `run` to idle in the SAME batched update that sets reviewBatchIds
  // (App.tsx:585-587), so `files` here today always equals one row per `batches` entry,
  // never a run-only-failure row -- a request that failed before ever producing a
  // batch is visible on ImportProgress while the run is live, then silently drops off
  // this screen. Flagged, not fixed (App.tsx is out of this subtask's scope and its
  // RunRoute handling is already-shipped, AC #8) -- filesStrip is still wired for the
  // `run` it is GIVEN, so this self-corrects if that reset is ever revisited.
  //
  // `files.length > 1` (this screen's own gate below) and `showsSourceFile(batches)`
  // (ReviewRow's gate, lib/reviewBatch.ts) are DELIBERATELY two different predicates,
  // not one accidentally duplicated: `showsSourceFile` asks "can an invoice row's
  // source be ambiguous" (only possible with >1 batch), while this asks "is there more
  // than one FILE to report on" (a batch plus a run-only failure both count, even
  // though only one of them ever produces an invoice to disambiguate). They coincide
  // today only because run-only-failure rows never survive to this screen (above) --
  // if that changes, a 2-file run (1 batch + 1 run-only failure) would correctly show
  // the strip (2 files) while ReviewRow correctly stays silent (only 1 batch's worth of
  // invoices exist, nothing to disambiguate between).
  const files = filesStrip(batches, ctx.run)
  // Summed across every batch (was the single batch's own `rows_valid`) -- the footer
  // paragraph just below states how many rows the LIVE channel's tiles above it were
  // built from, and that is a run-wide fact once more than one file is in play.
  const rowsValidTotal = batches.reduce((sum, b) => sum + b.rows_valid, 0)

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
          {/* `header.batchIds` is `[batchId]` at one batch -- `.join(', ')` over a
              single-element array returns that element verbatim, so this line stays
              byte-identical to the shipped single-batch text. */}
          BATCH {header.batchIds.join(', ')}
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
              Built from {rowsValidTotal} rows. Every one of these exists in the ledger — fixing and submitting is what is left.
            </p>
          </div>

          {/* The hairline is what makes "two channels" a structure rather than a claim. */}
          <div style={{ width: 1, alignSelf: 'stretch', background: 'var(--line-1)', flex: 'none' }} />

          <div style={{ flex: '1 1 280px', minWidth: 240 }}>
            <div className="label" style={{ marginBottom: 9 }}>Not imported</div>
            {/* Column, not the live channel's row direction: the paragraph below has to
                stay adjacent to the unreadable tile. `Tile` has no margin, hence gap. */}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {/* Value counts rows, caption counts invoices — BUG08-BATCH-8 pins both.
                  No colour ternary: --status-muted-* is already the greyed value. */}
              <Tile
                value={`${tiles.frozen.alreadyImported} already imported`}
                caption={tiles.frozen.alreadyImportedAtZero ? 'Nothing in this file was already in your ledger.' : `${tiles.frozen.alreadyImportedInvoices} invoices already in your ledger. Nothing to fix.`}
                bg="var(--status-muted-bg)"
                border="var(--status-muted-border)"
                text="var(--status-muted-text)"
                dashed
              />
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
            </div>
            <p style={{ fontSize: 11.5, color: 'var(--fg-3)', margin: '9px 0 0', lineHeight: 1.55 }}>
              {tiles.atZero
                ? 'This channel stays visible even at zero, so its absence is a fact and not an omission.'
                : 'A structural failure, not a compliance one: no rule was ever run. Nothing was stored.'}
            </p>
          </div>
        </div>
      </div>

      {/* The files strip (AC #3, Core AC 5) -- one row per file from filesStrip, hidden
          entirely at exactly one file (nothing to disambiguate there, matching
          showsSourceFile's own ">1" threshold). Reused, never re-derived, by
          RejectedRun above for the all-rejected multi-file surface. */}
      {files.length > 1 && <FilesStripView files={files} />}

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

      {/* Rendered inside this branch and genuinely UNMOUNTED otherwise, never CSS-hidden:
          the re-homed E2E-04 subset witness (import-wizard.spec.ts:410) asserts a clean
          invoice number is absent — as a SUBSTRING match over the page — while the
          Unreadable rows tab is the open one. An always-mounted, hidden table fails it,
          and nothing on this branch can catch that before the PR leaves draft. */}
      {activeTab === 'invoices' && (
        <ReviewInvoicesTab
          ctx={ctx}
          base={base}
          batchIds={batchIds}
          batches={batches}
          totals={{ allTotal, cleanTotal, failingTotal, queuedTotal }}
          onSubmitted={shell.run}
        />
      )}

      {activeTab === 'unreadable' && (
        <ReviewUnreadableTab
          rows={rows}
          rowsTotal={batches.reduce((sum, b) => sum + b.rows_total, 0)}
          batchIds={batchIds}
          onImportCorrected={ctx.restartImport}
        />
      )}

      {activeTab === 'already-imported' && (
        <ReviewAlreadyImportedTab
          rows={alreadyImportedRows}
          rowsTotal={batches.reduce((sum, b) => sum + b.rows_total, 0)}
          batchIds={batchIds}
          onOpenInvoice={ctx.openImportedInvoice}
        />
      )}

      {/* Footer. `N kept as-is` (INVCR-01-15, D6, task-291) is now a REAL server total
          (keptTotal, the shell's sixth list query) -- the false-zero concern the prior
          subtask's comment here raised no longer applies, since the count is genuinely
          fetched rather than hardcoded. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap', paddingTop: 4 }}>
        {/* `N queued for transmission` rather than AC-7's literal `N submitted`: the only
            count this screen has is `status=queued`, and the worker advances rows past
            queued within seconds — so labelling it "submitted" would read `0 submitted`
            for a fully-sent batch on a revisit, the exact false zero this story's counter
            discipline exists to prevent. QUEUED is D2's own real name for the state. */}
        <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>
          {allTotal} invoices stored · {cleanTotal} ready to submit · {queuedTotal} queued for transmission · {failingTotal} awaiting a fix · {keptTotal} kept as-is
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

// Multi-file all-rejected surface (BULK-01-07, Core AC 5 gap). reviewShellStateAll
// answers 'rejected' whenever NO batch in the run is 'completed' -- genuinely reachable
// with several batches (e.g. a 3-file run where every file is header-only, each still
// answering 201 per file). RejectedFile above takes ONE batch and cannot represent this;
// rendering only batches[0] would silently drop every other file's name and reason,
// which is exactly the Core AC 5 miss this component exists to close. Never reached at
// exactly one batch -- that case stays on the original RejectedFile, untouched, so the
// shipped e2e (import-wizard.spec.ts:1002-1014) keeps seeing byte-identical output.
//
// filesStrip(batches, run) is the SAME per-file report the successful surface's files
// strip (FilesStripView, below) renders -- reused, not re-derived, so both branches
// report a rejected file's reason from one source.
function RejectedRun({ ctx, batches, run }: { ctx: PlatformCtx; batches: ImportBatch[]; run: ImportRun }) {
  const files = filesStrip(batches, run)
  return (
    <div data-testid="review-rejected-run" style={{ background: 'var(--bg-2)', border: '1px solid var(--status-red-border)', borderRadius: 'var(--radius-md)', padding: '24px 22px', maxWidth: 720 }}>
      <div style={{ fontSize: 16, fontWeight: 600, color: 'var(--status-red-text)', marginBottom: 8 }}>Nothing was imported</div>
      <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: '0 0 18px', lineHeight: 1.6 }}>
        The server rejected every file in this run and created no invoices. This usually means a file held no data rows — a spreadsheet with only a header row, for example.
      </p>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginBottom: 18 }}>
        {files.map((f) => (
          <div key={f.id} data-testid="review-rejected-run-file" style={{ border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', padding: '11px 14px', background: 'var(--bg-3)' }}>
            <div className="mono" style={{ fontSize: 12.5, fontWeight: 600, color: 'var(--fg-2)', wordBreak: 'break-all' }}>{f.filename}</div>
            {/* `reason` is null iff the file produced at least one ready invoice --
                unreachable in THIS branch for a batch row (every batch here is
                non-'completed'), but a run-only failure row is always non-null too
                (runFailures/filesStrip only ever push one with a message). Guarded
                anyway rather than assumed, matching filesStrip's own total-over-input
                contract. */}
            {f.reason != null && (
              <div style={{ fontSize: 12, color: 'var(--fg-3)', marginTop: 4, lineHeight: 1.5 }}>{f.reason}</div>
            )}
          </div>
        ))}
      </div>

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

// The files strip (AC #3, Core AC 5) -- one row per file, filename + outcome + (for a
// rejected file) the reason, read straight off filesStrip's own FileStripRow shape.
// FileStripRow carries no invoice-count field (`{id, filename, reason}` only, pinned by
// reviewBatch.test.ts) -- an exact per-file invoice count is not cheaply available
// without a NEW per-batch query this subtask does not call for (the shell's own counts
// are aggregated across every batch id via reviewQueryAll), so none is rendered here
// rather than fabricating one from `rows_valid` (rows are not invoices -- several rows
// can group into one). Flagged in this subtask's own report, not silently dropped.
function FilesStripView({ files }: { files: FileStripRow[] }) {
  return (
    <div data-testid="review-files-strip" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div className="label">Files in this run</div>
      {files.map((f) => {
        const ok = f.reason == null
        return (
          <div key={f.id} data-testid="review-files-strip-row" style={{ padding: '10px 14px', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <span style={{ width: 8, height: 8, borderRadius: 99, background: ok ? 'var(--status-green-text)' : 'var(--status-red-text)', flex: 'none' }} />
              <span className="mono" style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)', wordBreak: 'break-all', flex: 1 }}>{f.filename}</span>
              <span
                className="mono"
                style={{ fontSize: 10.5, fontWeight: 600, color: ok ? 'var(--status-green-text)' : 'var(--status-red-text)', letterSpacing: '0.04em', textTransform: 'uppercase', flex: 'none' }}
              >
                {ok ? 'Imported' : 'Rejected'}
              </span>
            </div>
            {/* Reason is read from `reason` alone, never from a batch's own `status`
                ([reason-comes-from-errors-not-status]) -- filesStrip already resolved
                this off batch.errors, so this component performs no re-derivation. */}
            {f.reason != null && (
              <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 5, lineHeight: 1.5, paddingLeft: 18 }}>{f.reason}</div>
            )}
          </div>
        )
      })}
    </div>
  )
}
