import { APP_PATHS } from './appPaths.ts'
import type { Feature, Group, RawFeature, RawGroup, Scene, Stage, TourStop, ViewId } from './types.ts'

export const COMING_SOON_IDS: readonly string[] = ['learns', 'fiscal-outcomes', 'reports', 'contacts', 'company-profile',
  'rule-library', 'custom-rules', 'invite-members', 'erp-connectors', 'alerts', 'channels']

// Positional parameters match the prototype so the data block pastes in unchanged.
const F = (id: string, title: string, short: string, desc: string, benefits: string[], who: string[], view: ViewId, rel: string[], sc: Scene): RawFeature => ({ id, title, short, desc, benefits, who, view, rel, sc, ...(COMING_SOON_IDS.includes(id) ? { status: 'soon' as const } : { status: 'shipped' as const, path: APP_PATHS[view] }) })

const RAW_GROUPS: RawGroup[] = [
      { id: 'invoices', n: '01', name: 'Invoices', icon: 'file-text', view: 'invoices', one: 'Import, create and track every invoice.', intro: 'Bring invoices in from your accounting system or enter them by hand, then follow each one through its statuses until it is cleared.', feats: [
        F('import-files', 'Import from CSV or Excel', 'Upload an export and get one invoice per row.', 'Upload a file from your accounting system. ASComply reads the columns, matches them to invoice fields and creates one invoice per row. Column mappings are remembered for the next import.', ['Works with exports from Odoo, Sage, QuickBooks, SAP and Microsoft Dynamics', 'Column mapping is saved for the next import', 'Rows that cannot be read are listed with the reason'], ['Finance officer', 'Accountant'], 'create', ['read-documents', 'validate', 'contacts'],
          { kind: 'list', win: 'Import · sahara-foods-june.csv', cols: ['Row', 'Customer', 'Amount', 'Status'], grid: '0.5fr 2fr 1.2fr 1.2fr', show0: 0, rows: [['1', 'Lagos Freight & Logistics Ltd', '₦ 4,820,000', 'new'], ['2', 'Honeywell Group', '₦ 12,450,000', 'new'], ['3', 'Nigerian Delta Supplies Co.', '₦ 960,500', 'new'], ['4', 'Adeyemi & Sons Trading', '₦ 2,115,000', 'new']], steps: [
            { cap: 'Upload the export from your accounting system', banner: 'sahara-foods-june.csv · 42 rows' },
            { cap: 'Columns are matched to invoice fields', banner: 'Mapped 9 of 9 columns', show: 2, focus: 0 },
            { cap: 'One invoice is created per row', show: 4, focus: 3, set: { 0: 'valid', 1: 'valid', 2: 'valid' } },
            { cap: 'Rows that cannot be read are flagged with a reason', focus: 3, set: { 3: 'error' } }] }),
        F('create-invoice', 'Create an invoice by hand', 'One form for customer, line items and tax.', 'Enter an invoice in a single form with customer, line items and tax. Fields are checked as you type, so errors are caught before the invoice is saved.', ['Customer details are picked from your contacts', 'VAT is calculated per line', 'Checks run before you save'], ['Finance officer'], 'create', ['validate', 'contacts', 'invoice-status'],
          { kind: 'form', win: 'New invoice', fields: [{ l: 'Customer', v: 'Sahara Foods Distribution Ltd' }, { l: 'Customer TIN', v: '19847720-0001' }, { l: 'Line item', v: 'Palm oil, 25 L × 120' }, { l: 'Taxable amount', v: '₦ 3,600,000' }, { l: 'VAT (7.5%)', v: '₦ 270,000' }], steps: [
            { cap: 'Pick the customer from your contacts', reveal: 2, focus: 0 },
            { cap: 'Add line items; tax is calculated per line', reveal: 5, focus: 2 },
            { cap: 'Fields are checked as you type', focus: -1, mark: { 1: 'ok', 3: 'ok', 4: 'ok' }, msg: '12 fields checked · 0 errors', tone: 'ok' }] }),
        F('invoice-status', 'Track every invoice', 'One list, one clear status per invoice.', 'See each invoice and where it stands: draft, needing attention, awaiting approval, submitted or cleared. Filter by status to see what needs action today.', ['One list for all clients or a single company', 'Status history on every invoice', 'Sidebar counts show what needs attention'], ['Finance officer', 'Accountant'], 'invoices', ['approval-queue', 'fiscal-outcomes', 'audit-trail'],
          { kind: 'list', win: 'Invoices · Sahara Foods', cols: ['Invoice', 'Customer', 'Amount', 'Status'], grid: '1.2fr 2fr 1.2fr 1.3fr', rows: [['INV-2026-0412', 'Lagos Freight & Logistics Ltd', '₦ 4,820,000', 'draft'], ['INV-2026-0413', 'Honeywell Group', '₦ 12,450,000', 'draft'], ['INV-2026-0414', 'Nigerian Delta Supplies Co.', '₦ 960,500', 'valid'], ['INV-2026-0415', 'Adeyemi & Sons Trading', '₦ 2,115,000', 'cleared']], steps: [
            { cap: 'Every invoice has one clear status', focus: 0 },
            { cap: 'An invoice that fails a rule is marked for attention', focus: 1, set: { 1: 'error' } },
            { cap: 'Fix it and it moves forward', focus: 1, set: { 1: 'valid' } },
            { cap: 'Cleared invoices are locked and archived', focus: 0, set: { 0: 'cleared' } }] })] },
      { id: 'recognition', n: '02', name: 'Document recognition', icon: 'file-search', view: 'invoices', one: 'Read PDFs and scans into invoice fields.', intro: 'Upload a PDF, scan or photo and ASComply reads the page. You review what it is unsure about, and it learns each supplier layout from your corrections.', feats: [
        F('read-documents', 'Read PDFs and scans', 'Fields are located and filled in for you.', 'Upload a supplier or sales invoice as a PDF or image. ASComply reads the page, finds the invoice fields and fills the invoice form for you.', ['PDFs, scans and photos are accepted', 'Each value is linked to its place on the page', 'No retyping of numbers, dates or TINs'], ['Finance officer', 'Accountant'], 'invoices', ['review-fields', 'learns', 'import-files'],
          { kind: 'form', doc: true, win: 'Document · adeyemi-0388.pdf', fields: [{ l: 'Invoice number', v: 'INV-2026-0388' }, { l: 'Supplier', v: 'Adeyemi & Sons Trading' }, { l: 'Invoice date', v: '14 Sep 2026' }, { l: 'Total', v: '₦ 2,115,000' }, { l: 'VAT', v: '₦ 147,558' }], steps: [
            { cap: 'Upload a PDF, scan or photo', reveal: 0, focus: -1 },
            { cap: 'The page is read and the fields are located', reveal: 2, focus: 0 },
            { cap: 'Values are filled into the invoice form', reveal: 5, focus: 3, mark: { 0: 'ok', 1: 'ok', 2: 'ok', 3: 'ok', 4: 'ok' } }] }),
        F('review-fields', 'Review low-confidence fields', 'Uncertain values are flagged for a quick check.', 'Fields the reader is unsure about are flagged. You see the source region next to the value and confirm or correct it before the invoice moves on.', ['Only uncertain fields need your attention', 'Source region shown beside each value', 'Nothing moves on until flagged fields are confirmed'], ['Finance officer'], 'invoices', ['read-documents', 'learns', 'validate'],
          { kind: 'form', doc: true, win: 'Review · adeyemi-0388.pdf', fields: [{ l: 'Invoice number', v: 'INV-2026-0388' }, { l: 'Supplier', v: 'Adeyemi & Sons Trading' }, { l: 'Invoice date', v: '14 Sep 2026' }, { l: 'Total', v: '₦ 2,115,000' }, { l: 'VAT', v: '₦ 147,558' }], steps: [
            { cap: 'Uncertain fields are flagged', reveal: 5, focus: 2, mark: { 0: 'ok', 1: 'ok', 2: 'low', 3: 'low', 4: 'ok' } },
            { cap: 'Compare the value with the source region', focus: 3 },
            { cap: 'Confirm or correct, then continue', focus: -1, mark: { 2: 'ok', 3: 'ok' }, msg: '2 fields confirmed', tone: 'ok' }] }),
        F('learns', 'Corrections teach the reader', 'Each supplier layout is remembered.', 'When you correct a field, the position is remembered for that supplier\'s layout. The next invoice from the same supplier is read correctly the first time.', ['Fewer reviews with every invoice from a supplier', 'Learning is per supplier layout', 'Corrections are recorded in the audit trail'], ['Finance officer', 'Accountant'], 'invoices', ['read-documents', 'review-fields'],
          { kind: 'form', doc: true, win: 'Supplier layout · Adeyemi & Sons', fields: [{ l: 'Invoice number', v: 'INV-2026-0388' }, { l: 'Supplier', v: 'Adeyemi & Sons Trading' }, { l: 'Invoice date', v: '14 Sep 2026' }, { l: 'Total', v: '₦ 2,115,000' }, { l: 'VAT', v: '₦ 147,558' }], steps: [
            { cap: 'Correct a field once', reveal: 5, focus: 3, mark: { 0: 'ok', 1: 'ok', 2: 'ok', 3: 'fix', 4: 'ok' } },
            { cap: 'The layout is remembered for that supplier', focus: 3, msg: 'Layout saved for Adeyemi & Sons Trading', tone: 'info' },
            { cap: 'The next invoice reads without review', focus: -1, mark: { 3: 'ok' }, msg: '5 fields read · 0 flagged', tone: 'ok' }] })] },
      { id: 'rules', n: '03', name: 'Rules & validation', icon: 'shield-check', view: 'rules', one: 'Check every invoice against a sealed rule set.', intro: 'Invoices are checked before they reach FIRS. Failed checks say what to change, and you can add rules specific to your company.', feats: [
        F('rule-library', 'A rule set for every invoice', 'Versioned rules that never change after the fact.', 'Invoices are checked against a versioned rule set covering mandatory fields, TIN format, tax calculations and the e-invoicing schema. Each version is sealed once active, so past results never change.', ['Rules cover fields, TINs, tax and schema', 'Sealed versions keep past results stable', 'A single rule can be paused without a new version'], ['Compliance lead', 'Tax adviser'], 'rules', ['validate', 'custom-rules', 'audit-trail'],
          { kind: 'toggles', win: 'Rules · Standard set v4', rows: [['Seller TIN is present and correctly formatted', 'Mandatory field', true], ['Buyer name and address are present', 'Mandatory field', true], ['VAT equals 7.5% of the taxable amount', 'Tax calculation', true], ['Line totals add up to the invoice total', 'Tax calculation', true], ['Issue date is not in the future', 'Schema', true]], steps: [
            { cap: 'Rules are grouped in a versioned set', focus: 0 },
            { cap: 'An admin can pause a single rule', focus: 4, flip: { 4: false } },
            { cap: 'The change is recorded in the audit log', focus: 4, flip: { 4: true } }] }),
        F('validate', 'Plain validation messages', 'Each failed check says what to change.', 'Each failed check names the field that is wrong and says what to change, in plain language. Your team fixes the invoice without reading a schema.', ['Message names the field and the expected value', 'Checks re-run as soon as you edit', 'Errors are caught before submission'], ['Finance officer', 'Accountant'], 'invoices', ['rule-library', 'review-fields', 'approval-queue'],
          { kind: 'form', win: 'Validation · INV-2026-0413', fields: [{ l: 'Seller TIN', v: '2018441-0001', fix: '20184412-0001' }, { l: 'Buyer', v: 'Honeywell Group' }, { l: 'Taxable amount', v: '₦ 11,581,395' }, { l: 'VAT (7.5%)', v: '₦ 868,605' }, { l: 'Total', v: '₦ 12,450,000' }], steps: [
            { cap: 'A failed check names the field', reveal: 5, focus: 0, mark: { 0: 'err', 1: 'ok', 2: 'ok', 3: 'ok', 4: 'ok' }, msg: 'Seller TIN has 7 digits. Expected 8 digits followed by -0001.', tone: 'err' },
            { cap: 'Edit the field and the check re-runs', focus: 0, mark: { 0: 'fix' } },
            { cap: 'The invoice is ready to go forward', focus: -1, mark: { 0: 'ok' }, msg: '12 fields checked · 0 errors', tone: 'ok' }] }),
        F('custom-rules', 'Add your own rules', 'Company-specific checks on top of the standard set.', 'Add company-specific checks on top of the standard set, such as a purchase order reference that must be present above a set amount.', ['Conditions by customer, amount or field', 'Choose whether a rule warns or blocks', 'Takes effect on the next validation run'], ['Compliance lead'], 'rules', ['rule-library', 'validate'],
          { kind: 'form', win: 'New rule', fields: [{ l: 'Rule name', v: 'PO reference required' }, { l: 'Applies to', v: 'Customer: Honeywell Group' }, { l: 'Condition', v: 'Amount above ₦ 5,000,000' }, { l: 'Check', v: 'PO reference is present' }, { l: 'Severity', v: 'Blocks submission' }], steps: [
            { cap: 'Name the rule and choose who it applies to', reveal: 2, focus: 1 },
            { cap: 'Set the condition and the check', reveal: 4, focus: 3 },
            { cap: 'Choose whether it warns or blocks', reveal: 5, focus: 4, mark: { 4: 'ok' }, msg: 'Rule active for the next validation run', tone: 'ok' }] })] },
      { id: 'approvals', n: '04', name: 'Approvals & workflows', icon: 'workflow', view: 'approvals', one: 'Route invoices to the right approver.', intro: 'Set who approves what, and work through a single queue. Every decision is recorded and the submitter is told the outcome.', feats: [
        F('approval-queue', 'One queue for approvals', 'Approve, reject with a reason, or reassign.', 'Invoices waiting on you are in one queue with the amount, customer and the step you are on. Approve, reject with a reason, or reassign.', ['One list of everything waiting on you', 'Rejections carry a reason back to the submitter', 'Each decision is time-stamped'], ['Approver', 'Finance director'], 'approvals', ['workflow-builder', 'alerts', 'roles'],
          { kind: 'list', win: 'Approvals · Awaiting you', cols: ['Invoice', 'Customer', 'Amount', 'Status'], grid: '1.2fr 2fr 1.2fr 1.4fr', rows: [['INV-2026-0412', 'Lagos Freight & Logistics Ltd', '₦ 4,820,000', 'pending'], ['INV-2026-0413', 'Honeywell Group', '₦ 12,450,000', 'pending'], ['INV-2026-0416', 'Nigerian Delta Supplies Co.', '₦ 960,500', 'pending']], steps: [
            { cap: 'Invoices waiting for your decision are queued', focus: 0 },
            { cap: 'Open an invoice and review its checks', focus: 1 },
            { cap: 'Approve it, and it moves to the next step', focus: 1, set: { 1: 'approved' } },
            { cap: 'Or reject with a reason; the submitter is notified', focus: 2, set: { 2: 'rejected' } }] }),
        F('workflow-builder', 'Build an approval workflow', 'Steps, approvers and amount thresholds.', 'Set the steps an invoice passes through, who approves each one and the amount that triggers it. Test a workflow with a sample amount before turning it on.', ['Conditions by amount, customer or client', 'Approvers by person or by role', 'Try a sample amount before going live'], ['Finance director', 'Firm partner'], 'workflows', ['approval-queue', 'roles', 'submit-clear'],
          { kind: 'flow', win: 'Workflow · Over ₦ 1m', nodes: [['Submitted', 'Finance officer'], ['Amount check', 'Over ₦ 1,000,000'], ['Step 1', 'Accountant'], ['Step 2', 'Finance director'], ['Released', 'Ready to clear']], steps: [
            { cap: 'An invoice enters the workflow when submitted', at: 0, out: 'INV-2026-0412 · ₦ 4,820,000 submitted' },
            { cap: 'A condition decides which path it takes', at: 1, out: 'Amount is over the threshold, two approvals needed' },
            { cap: 'Each step has an approver or a role', at: 3, out: 'Step 1 approved · waiting on Finance director' },
            { cap: 'The last approval releases the invoice for clearance', at: 4, out: 'Approved at step 2 · released for clearance' }] })] },
      { id: 'clearance', n: '05', name: 'FIRS clearance & submission', icon: 'send', view: 'invoices', one: 'Send approved invoices and record the outcome.', intro: 'Approved invoices are submitted for clearance and tracked until FIRS responds. Failures are retried, and rejections show the reason.', feats: [
        F('submit-clear', 'Submit for FIRS clearance', 'Submission is tracked to a recorded outcome.', 'Approved invoices are sent for clearance. ASComply tracks the submission and records the fiscal outcome on the invoice when FIRS responds.', ['Submission status visible on every invoice', 'Clearance reference stored with the invoice', 'No manual re-keying into another portal'], ['Finance officer', 'Compliance lead'], 'invoices', ['fiscal-outcomes', 'evidence-bundle', 'erp-connectors'],
          { kind: 'flow', win: 'Submission · INV-2026-0412', nodes: [['Approved', 'Released'], ['Prepared', 'Required format'], ['Submitted', 'Sent to FIRS'], ['Response', 'Awaiting FIRS'], ['Cleared', 'Reference stored']], steps: [
            { cap: 'The approved invoice is prepared in the required format', at: 1, out: 'Invoice converted and signed' },
            { cap: 'It is submitted to FIRS', at: 2, out: 'Submitted · waiting for a response' },
            { cap: 'The response is picked up automatically', at: 3, out: 'Response received from FIRS' },
            { cap: 'The clearance outcome is recorded on the invoice', at: 4, out: 'Cleared · reference stored on the invoice' }] }),
        F('fiscal-outcomes', 'Outcomes and retries', 'Failures retry; rejections explain themselves.', 'Rejected or failed submissions show the reason from FIRS. Fix the invoice and resubmit; temporary connection failures are retried automatically.', ['Reason shown for every rejection', 'Connection failures retry without action', 'Resubmit from the invoice itself'], ['Finance officer', 'Compliance lead'], 'invoices', ['submit-clear', 'alerts', 'audit-trail'],
          { kind: 'list', win: 'Submissions · This week', cols: ['Invoice', 'Customer', 'Amount', 'Status'], grid: '1.2fr 2fr 1.2fr 1.2fr', rows: [['INV-2026-0409', 'Lagos Freight & Logistics Ltd', '₦ 1,640,000', 'sent'], ['INV-2026-0410', 'Honeywell Group', '₦ 8,200,000', 'sent'], ['INV-2026-0411', 'Adeyemi & Sons Trading', '₦ 735,000', 'sent']], steps: [
            { cap: 'Submitted invoices are tracked until FIRS responds', focus: 0 },
            { cap: 'A connection failure is retried automatically', focus: 1, set: { 1: 'failed' }, banner: 'Connection to FIRS timed out · retrying' },
            { cap: 'A rejection shows the reason', focus: 2, set: { 1: 'cleared', 2: 'rejected' }, banner: 'Rejected: buyer TIN not found' },
            { cap: 'Fix the invoice and resubmit', focus: 2, set: { 2: 'cleared' } }] })] },
      { id: 'reports', n: '06', name: 'Reports & analytics', icon: 'chart-column', view: 'dashboard', one: 'Readiness and results at a glance.', intro: 'See how many invoices passed, what needs attention and how a client or a whole portfolio is progressing.', feats: [
        F('overview', 'Readiness overview', 'Totals, pass rate and items needing attention.', 'A dashboard of invoices processed, pass rate and items needing attention, for one company or the whole portfolio.', ['Pass rate over time', 'Counts that link to the invoices behind them', 'One company or all clients'], ['Finance director', 'Firm partner'], 'dashboard', ['reports', 'portfolio', 'invoice-status'],
          { kind: 'metrics', win: 'Overview · Sahara Foods · June', tiles: [['Invoices', 482, ''], ['Pass rate', 94, '%'], ['Needing attention', 12, '']], bars: [0.35, 0.5, 0.45, 0.65, 0.72, 0.85, 0.95], steps: [
            { cap: 'Totals for the selected period', grow: 0.4 },
            { cap: 'The pass rate rises as errors are fixed', grow: 0.8 },
            { cap: 'Drill into what still needs attention', grow: 1 }] }),
        F('reports', 'Reports you can export', 'A summary for your records or your adviser.', 'Choose a period and a company, then download the compliance summary as a file for your records or your adviser.', ['Cleared, rejected and pending counts', 'Choose the period and the company', 'Download as a file'], ['Tax adviser', 'Accountant'], 'reports', ['overview', 'evidence-bundle'],
          { kind: 'metrics', win: 'Reports · Compliance summary', tiles: [['Cleared', 438, ''], ['Rejected', 9, ''], ['Awaiting approval', 35, '']], bars: [0.5, 0.6, 0.55, 0.7, 0.8, 0.75, 0.9], steps: [
            { cap: 'Choose a period and a company', grow: 0.3 },
            { cap: 'Results are summarised by outcome', grow: 0.7 },
            { cap: 'Download the summary for your records', grow: 1 }] })] },
      { id: 'audit', n: '07', name: 'Audit & evidence', icon: 'archive', view: 'audit', one: 'A record of every action, ready for an auditor.', intro: 'Every import, edit, approval and submission is recorded. An evidence bundle packages one invoice with everything an auditor asks for.', feats: [
        F('audit-trail', 'Audit trail', 'Who did what, and when.', 'Every action on an invoice is recorded with who did it and when: imports, edits, approvals and submissions. Entries cannot be edited.', ['Entries cannot be edited or removed', 'Filter by invoice, person or action', 'System actions are recorded alongside people'], ['Compliance lead', 'Tax adviser'], 'audit', ['evidence-bundle', 'roles'],
          { kind: 'feed', win: 'Audit · INV-2026-0412', items: [['14:02', 'Imported from sahara-foods-june.csv', 'Finance officer', 'g'], ['14:05', 'Field corrected: Seller TIN', 'Finance officer', 'a'], ['14:09', 'Approved at step 2', 'Finance director', 'g'], ['14:12', 'Submitted to FIRS', 'System', 'm'], ['14:13', 'Cleared by FIRS', 'System', 'g']], steps: [
            { cap: 'Each action is recorded as it happens', show: 2 },
            { cap: 'Approvals show who decided and when', show: 3 },
            { cap: 'System actions are recorded too', show: 5 }] }),
        F('evidence-bundle', 'Evidence bundle', 'One invoice, everything an auditor needs.', 'Export one invoice with its original document, validation results, approvals and clearance response as a single package for an auditor.', ['Original document included', 'Validation, approvals and clearance in one file', 'Nothing to assemble by hand'], ['Compliance lead', 'Tax adviser'], 'audit', ['audit-trail', 'submit-clear'],
          { kind: 'flow', win: 'Evidence · INV-2026-0412', nodes: [['Source document', 'PDF as received'], ['Validation', 'Results and rule version'], ['Approvals', 'Who and when'], ['Clearance', 'FIRS response'], ['Bundle', 'Ready to download']], steps: [
            { cap: 'The original document is attached', at: 0, out: 'adeyemi-0388.pdf added' },
            { cap: 'Validation results and the rule version are added', at: 1, out: 'Rule set v4 · 12 checks · 0 errors' },
            { cap: 'Approvals and the clearance response follow', at: 3, out: '2 approvals and the FIRS response added' },
            { cap: 'Download the bundle as one package', at: 4, out: 'evidence-INV-2026-0412 ready to download' }] })] },
      { id: 'clients', n: '08', name: 'Clients & firm portfolio', icon: 'building-2', view: 'clients', one: 'Run many clients from one place.', intro: 'Accounting and tax firms see every client in a single portfolio, onboard new ones, and keep customer contacts in sync.', feats: [
        F('portfolio', 'Portfolio of clients', 'Readiness and volume across every client.', 'Accounting and tax firms see all clients in one list with readiness scores, invoice volume and what needs attention. Switch into any client from the list.', ['Readiness score per client', 'Invoice volume at a glance', 'Switch into a client in one click'], ['Firm partner', 'Accountant'], 'clients', ['onboard-client', 'overview', 'contacts'],
          { kind: 'list', win: 'Clients · Portfolio', cols: ['Client', 'Readiness', 'Invoices', 'Status'], grid: '2.2fr 1fr 1fr 1.3fr', rows: [['Lagos Freight & Logistics Ltd', '94', '60', 'ready'], ['Sahara Foods Distribution Ltd', '87', '42', 'ready'], ['Nigerian Delta Supplies Co.', '71', '24', 'progress'], ['Adeyemi & Sons Trading', '63', '14', 'progress'], ['Kano Textile Mills Plc', '–', '0', 'none']], steps: [
            { cap: 'Every client in one portfolio', focus: 0 },
            { cap: 'Readiness shows who needs help first', focus: 3 },
            { cap: 'A new client starts with no invoices yet', focus: 4 }] }),
        F('onboard-client', 'Add a client', 'Set up a company in one form.', 'Add a client with its legal name, TIN, taxpayer size and accounting system. The client gets its own workspace, rules and approvals.', ['Own workspace, rules and approvals per client', 'Taxpayer size sets sensible defaults', 'Connect the accounting system afterwards'], ['Firm partner'], 'clients', ['portfolio', 'invite-members', 'erp-connectors'],
          { kind: 'form', win: 'Add a client', fields: [{ l: 'Company name', v: 'Kano Textile Mills Plc' }, { l: 'TIN', v: '18772300-0001' }, { l: 'Taxpayer size', v: 'Medium' }, { l: 'Accounting system', v: 'Sage' }], steps: [
            { cap: 'Enter the company name and TIN', reveal: 2, focus: 1, mark: { 1: 'ok' } },
            { cap: 'Choose the taxpayer size and accounting system', reveal: 4, focus: 3 },
            { cap: 'The client gets its own workspace', focus: -1, mark: { 0: 'ok', 2: 'ok', 3: 'ok' }, msg: 'Workspace created for Kano Textile Mills Plc', tone: 'ok' }] }),
        F('contacts', 'Customer and supplier contacts', 'Contacts sync from your accounting system.', 'Customers and suppliers sync from your accounting system and are checked for a valid TIN. Invoices pick up the contact details automatically.', ['Syncs from the accounting system', 'TINs are checked when a contact arrives', 'Invoices reuse the saved details'], ['Finance officer', 'Accountant'], 'customers', ['create-invoice', 'import-files', 'portfolio'],
          { kind: 'list', win: 'Customers · Sahara Foods', cols: ['Contact', 'TIN', 'Invoices', 'Status'], grid: '2.2fr 1.4fr 0.8fr 1.2fr', show0: 0, rows: [['Lagos Freight & Logistics Ltd', '20184412-0001', '18', 'new'], ['Honeywell Group', '20665510-0001', '12', 'new'], ['Adeyemi & Sons Trading', '2099104-0001', '7', 'new']], steps: [
            { cap: 'Contacts sync from your accounting system', show: 3, banner: 'Synced 3 contacts from Sage' },
            { cap: 'Each TIN is checked as it arrives', focus: 2, set: { 0: 'valid', 1: 'valid', 2: 'error' } },
            { cap: 'Correct the contact and invoices pick it up', focus: 2, set: { 2: 'valid' } }] })] },
      { id: 'members', n: '09', name: 'Members & roles', icon: 'users', view: 'settings', one: 'Invite your team and set what each role can do.', intro: 'Invite colleagues, give each a role, and decide which actions that role can take.', feats: [
        F('invite-members', 'Invite your team', 'Add a colleague with a role and client access.', 'Invite a colleague by email, choose a role and the clients they can see. They join with their own sign-in.', ['Role and client access set at invitation', 'Each person signs in individually', 'Access can be changed or removed later'], ['Admin', 'Firm partner'], 'settings', ['roles', 'onboard-client'],
          { kind: 'form', win: 'Invite a member', fields: [{ l: 'Email', v: 'accounts@saharafoods.example' }, { l: 'Role', v: 'Approver' }, { l: 'Client access', v: 'Sahara Foods Distribution Ltd' }], steps: [
            { cap: 'Enter the colleague\'s email', reveal: 1, focus: 0 },
            { cap: 'Choose a role and the client they can see', reveal: 3, focus: 1 },
            { cap: 'The invitation is sent', focus: -1, mark: { 0: 'ok', 1: 'ok', 2: 'ok' }, msg: 'Invitation sent to accounts@saharafoods.example', tone: 'ok' }] }),
        F('roles', 'Roles and permissions', 'Decide what each role can do.', 'Each role has a set of permissions: viewing, creating, approving, submitting and managing. Approvers can approve without being able to submit.', ['Separate approving from submitting', 'Admins alone manage members and connectors', 'Changes appear in the audit trail'], ['Admin', 'Compliance lead'], 'settings', ['invite-members', 'approval-queue'],
          { kind: 'toggles', win: 'Roles · Approver', rows: [['View invoices', 'Read access', true], ['Create and import', 'Write access', false], ['Approve invoices', 'Workflow', true], ['Submit for clearance', 'Workflow', true], ['Manage members', 'Administration', false]], steps: [
            { cap: 'Each role has a set of permissions', focus: 0 },
            { cap: 'Approvers can approve but not submit', focus: 3, flip: { 3: false } },
            { cap: 'Only admins manage members', focus: 4 }] })] },
      { id: 'notifications', n: '10', name: 'Notifications', icon: 'bell', view: 'settings', one: 'Hear about what needs you.', intro: 'Approval requests, rejections and clearance results reach the right person. Each person chooses what they hear about.', feats: [
        F('alerts', 'Alerts where you work', 'The right person hears about each event.', 'Approvers are told when an invoice needs them, submitters when one is rejected, and everyone when an import finishes or a clearance result arrives.', ['Approval requests reach the approver', 'Rejections reach the submitter with the reason', 'Import and clearance results are announced'], ['Approver', 'Finance officer'], 'settings', ['channels', 'approval-queue'],
          { kind: 'feed', win: 'Notifications', items: [['09:41', 'INV-2026-0412 needs your approval', 'Step 2 · ₦ 4,820,000', 'a'], ['09:58', 'INV-2026-0398 was rejected', 'Buyer TIN not found', 'r'], ['10:20', 'Import finished', '42 rows · 3 need attention', 'm'], ['10:34', 'INV-2026-0371 cleared by FIRS', 'Reference stored', 'g']], steps: [
            { cap: 'Approvers are told when an invoice needs them', show: 1 },
            { cap: 'Submitters see rejections with the reason', show: 2 },
            { cap: 'Imports and clearance results are announced', show: 4 }] }),
        F('channels', 'Choose what you hear about', 'Each person sets their own alerts.', 'Each person chooses which events notify them, so a busy approver is not told about every import.', ['Per-person preferences', 'Weekly readiness summary is optional', 'Defaults suit each role'], ['Everyone'], 'settings', ['alerts', 'roles'],
          { kind: 'toggles', win: 'Notification preferences', rows: [['Approval requests', 'When an invoice needs you', true], ['Rejections', 'When your invoice is rejected', true], ['Clearance results', 'When FIRS responds', true], ['Import finished', 'After each file import', true], ['Weekly readiness summary', 'Every Monday', false]], steps: [
            { cap: 'Each person has their own preferences', focus: 0 },
            { cap: 'Switch off events you do not need', focus: 3, flip: { 3: false } },
            { cap: 'Opt in to a weekly summary', focus: 4, flip: { 4: true } }] })] },
      { id: 'settings', n: '11', name: 'Settings & integrations', icon: 'plug', view: 'settings', one: 'Connect your accounting system and company details.', intro: 'ASComply sits beside your accounting system and does not replace it. Connect it, and keep the company details and signing certificate up to date.', feats: [
        F('erp-connectors', 'Connect your accounting system', 'Invoices flow in; results flow back.', 'Link Odoo, Sage, QuickBooks, SAP or Microsoft Dynamics so invoices flow in and clearance results flow back. ASComply does not replace the accounting system.', ['Invoices flow in without exports', 'Clearance results flow back to the ledger', 'Your accounting system stays the source of truth'], ['Admin', 'ERP consultant'], 'settings', ['import-files', 'submit-clear'],
          { kind: 'flow', win: 'Connector · Sage', nodes: [['Accounting system', 'Sage'], ['ASComply', 'Invoices received'], ['Validate and approve', 'Rules and workflow'], ['FIRS', 'Clearance'], ['Result back', 'Written to the ledger']], steps: [
            { cap: 'Invoices are received from your accounting system', at: 1, out: '42 invoices received from Sage' },
            { cap: 'They are validated and approved here', at: 2, out: '39 passed · 3 need attention' },
            { cap: 'Cleared invoices go to FIRS', at: 3, out: '39 submitted for clearance' },
            { cap: 'The result is written back to the ledger', at: 4, out: 'Clearance references written to Sage' }] }),
        F('company-profile', 'Company and signing certificate', 'Details that go on every submission.', 'Keep the legal name, TIN, taxpayer size and signing certificate current. They are applied to every submission for the company.', ['One place for company details', 'Certificate expiry is visible', 'Applied to every submission'], ['Admin'], 'settings', ['erp-connectors', 'submit-clear'],
          { kind: 'form', win: 'Settings · Company', fields: [{ l: 'Legal name', v: 'Sahara Foods Distribution Ltd' }, { l: 'TIN', v: '19847720-0001' }, { l: 'Taxpayer size', v: 'Medium' }, { l: 'Signing certificate', v: 'Uploaded · valid to Mar 2027' }], steps: [
            { cap: 'Company details are kept in one place', reveal: 3, focus: 0 },
            { cap: 'Upload the signing certificate', reveal: 4, focus: 3 },
            { cap: 'Details are applied to every submission', focus: -1, mark: { 0: 'ok', 1: 'ok', 2: 'ok', 3: 'ok' }, msg: 'Certificate valid · 4 fields saved', tone: 'ok' }] })] }
]

