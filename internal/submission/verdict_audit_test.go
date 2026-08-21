// verdict_audit_test.go: specs for this package's audit seam.
//
// Package submission (whitebox), not submission_test: recordVerdictAudit,
// recordSubmissionAudit and recordFailureAudit are all unexported. Every DB fixture in
// this directory (fx, requireEffects, auditCount, auditPayloadMap) lives in package
// submission_test and is unreachable from here, so the DB cases below open their own
// pool -- the shape seed_evidence_honesty_test.go uses, gated on DATABASE_URL.
package submission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ---------------------------------------------------------------------------
// AST scanner shared by the two source-scan specs
// ---------------------------------------------------------------------------

// vaFile is one parsed non-test source file of internal/submission.
type vaFile struct {
	name string
	fset *token.FileSet
	ast  *ast.File
}

// vaSite locates one AST node: its file, its file:line, and the FuncDecl enclosing it
// ("" when the node sits outside any function).
type vaSite struct {
	file string
	loc  string
	fn   string
}

// vaCall is one CallExpr the scanner matched, with its argument list.
type vaCall struct {
	vaSite
	args []ast.Expr
}

// vaMinFilesParsed is the population floor. 15 non-test files today.
const vaMinFilesParsed = 12

// vaRepoRoot resolves the worktree root. Duplicated from deps_test.go's
// repoRootForDepsTest, which lives in package submission_test and cannot be called here.
func vaRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// vaScanSubmissionPackage parses every non-test .go file of internal/submission. AST, not
// substring matching: prose naming audit.Record (exchange.go, verdict_audit.go) is not a
// BasicLit and must not reach any count below.
func vaScanSubmissionPackage(t *testing.T) []vaFile {
	t.Helper()
	dir := filepath.Join(vaRepoRoot(t), "internal", "submission")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []vaFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out = append(out, vaFile{name: name, fset: fset, ast: f})
	}
	return out
}

// vaRequirePopulation is the false-green floor both scans share.
func vaRequirePopulation(t *testing.T, files []vaFile) {
	t.Helper()
	if len(files) < vaMinFilesParsed {
		t.Fatalf("parsed %d non-test files, want >= %d -- the walk is broken, so every count below would be a vacuous zero",
			len(files), vaMinFilesParsed)
	}
}

// vaInspect walks each declaration separately so the enclosing FuncDecl is known by
// construction, never by line arithmetic.
func vaInspect(files []vaFile, visit func(f vaFile, enclosing string, n ast.Node)) {
	for _, f := range files {
		for _, d := range f.ast.Decls {
			enclosing := ""
			if fd, ok := d.(*ast.FuncDecl); ok {
				enclosing = fd.Name.Name
			}
			ast.Inspect(d, func(n ast.Node) bool {
				if n != nil {
					visit(f, enclosing, n)
				}
				return true
			})
		}
	}
}

// vaCalleeName spells a call target as written: "recordVerdictAudit" or "audit.Record".
func vaCalleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return x.Name + "." + e.Sel.Name
		}
	}
	return ""
}

func vaSiteOf(f vaFile, enclosing string, n ast.Node) vaSite {
	pos := f.fset.Position(n.Pos())
	return vaSite{file: f.name, loc: fmt.Sprintf("%s:%d", f.name, pos.Line), fn: enclosing}
}

// vaCollectCalls returns every CallExpr whose callee spells want.
func vaCollectCalls(files []vaFile, want string) []vaCall {
	var out []vaCall
	vaInspect(files, func(f vaFile, enclosing string, n ast.Node) {
		c, ok := n.(*ast.CallExpr)
		if !ok || vaCalleeName(c.Fun) != want {
			return
		}
		out = append(out, vaCall{vaSite: vaSiteOf(f, enclosing, c), args: c.Args})
	})
	return out
}

// vaCollectStringLits returns every string BasicLit whose value equals want.
func vaCollectStringLits(files []vaFile, want string) []vaSite {
	var out []vaSite
	vaInspect(files, func(f vaFile, enclosing string, n ast.Node) {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil || v != want {
			return
		}
		out = append(out, vaSiteOf(f, enclosing, lit))
	})
	return out
}

