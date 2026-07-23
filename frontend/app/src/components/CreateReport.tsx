// Create flow · step "report" — what the server did with the uploaded spreadsheet
// (M4-08-05, Core AC3/AC4). PRESENTATION ONLY: every derivation lives in
// lib/importReport.ts so it is node-testable, and this file renders what those functions
// return. Nothing here recomputes a count, a verdict, or a severity label — the browser
// must never form an opinion the server did not send (Core AC3).
//
// The two error channels are rendered as two SEPARATE sections and never merged.
// `errors[]` is structural ("couldn't read this row"); `invoice_violations[]` is content
// ("read fine, but the rule failed"). A structural error may carry a `rule_key` — the
// store-duplicate case — and is STILL structural, so it appears in the structural
// section only.
//
// Neither section header carries a count. A list length rendered as a number beside the
// server's own counters invites reading it AS one of them, and for the violations list
// that is actively wrong: `invoice_violations` lists every invoice with a violation of
// ANY severity, while `invoices_with_violations` counts only BLOCKING ones, so a
// warning-only invoice is counted clean AND listed. "(3)" beside a strip reading
// `invoices_with_violations 0` is exactly that conflation.

import { reportSummary, structuralErrorRows, violationRows } from '../lib/importReport'
import type { ReactNode } from 'react'
import type { PlatformCtx } from '../types'

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="label" style={{ marginBottom: 4 }}>
        {label}
      </div>
      <div className="mono" style={{ fontSize: 15, fontWeight: 600 }}>{value}</div>
    </div>
  )
}

function Section({ title, subtitle, children }: { title: string; subtitle: string; children: ReactNode }) {
  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', overflow: 'hidden' }}>
      <div style={{ padding: '12px 18px', borderBottom: '1px solid var(--line-1)' }}>
        <div style={{ fontSize: 14, fontWeight: 600 }}>{title}</div>
        <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 2 }}>{subtitle}</div>
      </div>
      {children}
    </div>
  )
}

export function CreateReport({ ctx }: { ctx: PlatformCtx }) {
  const { report } = ctx
  if (!report) return null

  const summary = reportSummary(report)

  // The server refused the file. Render THAT and nothing else — no counters, no "0 rows
  // valid", no reassuring empty-errors list. Reached in production by uploading a
  // header-only spreadsheet: the service aborts on rows_total === 0 but the handler still
  // answers 201 Created, so the app arrives here with every counter at Go-zero. Showing
  // those zeros would read as a flawless import of nothing ([report-failed-status]).
  if (summary.kind === 'failed') {
    return (
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--status-red-border)', borderRadius: 'var(--radius-xl)', padding: '24px 22px', maxWidth: 720 }}>
        <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--status-red-text)', marginBottom: 8 }}>Import failed</div>
        <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: '0 0 18px', lineHeight: 1.6 }}>
          The server reported this import as failed and created no invoices. This usually means the file held no data rows — a spreadsheet with only a header row, for example. Check the file and start a new import.
        </p>
        <div className="label" style={{ marginBottom: 4 }}>
          Batch id
        </div>
        <div className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', wordBreak: 'break-all' }}>{summary.id}</div>
      </div>
    )
  }

  const structural = structuralErrorRows(report)
  const violations = violationRows(report)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-xl)', padding: '18px 20px' }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(130px, 1fr))', gap: 18 }}>
          <Stat label="Rows valid" value={`${summary.rows_valid} / ${summary.rows_total}`} />
          <Stat label="Ready invoices" value={String(summary.ready_invoices)} />
          <Stat label="Quarantined" value={String(summary.quarantined_invoices)} />
          <Stat label="Invoices clean" value={String(summary.invoices_clean)} />
          <Stat label="With violations" value={String(summary.invoices_with_violations)} />
          {/* null means nothing was evaluated — never rendered as a rule set numbered 0. */}
          <Stat label="Rule set" value={summary.rule_set_version === null ? 'not evaluated' : `v${summary.rule_set_version}`} />
        </div>
      </div>

      <Section title="Structural row errors" subtitle="Rows the import could not read">
        {structural.length === 0 ? (
          <div style={{ padding: '14px 18px', fontSize: 13, color: 'var(--fg-3)' }}>None — every row was readable.</div>
        ) : (
          structural.map((e, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'flex-start', gap: 14, padding: '12px 18px', borderTop: i === 0 ? 'none' : '1px solid var(--line-1)' }}>
              <span className="mono" style={{ flex: 'none', width: 110, fontSize: 11.5, color: 'var(--fg-3)', paddingTop: 1 }}>{e.rowLabel}</span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 13, color: 'var(--fg-1)' }}>{e.message}</div>
                <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>
                  {[e.field, e.rule_key, e.severity].filter((v) => v !== null).join(' · ')}
                </div>
              </div>
            </div>
          ))
        )}
      </Section>

      <Section title="Rule violations" subtitle="Invoices that were read successfully but failed a rule">
        {violations.length === 0 ? (
          <div style={{ padding: '14px 18px', fontSize: 13, color: 'var(--fg-3)' }}>None — no invoice tripped a rule.</div>
        ) : (
          violations.map((v, i) => {
            // Clickable only when the server sent an invoice id. Without one there is
            // nothing to open, and the invoice NUMBER is not a substitute — the detail
            // view is addressed by id.
            const openId = v.invoice_id
            const clickable = openId !== null
            return (
              <div
                key={i}
                onClick={openId !== null ? () => ctx.openImportedInvoice(openId) : undefined}
                style={{ display: 'flex', alignItems: 'flex-start', gap: 14, padding: '12px 18px', borderTop: i === 0 ? 'none' : '1px solid var(--line-1)', cursor: clickable ? 'pointer' : 'default' }}
              >
                <div style={{ flex: 'none', width: 110 }}>
                  <div className="mono" style={{ fontSize: 11.5, fontWeight: 600, color: clickable ? 'var(--accent)' : 'var(--fg-2)' }}>{v.invoice_number}</div>
                  {/* Joined, never a min–max range: the server reports the rows it saw,
                      and a range would assert the ones between them too. */}
                  <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', marginTop: 2 }}>
                    {v.rows.join(', ')}
                  </div>
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 13, color: 'var(--fg-1)' }}>{v.message}</div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>
                    {/* severity VERBATIM — no blocked/clean label is derived from it here. */}
                    {[v.rule_key, v.severity, v.path].filter((x) => x !== null).join(' · ')}
                  </div>
                </div>
              </div>
            )
          })
        )}
      </Section>
    </div>
  )
}