export const GROUPS: Group[] = RAW_GROUPS.map((g) => ({ ...g, feats: g.feats.map((f): Feature => ({ ...f, gid: g.id })) }))
export const FEATURES: Feature[] = GROUPS.flatMap((g) => g.feats)
export const STAGES: Stage[] = [['Import', 'invoices'], ['Extract', 'recognition'], ['Validate', 'rules'], ['Approve', 'approvals'], ['Clear', 'clearance'], ['Archive', 'audit']]
export const TOUR: TourStop[] = [
  { g: 'invoices', f: 'import-files', stage: 0, t: 'Start with the data you already have', d: 'Import a file from your accounting system. Each row becomes an invoice, and unreadable rows are listed with the reason.' },
  { g: 'recognition', f: 'read-documents', stage: 1, t: 'Read PDFs and scans', d: 'Upload a document and the invoice fields are located and filled in for you.' },
  { g: 'rules', f: 'validate', stage: 2, t: 'Check every invoice', d: 'Each failed check names the field and says what to change.' },
  { g: 'approvals', f: 'approval-queue', stage: 3, t: 'Approve in one queue', d: 'Invoices waiting on you are in one list. Approve, or reject with a reason.' },
  { g: 'clearance', f: 'submit-clear', stage: 4, t: 'Submit for FIRS clearance', d: 'Approved invoices are sent, and the fiscal outcome is recorded on each one.' },
  { g: 'audit', f: 'audit-trail', stage: 5, t: 'Keep the evidence', d: 'Every action is recorded with who did it and when, ready for an auditor.' },
  { g: 'reports', f: 'overview', stage: -1, t: 'See readiness at a glance', d: 'Totals, pass rate and what needs attention, for one company or a whole portfolio.' }
]