// vaStringArg reads argument i of a call as a string literal. ok is false for anything
// else -- an identifier, a concatenation, a constant reference.
func vaStringArg(c vaCall, i int) (string, bool) {
	if i >= len(c.args) {
		return "", false
	}
	lit, ok := c.args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

func vaLocs(sites []vaSite) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.loc)
	}
	return out
}

func vaCallLocs(calls []vaCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.loc)
	}
	return out
}

func vaContains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// AC-2 -- one construction site
// ---------------------------------------------------------------------------

// TestSubmissionAudit_EventNameHasOneConstructionSite pins AC-2: recordSubmissionAudit is
// the only place this package calls audit.Record and the only place "submission." is
// concatenated, so the event vocabulary cannot grow a fourth value by accident.
func TestSubmissionAudit_EventNameHasOneConstructionSite(t *testing.T) {
	files := vaScanSubmissionPackage(t)
	vaRequirePopulation(t, files)

	// Control needle: the walk must reach worker.go, the file most likely to grow a
	// second construction site -- not merely the file under test.
	var outside []string
	for _, c := range vaCollectCalls(files, "recordVerdictAudit") {
		if c.file != "verdict_audit.go" {
			outside = append(outside, c.loc)
		}
	}
	if len(outside) < 2 {
		t.Fatalf("recordVerdictAudit calls outside verdict_audit.go = %d %v, want >= 2 -- the walk is not reaching worker.go, so every count below would be a vacuous zero",
			len(outside), outside)
	}

	records := vaCollectCalls(files, "audit.Record")
	if len(records) != 1 {
		t.Fatalf("audit.Record calls in internal/submission = %d %v, want exactly 1 -- recordSubmissionAudit must be the only one",
			len(records), vaCallLocs(records))
	}
	site := records[0]
	if site.file != "verdict_audit.go" {
		t.Errorf("the single audit.Record call is in %s, want verdict_audit.go", site.loc)
	}
	if site.fn != "recordSubmissionAudit" {
		t.Errorf("the single audit.Record call is inside %s (%s), want recordSubmissionAudit -- the submission.<outcome> event name must have exactly one construction site",
			site.fn, site.loc)
	}

	lits := vaCollectStringLits(files, "submission.")
	if len(lits) != 1 {
		t.Fatalf(`string literal "submission." appears %d times %v, want exactly 1 -- the event prefix must be concatenated in one place`,
			len(lits), vaLocs(lits))
	}
	if lits[0].file != "verdict_audit.go" {
		t.Errorf(`the "submission." literal is in %s, want verdict_audit.go`, lits[0].loc)
	}
	if lits[0].fn != "recordSubmissionAudit" {
		t.Errorf(`the "submission." literal is inside %s (%s), want recordSubmissionAudit`, lits[0].fn, lits[0].loc)
	}
}

// ---------------------------------------------------------------------------
// AC-2 -- the outcome vocabulary is closed at three
// ---------------------------------------------------------------------------

