// Command invoice is the 03 Invoice context service. It serves the platform
// kit's /healthz + /readyz plus the /v1/invoices... CRUD + guarded-transition
// routes (M4-02): manual create, read/list, and the single guarded
// transitions endpoint — all resolved under RLS via internal/invoice.Store —
// plus the validate gate (M4-04), which evaluates an invoice against 04's
// active rule set and is the only route to the validated status, and the
// batch submit endpoint (M5-04-07/08), which enqueues validated invoices
// onto the submission worker's queue via a transactional-outbox insert.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/demodocs"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

func main() {
	app, err := platform.New("invoice")
	if err != nil {
		log.Fatalf("invoice: startup: %v", err)
	}

	// The invoice_app (NOBYPASSRLS) connection pool. DATABASE_URL is required — an
	// invoice service that cannot reach its database is misconfigured, not
	// degraded. pgxpool.New is lazy (it connects on first use), so an unreachable
	// DB surfaces via /readyz rather than blocking startup.
	pool, err := db.NewPool(context.Background(), mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("invoice: db pool: %v", err)
	}
	defer pool.Close()

	// Readiness: the app-role pool can round-trip to Postgres. Liveness (/healthz)
	// stays up regardless; /readyz flips to 503 while the DB is unreachable.
	app.Ready("database", func(ctx context.Context) error { return pool.Ping(ctx) })

	// The five DOCUMENT_* variables are required, and the store is built here so
	// an unusable object-storage configuration fails at boot rather than on the
	// first upload.
	docCfg, err := document.ConfigFromEnv()
	if err != nil {
		fatal(app.Logger, "invoice: %v", err)
	}
	docObjects, err := document.NewS3Store(docCfg, nil)
	if err != nil {
		fatal(app.Logger, "invoice: document store: %v", err)
	}

	// Stub endpoint from the M2-04 skeleton — kept as the trivial reachability probe
	// the gateway's proxy tests exercise (/api/invoice/v1/ping). Real endpoints
	// live alongside it.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"invoice","status":"ok"}`))
	})

	// /v1/invoices... — the invoice CRUD + guarded-transition surface, resolved
	// under RLS. Reached via the gateway as /api/invoice/v1/invoices... (the
	// prefix is stripped upstream).
	//
	// APPROVALS_ENFORCED gates enforcement only (docs/approvals.md §11); unset is
	// off and an unparseable value stops the boot. fatal, not log.Fatalf — see
	// fatal's doc comment (TestInvoiceMain_WiresTheApprovalsEnforcedFlag).
	enforced, err := parseEnvBool(os.Getenv("APPROVALS_ENFORCED"))
	if err != nil {
		fatal(app.Logger, "invoice: APPROVALS_ENFORCED must be a boolean: %v", err)
	}
	store := invoice.NewStore(pool, invoice.WithApprovalsEnforced(enforced))
	app.Mux.HandleFunc("POST /v1/invoices", invoice.CreateHandler(store.Create, app.Logger))
	app.Mux.HandleFunc("GET /v1/invoices/{id}", invoice.GetHandler(store.Get, store.CallerRole, store.ApprovalFacts, app.Logger))
	app.Mux.HandleFunc("GET /v1/invoices/{id}/history", invoice.HistoryHandler(store.History, app.Logger))
	app.Mux.HandleFunc("GET /v1/invoices/{id}/source-document", invoice.SourceDocumentHandler(store.SourceDocument, app.Logger))
	app.Mux.HandleFunc("GET /v1/invoices/{id}/ubl", invoice.UBLHandler(store.Get, app.Logger))
	app.Mux.HandleFunc("GET /v1/invoices", invoice.ListHandler(store.List, app.Logger))
	// GET /v1/invoices/violation-summary -- the review screen's failing-rules
	// rail (INVCR-01-07): one row per rule_key over ONE import batch, so the
	// rail is derived from the whole batch instead of the 50 rows on the
	// current page. Registration order relative to GET /v1/invoices/{id} above
	// is IRRELEVANT: Go 1.22+ ServeMux resolves by pattern specificity, and a
	// literal segment always beats a {wildcard}.
	app.Mux.HandleFunc("GET /v1/invoices/violation-summary", invoice.ViolationSummaryHandler(store.ViolationSummary, app.Logger))
	app.Mux.HandleFunc("POST /v1/invoices/{id}/transitions", invoice.TransitionHandler(store.Transition, store.CallerRole, app.Logger))
	app.Mux.HandleFunc("PATCH /v1/invoices/{id}", invoice.EditHandler(store.Edit, app.Logger))
	// POST/DELETE /v1/invoices/{id}/keep-as-is -- D6's auditable-triage write
	// (INVCR-01-15, task-291): never touches status or legalTransitions.
	app.Mux.HandleFunc("POST /v1/invoices/{id}/keep-as-is", invoice.KeepAsIsHandler(store.KeepAsIs, app.Logger))
	app.Mux.HandleFunc("DELETE /v1/invoices/{id}/keep-as-is", invoice.UnkeepAsIsHandler(store.UnkeepAsIs, app.Logger))
	// POST/DELETE /v1/invoices/{id}/resolved-outside -- approver-only mark
	// that a failed invoice was resolved outside the system; never touches
	// status or legalTransitions, same as keep-as-is above.
	app.Mux.HandleFunc("POST /v1/invoices/{id}/resolved-outside", invoice.ResolveOutsideHandler(store.ResolveOutside, app.Logger))
	app.Mux.HandleFunc("DELETE /v1/invoices/{id}/resolved-outside", invoice.UnresolveOutsideHandler(store.UnresolveOutside, app.Logger))

	// POST /v1/invoices/{id}/validate -- THE validate gate ([gate-endpoint],
	// M4-04): the ONLY route by which an invoice reaches validated, and the
	// on-demand re-validate endpoint. Reached via the gateway as
	// /api/invoice/v1/invoices/{id}/validate; the gateway forwards arbitrary
	// subpaths under its generic /api/ prefix, so this route needs no gateway
	// change.
	//
	// Both vars are REQUIRED ([env-wiring]) -- mustEnv log.Fatalf's on an unset
	// one, so an invoice service that cannot reach 04 fails fast at boot rather
	// than serving a surface that silently cannot validate. This service needs
	// its OWN copy of each: Railway vars are per-service, and the gateway's
	// VALIDATION_URL is not inherited here.
	//
	// VALIDATION_URL must carry NO trailing slash -- the client concatenates
	// "/v1/validate/batch" onto it (validator.go).
	validator := invoice.NewValidator(mustEnv("VALIDATION_URL"), mustEnv("S2S_TOKEN"), nil)
	gate := invoice.NewGate(store, validator)
	app.Mux.HandleFunc("POST /v1/invoices/{id}/validate", invoice.ValidateHandler(gate.Validate, app.Logger))

	// /v1/imports -- the bulk CSV/XLSX import surface (M4-03), reusing the SAME
	// *invoice.Store instance above so an import's Create calls run through the
	// identical invoice-write path as the manual endpoints. Reached via the
	// gateway as /api/invoice/v1/imports (same mux, same middleware chain, so
	// identity/tenant are already in context).
	// Reuses the SAME gate constructed above for the single-invoice validate
	// route: importer.NewService's third parameter is an importer-local
	// interface (task-114/M4-04-07's Stage-1 addendum F3) that *invoice.Gate
	// satisfies structurally (its Evaluate/ValidateBatch signatures match
	// exactly) -- no second gate, no adapter type, one gate driving both the
	// manual validate endpoint and the importer's batch pre-check
	// ([import-validates]/[dry-run-evaluates]).
	// /v1/imports/preview sits on the same mux and middleware chain and is the
	// only ROUTE by which a source document reaches storage ([upload-once]): it
	// writes the uploaded bytes to object storage and a row to documents, then
	// previews them. POST /v1/imports takes the id it returns instead of a
	// second copy of the file. (demodocs.Seed below also stores, but off the
	// mux entirely and only for the seed tenants.)
	docSvc := document.NewService(document.NewStore(pool), docObjects)

	// Give the SQL-seeded demo invoices the file they would have been imported
	// from -- db/seed.dev.sql runs in the gateway and cannot reach object
	// storage, so without this every demo invoice reads "no source document".
	// Runs before app.Run, so a green /healthz means the documents exist; the
	// gateway's own /healthz gates the SQL seed that this reads, and dev-env.yml
	// waits for it before deploying this service. Non-fatal: demo evidence is
	// not worth refusing to serve over.
	if res, err := demodocs.Seed(context.Background(), pool, docSvc.Store, app.Logger); err != nil {
		app.Logger.Error("invoice: demo source documents", "error", err,
			"documents", res.DocumentsStored, "invoices", res.InvoicesLinked)
	}
	impStore := importer.NewStore(pool)
	impSvc := importer.NewService(impStore, store, gate)
	app.Mux.HandleFunc("POST /v1/imports", importer.CreateHandler(impSvc.Import, docSvc.Open, app.Logger))
	app.Mux.HandleFunc("POST /v1/imports/preview", importer.PreviewHandler(docSvc.Store, app.Logger))
	// GET /v1/imports/{id} -- the import batch's own read route (INVCR-01-07).
	// rows_total/rows_valid/rows_invalid/errors/created_at live ONLY on
	// import_batches and, until now, reached the browser only inside the POST
	// response; without this route the review screen's "Not imported" channel
	// silently vanishes on reload. Every field is FROZEN at Finalize -- the
	// live ledger counters come from GET /v1/invoices?import_batch_id=...
	// instead. Its literal /preview sibling above is unaffected by
	// registration order (ServeMux resolves by specificity).
	app.Mux.HandleFunc("GET /v1/imports/{id}", importer.GetHandler(impStore.GetBatch, app.Logger))

	// GET /v1/documents/{id} -- the source-document download (DOC-01-05). Bytes are
	// proxied through the service after an RLS-scoped row lookup rather than handed
	// out as a presigned URL, so every read is authorised and audited.
	app.Mux.HandleFunc("GET /v1/documents/{id}", document.DownloadHandler(docSvc.Open, app.Logger))
	// GET /v1/documents/{id}/sheet -- the same bytes decoded through importer.Decode
	// for the previewer, so the evidence surface cannot disagree with the invoice it
	// is evidence for. Lives in importer because Decode does and the reverse import
	// edge is a cycle (TestDocument_ImportsNoRepoPackage). Go 1.22's {id} wildcard
	// matches one segment, so /sheet cannot be swallowed by the download route above.
	app.Mux.HandleFunc("GET /v1/documents/{id}/sheet", importer.SheetHandler(docSvc.Open, app.Logger))

	// /v1/workflow-roles... -- Settings > Roles: the approval seats and who staffs
	// them. Same invoice_app pool as the invoice store above; the writes are
	// admin-only inside the store, so no route here carries a role gate.
	// invoice.FingerprintTx is the publish sweep's Fingerprinter and
	// invoice.DemoteApprovalRejectedTx is Decide's reject-half Demoter: this is the one
	// place both packages are in scope, and internal/approval must never import
	// internal/invoice (TestApproval_DoesNotImportInvoicePackage). Passing nil here
	// compiles and fails only at the first publish/reject — TestMain_WiresTheStoreCollaborators
	// guards it.
	roleStore := approval.NewStore(pool, invoice.FingerprintTx, invoice.DemoteApprovalRejectedTx)
	app.Mux.HandleFunc("GET /v1/workflow-roles", approval.ListRolesHandler(roleStore.ListRoles, app.Logger))
	app.Mux.HandleFunc("POST /v1/workflow-roles", approval.CreateRoleHandler(roleStore.CreateRole, app.Logger))
	app.Mux.HandleFunc("PATCH /v1/workflow-roles/{key}", approval.UpdateRoleHandler(roleStore.UpdateRole, app.Logger))
	app.Mux.HandleFunc("DELETE /v1/workflow-roles/{key}", approval.DeleteRoleHandler(roleStore.DeleteRole, app.Logger))
	app.Mux.HandleFunc("PUT /v1/workflow-roles/{key}/members", approval.SetRoleMembersHandler(roleStore.SetRoleMembers, app.Logger))

	// /v1/approval-policies... -- Settings > Workflows: the policy drafts and the
	// publish-as-seal. roleStore is named for its first use, not its only one: the
	// policy methods hang off the same *approval.Store. Writes are admin-only inside
	// the store, so no route here carries a role gate either.
	app.Mux.HandleFunc("GET /v1/approval-policies", approval.ListPoliciesHandler(roleStore.ListPolicies, app.Logger))
	app.Mux.HandleFunc("POST /v1/approval-policies", approval.CreatePolicyHandler(roleStore.CreatePolicy, app.Logger))
	app.Mux.HandleFunc("GET /v1/approval-policies/{id}", approval.GetPolicyHandler(roleStore.GetPolicy, app.Logger))
	app.Mux.HandleFunc("PUT /v1/approval-policies/{id}/draft", approval.PutDraftHandler(roleStore.PutDraft, app.Logger))
	app.Mux.HandleFunc("POST /v1/approval-policies/{id}/publish", approval.PublishPolicyHandler(roleStore.PublishPolicy, app.Logger))
	app.Mux.HandleFunc("DELETE /v1/approval-policies/{id}", approval.DeletePolicyHandler(roleStore.DeletePolicy, app.Logger))

	// GET /v1/invoices/{id}/approval -- the run read model (APPR-07). No role gate:
	// any authenticated tenant member may read a run, same as the policy routes above.
	app.Mux.HandleFunc("GET /v1/invoices/{id}/approval", approval.RunHandler(roleStore.ApprovalRun, app.Logger))

	// POST /v1/invoices/{id}/approvals -- approve/reject a pending step (APPR-07-06).
	// Plural spelling, distinct route from the GET singular above.
	app.Mux.HandleFunc("POST /v1/invoices/{id}/approvals", approval.DecideHandler(roleStore.DecideSeam, app.Logger))

	// POST /v1/invoices/submissions -- the batch submit endpoint ([trigger-surface],
	// M5-04-07/08). q is an INSERT-ONLY River client (Queues/Workers both nil): this
	// service only ever enqueues submission_submit jobs via the transactional outbox
	// (EnqueueTx), it never fetches or runs them, so it must NOT be registered on the
	// platform kit's background-worker lifecycle (queue.Client.Start is for working
	// clients only -- internal/platform/queue/queue.go's own doc comment). Reached via
	// the gateway as /api/invoice/v1/invoices/submissions.
	q, err := queue.New(pool, queue.Config{})
	if err != nil {
		log.Fatalf("invoice: queue: %v", err)
	}
	submitter := invoice.NewSubmitter(store, q)
	app.Mux.HandleFunc("POST /v1/invoices/submissions", invoice.BatchSubmitHandler(submitter.BatchSubmit, store.CallerRole, app.Logger))

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("invoice: %v", err)
	}
}

// fatal logs a boot failure at ERROR and exits non-zero.
//
// It exists because log.Fatalf does NOT do that here: platform.New calls
// slog.SetDefault (internal/platform/server.go), which routes the standard log
// package through slog at INFO, so a log.Fatalf boot failure is emitted as
// {"level":"INFO"} and NOWHERE AT ALL under LOG_LEVEL=warn or error. A
// fail-closed guard that dies silently leaves an operator with a crash-loop and
// no cause. Copied from cmd/gateway/main.go; this file's remaining log.Fatalf
// calls, mustEnv's included, still carry the defect.
func fatal(logger *slog.Logger, format string, args ...any) {
	logger.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}

// parseEnvBool reads a boolean env value. Unset is false; a set-but-unparseable
// value is an ERROR, never silently false — the permissive state here is "off",
// so a typo would quietly reopen the transmit gate (TestParseEnvBool_Table).
// Value-based and pure, so it needs no t.Setenv — same shape as
// gateway.MockIssuerEnabled.
func parseEnvBool(raw string) (bool, error) {
	// Handled before ParseBool, which rejects the empty string.
	if raw == "" {
		return false, nil
	}
	return strconv.ParseBool(raw)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("invoice: %s is required", key)
	}
	return v
}