// TestSubmissionAudit_OutcomeVocabularyIsExactlyThree pins the three event names the seam
// can spell: submission.accepted, submission.rejected, submission.failed. A computed
// outcome fails the test unless it is recordVerdictAudit forwarding its own parameter --
// otherwise a fourth event could hide behind a variable.
func TestSubmissionAudit_OutcomeVocabularyIsExactlyThree(t *testing.T) {
	files := vaScanSubmissionPackage(t)
	vaRequirePopulation(t, files)

	var (
		sites   int
		found   []vaSite
		values  []string
		smuggle []string
	)
	// recordVerdictAudit(ctx, tx, invoiceID, jobID, outcome, reference) -- outcome is arg 4.
	for _, c := range vaCollectCalls(files, "recordVerdictAudit") {
		sites++
		v, ok := vaStringArg(c, 4)
		if !ok {
			smuggle = append(smuggle, fmt.Sprintf("%s: recordVerdictAudit inside %s passes a computed outcome", c.loc, c.fn))
			continue
		}
		found = append(found, c.vaSite)
		values = append(values, v)
	}
	// recordSubmissionAudit(ctx, tx, outcome, payload) -- outcome is arg 2.
	for _, c := range vaCollectCalls(files, "recordSubmissionAudit") {
		sites++
		v, ok := vaStringArg(c, 2)
		if !ok {
			// recordVerdictAudit is the one legitimate forwarder: it hands on its own
			// parameter, already counted at each of its four call sites.
			if c.fn == "recordVerdictAudit" {
				continue
			}
			smuggle = append(smuggle, fmt.Sprintf("%s: recordSubmissionAudit inside %s passes a computed outcome, so a fourth event could hide behind it", c.loc, c.fn))
			continue
		}
		found = append(found, c.vaSite)
		values = append(values, v)
	}

	if sites < 4 {
		t.Fatalf("matched %d recordVerdictAudit/recordSubmissionAudit calls, want >= 4 -- the scanner is matching nothing, so the vocabulary below would be a vacuous empty set", sites)
	}
	var crossFile int
	for _, s := range found {
		if s.file != "verdict_audit.go" {
			crossFile++
		}
	}
	if crossFile < 1 {
		t.Fatalf("collected %d outcome literals from files other than verdict_audit.go, want >= 1 -- the walk lost worker.go, so the vocabulary below is missing its verdict values", crossFile)
	}
	if len(smuggle) > 0 {
		t.Errorf("outcome passed as a variable at %d site(s):\n  %s", len(smuggle), strings.Join(smuggle, "\n  "))
	}

	var distinct []string
	for _, v := range values {
		if !vaContains(distinct, v) {
			distinct = append(distinct, v)
		}
	}
	sort.Strings(distinct)
	want := []string{"accepted", "failed", "rejected"}

	if len(distinct) != len(want) || !vaSameSet(distinct, want) {
		var notes []string
		for _, w := range want {
			if !vaContains(distinct, w) {
				notes = append(notes, fmt.Sprintf("%q is missing: no recordSubmissionAudit call passes it", w))
			}
		}
		for _, g := range distinct {
			if !vaContains(want, g) {
				notes = append(notes, fmt.Sprintf("%q is not in the vocabulary", g))
			}
		}
		t.Errorf("outcome vocabulary = %v (%d distinct), want exactly %d %v -- %s",
			distinct, len(distinct), len(want), want, strings.Join(notes, "; "))
	}
}

func vaSameSet(got, want []string) bool {
	for _, w := range want {
		if !vaContains(got, w) {
			return false
		}
	}
	for _, g := range got {
		if !vaContains(want, g) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// DB harness -- own pool, fresh tenant, nothing committed
// ---------------------------------------------------------------------------

// errVARollback ends every DB case below. Each case writes, reads its own rows back
// inside the same transaction, then returns this so WithinTenantTx rolls back and the
// case leaves no row behind.
var errVARollback = errors.New("verdict-audit spec: intentional rollback")

// vaRow is one audit_log row read back inside the writing transaction.
type vaRow struct {
	actor   string
	event   string
	payload map[string]any
}

// vaRequireAppPool opens this file's own pool. Same shape as
// seed_evidence_honesty_test.go's sehRequireSuperuserDSN, gated on DATABASE_URL: these
// cases write as invoice_app under RLS and need no migrator or superuser connection.
func vaRequireAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("verdict-audit DB cases skipped: set DATABASE_URL (or run make test-queue)")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect app pool from DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func vaAuditCount(ctx context.Context, tx pgx.Tx) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	return n, err
}

func vaReadRows(ctx context.Context, tx pgx.Tx) ([]vaRow, error) {
	rows, err := tx.Query(ctx, `SELECT actor, event, payload::text FROM audit_log ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []vaRow
	for rows.Next() {
		var r vaRow
		var body string
		if err := rows.Scan(&r.actor, &r.event, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &r.payload); err != nil {
			return nil, fmt.Errorf("unmarshal payload %q: %w", body, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// vaWithinFreshTenant runs write against a random tenant uuid, reads the rows back under
// that tenant's RLS, hands them to check, then rolls back. wantRows is the non-vacuity
// floor: an absence assertion must never pass on no row at all.
func vaWithinFreshTenant(t *testing.T, wantRows int, write func(ctx context.Context, tx pgx.Tx) error, check func(t *testing.T, rows []vaRow)) {
	t.Helper()
	pool := vaRequireAppPool(t)
	ctx := context.Background()
	err := db.WithinTenantTx(ctx, pool, uuid.NewString(), func(tx pgx.Tx) error {
		if err := write(ctx, tx); err != nil {
			return err
		}
		n, err := vaAuditCount(ctx, tx)
		if err != nil {
			return err
		}
		if n != wantRows {
			t.Errorf("audit_log rows visible under this tenant = %d, want %d -- every assertion below would otherwise read no row at all", n, wantRows)
			return errVARollback
		}
		rows, err := vaReadRows(ctx, tx)
		if err != nil {
			return err
		}
		check(t, rows)
		return errVARollback
	})
	if !errors.Is(err, errVARollback) {
		t.Fatalf("WithinTenantTx returned %v, want the rollback sentinel -- the case must commit nothing", err)
	}
}

func vaKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// AC-1 / D-2 / D-7 -- recordFailureAudit
// ---------------------------------------------------------------------------

// TestRecordFailureAudit_ActorIsSystem pins AC-1's actor and event: the worker path has no
// human, so the row is attributed to the literal "system", and the event name is built
// from the "failed" outcome like every other row this seam writes.
func TestRecordFailureAudit_ActorIsSystem(t *testing.T) {
	invoiceID, jobID := uuid.NewString(), uuid.NewString()
	vaWithinFreshTenant(t, 1,
		func(ctx context.Context, tx pgx.Tx) error {
			return recordFailureAudit(ctx, tx, invoiceID, jobID, FailurePayloadNotBuilt)
		},
		func(t *testing.T, rows []vaRow) {
			if rows[0].actor != "system" {
				t.Errorf("actor = %q, want %q", rows[0].actor, "system")
			}
			if rows[0].event != "submission.failed" {
				t.Errorf("event = %q, want %q", rows[0].event, "submission.failed")
			}
		})
}

// TestRecordFailureAudit_PayloadIsExactlyFourKeys pins AC-1's payload shape. The fourth
// key is what makes the extraction worth doing: the verdict payload has no failure_kind
// and this one has no reference. The event suffix and payload outcome are asserted to
// agree, since "failed" is written twice in the helper.
func TestRecordFailureAudit_PayloadIsExactlyFourKeys(t *testing.T) {
	invoiceID, jobID := uuid.NewString(), uuid.NewString()
	kind := FailureNeverAcknowledged
	vaWithinFreshTenant(t, 1,
		func(ctx context.Context, tx pgx.Tx) error {
			return recordFailureAudit(ctx, tx, invoiceID, jobID, kind)
		},
		func(t *testing.T, rows []vaRow) {
			want := []string{"failure_kind", "invoice_id", "outcome", "submission_job_id"}
			if got := vaKeys(rows[0].payload); !vaSameSet(got, want) || len(got) != len(want) {
				t.Fatalf("payload keys = %v, want exactly %v", got, want)
			}
			for key, want := range map[string]string{
				"invoice_id":        invoiceID,
				"submission_job_id": jobID,
				"outcome":           "failed",
				"failure_kind":      string(kind),
			} {
				if got, _ := rows[0].payload[key].(string); got != want {
					t.Errorf("payload[%q] = %q, want %q", key, got, want)
				}
			}
			if !kind.Valid() {
				t.Errorf("%q is not a FailureKind the invoices.failure_kind CHECK admits", string(kind))
			}
			suffix := strings.TrimPrefix(rows[0].event, "submission.")
			if got, _ := rows[0].payload["outcome"].(string); got != suffix {
				t.Errorf("event suffix %q and payload outcome %q disagree -- both spell the same word in recordFailureAudit", suffix, got)
			}
		})
}

// TestRecordFailureAudit_CarriesNoWireDetail pins D-7: the reason travels as the
// FailureKind enum, never submission_jobs.last_error, which is adapter-shaped free text.
// Driving all three kinds through one transaction proves the enum is the only thing that
// varies between the rows.
func TestRecordFailureAudit_CarriesNoWireDetail(t *testing.T) {
	kinds := []FailureKind{FailurePayloadNotBuilt, FailureNeverAcknowledged, FailureAcknowledgedNoVerdict}
	invoiceID, jobID := uuid.NewString(), uuid.NewString()
	forbidden := []string{"last_error", "error", "message", "response", "body", "detail", "raw", "wire"}

	vaWithinFreshTenant(t, len(kinds),
		func(ctx context.Context, tx pgx.Tx) error {
			for _, k := range kinds {
				if err := recordFailureAudit(ctx, tx, invoiceID, jobID, k); err != nil {
					return err
				}
			}
			return nil
		},
		func(t *testing.T, rows []vaRow) {
			var seen []string
			for i, r := range rows {
				for _, f := range forbidden {
					if _, ok := r.payload[f]; ok {
						t.Errorf("row %d payload carries %q -- the reason must be the FailureKind enum, not adapter-shaped free text", i, f)
					}
				}
				got, _ := r.payload["failure_kind"].(string)
				if !FailureKind(got).Valid() {
					t.Errorf("row %d failure_kind = %q, which is not one of the three admitted kinds", i, got)
				}
				seen = append(seen, got)

				// Everything except failure_kind must be identical across the three rows.
				rest := map[string]any{}
				for k, v := range r.payload {
					if k != "failure_kind" {
						rest[k] = v
					}
				}
				wantRest := map[string]any{"invoice_id": invoiceID, "submission_job_id": jobID, "outcome": "failed"}
				if fmt.Sprint(vaKeys(rest)) != fmt.Sprint(vaKeys(wantRest)) {
					t.Errorf("row %d payload keys beside failure_kind = %v, want %v", i, vaKeys(rest), vaKeys(wantRest))
					continue
				}
				for k, w := range wantRest {
					if rest[k] != w {
						t.Errorf("row %d payload[%q] = %v, want %v -- only failure_kind may vary between kinds", i, k, rest[k], w)
					}
				}
			}
			var want []string
			for _, k := range kinds {
				want = append(want, string(k))
			}
			sort.Strings(seen)
			sort.Strings(want)
			if fmt.Sprint(seen) != fmt.Sprint(want) {
				t.Errorf("failure_kind values written = %v, want %v -- all three kinds must reach the payload verbatim", seen, want)
			}
		})
}

// ---------------------------------------------------------------------------
// AC-3 -- regression backstop, green before and after the extraction
// ---------------------------------------------------------------------------

// TestRecordVerdictAudit_AcceptedRejectedUnchanged is a regression backstop, not a
// red-to-green spec: it asserts today's behaviour so the extraction cannot move it. On
// Accepted the IRN rides along as reference; on Rejected the caller passes "" and the key
// is left out of the payload rather than written as an empty string.
func TestRecordVerdictAudit_AcceptedRejectedUnchanged(t *testing.T) {
	invoiceID, jobID := uuid.NewString(), uuid.NewString()
	const irn = "IRN-VA-0001"

	vaWithinFreshTenant(t, 2,
		func(ctx context.Context, tx pgx.Tx) error {
			if err := recordVerdictAudit(ctx, tx, invoiceID, jobID, "accepted", irn); err != nil {
				return err
			}
			return recordVerdictAudit(ctx, tx, invoiceID, jobID, "rejected", "")
		},
		func(t *testing.T, rows []vaRow) {
			accepted, rejected := rows[0], rows[1]

			if accepted.event != "submission.accepted" {
				t.Errorf("first event = %q, want %q", accepted.event, "submission.accepted")
			}
			wantAccepted := []string{"invoice_id", "outcome", "reference", "submission_job_id"}
			if got := vaKeys(accepted.payload); !vaSameSet(got, wantAccepted) || len(got) != len(wantAccepted) {
				t.Errorf("accepted payload keys = %v, want exactly %v", got, wantAccepted)
			}
			if got, _ := accepted.payload["reference"].(string); got != irn {
				t.Errorf("accepted payload reference = %q, want %q", got, irn)
			}
			if got, _ := accepted.payload["outcome"].(string); got != "accepted" {
				t.Errorf("accepted payload outcome = %q, want %q", got, "accepted")
			}

			if rejected.event != "submission.rejected" {
				t.Errorf("second event = %q, want %q", rejected.event, "submission.rejected")
			}
			wantRejected := []string{"invoice_id", "outcome", "submission_job_id"}
			if got := vaKeys(rejected.payload); !vaSameSet(got, wantRejected) || len(got) != len(wantRejected) {
				t.Errorf("rejected payload keys = %v, want exactly %v", got, wantRejected)
			}
			if _, present := rejected.payload["reference"]; present {
				t.Errorf("rejected payload carries reference = %v, want the key absent entirely", rejected.payload["reference"])
			}
			for _, r := range []vaRow{accepted, rejected} {
				if r.actor != "system" {
					t.Errorf("%s actor = %q, want %q", r.event, r.actor, "system")
				}
			}
		})
}
