// dsn_test.go pins the KindOpaque half of the severity table, written RED
// before dsn.go grows the `Kind` field and the five DOCUMENT_* rows.
//
// KindOpaque exists because a DOCUMENT_ACCESS_KEY_ID is not a URL and has no
// password component: run through the DSN checks it is DefectNoPassword in
// every healthy environment.
//
// The Kind field is read by REFLECTION, not by name: a direct reference would
// be a compile error until dsn.go changes, which reds the whole package
// including main_test.go's green suite (the RED shape dsn_check_test.go:164-174
// already rejects).
//
// requireDocumentRows guards the "clean input stays clean" fixtures -- a table
// that does not know DOCUMENT_* exists satisfies those for the wrong reason.
package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The five rendered values, as sentinels. None is a credential; all five are
// greppable so a report that echoes one is caught.
const (
	sentinelBucket   = "S3NT1NEL-bucket-value"
	sentinelEndpoint = "https://s3nt1nel.invalid"
	sentinelRegion   = "S3NT1NEL-region-value"
	sentinelKeyID    = "S3NT1NEL-key-id-value"
	sentinelSecret   = "S3NT1NEL-secret-value"
)

// unrenderedRef is the shape a `${{source-documents.X}}` reference keeps when a
// fork fails to resolve it -- the DOC-01 instance of the M4-22 failure mode.
func unrenderedRef(variable string) string {
	return "${{source-documents." + strings.TrimPrefix(variable, "DOCUMENT_") + "}}"
}

// documentVars is what Railway renders onto the invoice service once the five
// reference variables are set.
func documentVars() map[string]string {
	return map[string]string{
		"DOCUMENT_BUCKET":            sentinelBucket,
		"DOCUMENT_ENDPOINT":          sentinelEndpoint,
		"DOCUMENT_REGION":            sentinelRegion,
		"DOCUMENT_ACCESS_KEY_ID":     sentinelKeyID,
		"DOCUMENT_SECRET_ACCESS_KEY": sentinelSecret,
	}
}

func documentVarNames() []string {
	names := make([]string, 0, 5)
	for name := range documentVars() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// documentMap is healthyMap with the invoice service carrying all five rendered
// DOCUMENT_* variables.
func documentMap() dsnMap {
	m := healthyMap()
	for k, v := range documentVars() {
		m["invoice"][k] = v
	}
	return m
}

// requireDocumentRows fails the caller unless the severity table carries the
// five invoice DOCUMENT_* rows. Without it every "this input is clean"
// assertion in this file passes vacuously against today's table, which cannot
// flag a variable it does not know exists.
func requireDocumentRows(t *testing.T) {
	t.Helper()
	have := map[string]bool{}
	for _, req := range DSNRequirements {
		if req.Service == "invoice" && strings.HasPrefix(req.Variable, "DOCUMENT_") {
			have[req.Variable] = true
		}
	}
	var missing []string
	for _, name := range documentVarNames() {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("DSNRequirements carries no invoice row for %v. tools/prenv/dsn.go:51-66 must gain the five DOCUMENT_* rows (IfPresent, KindOpaque); until then this assertion passes for the wrong reason -- the table cannot flag a variable it does not know about.", missing)
	}
}

// offenderStrings renders an offender slice for a readable diff.
func offenderStrings(offenders []Offender) []string {
	out := make([]string, 0, len(offenders))
	for _, o := range offenders {
		out = append(out, o.String())
	}
	return out
}

// T-DOC-1. The five DOCUMENT_* rows are `if-present-must-be-valid`, and it
// takes BOTH directions to pin that -- same shape as T1-6/T1-7 for
// notifications.DATABASE_URL.
//
// Absent is clean until DOC-01-03: cmd/invoice/main.go reads none of the five
// yet, so Required here fails `assert-db-dsns --source-only` on every open PR
// (dev-env.yml:315-327) before the operator has set the production variables.
// Present-but-empty is the fork-rendering failure mode itself.
func TestCheckDSNs_DocumentVarsAreIfPresentMustBeValid(t *testing.T) {
	t.Run("absent is clean", func(t *testing.T) {
		requireDocumentRows(t)

		offenders := CheckDSNs(healthyMap())
		if len(offenders) != 0 {
			t.Errorf("healthy fleet with no DOCUMENT_* variables reports %v. The five rows are IfPresent in this subtask -- Required is DOC-01-03's job, once cmd/invoice/main.go actually reads them.", offenderStrings(offenders))
		}
	})

	t.Run("present but empty is an offender", func(t *testing.T) {
		for _, name := range documentVarNames() {
			t.Run(name, func(t *testing.T) {
				m := documentMap()
				m["invoice"][name] = ""

				offenders := CheckDSNs(m)
				want := []Offender{{"invoice", name, DefectEmptyValue}}
				if !reflect.DeepEqual(offenders, want) {
					t.Errorf("offenders = %v, want %v: an empty %s means the ${{source-documents.*}} reference resolved to nothing -- IfPresent never means 'do not check'.", offenderStrings(offenders), offenderStrings(want), name)
				}
			})
		}
	})
}

// T-DOC-2. An unrendered reference is its own defect with its own remedy (fix
// the variable reference, not the value), and it is the defect a fork produces
// when `${{source-documents.*}}` fails to resolve.
func TestCheckDSNs_OpaqueUnrenderedIsOffender(t *testing.T) {
	for _, name := range documentVarNames() {
		t.Run(name, func(t *testing.T) {
			m := documentMap()
			m["invoice"][name] = unrenderedRef(name)

			offenders := CheckDSNs(m)
			want := []Offender{{"invoice", name, DefectUnrendered}}
			if !reflect.DeepEqual(offenders, want) {
				t.Errorf("offenders = %v, want %v: %s still holds %s, so the service boots against a bucket that does not exist.", offenderStrings(offenders), offenderStrings(want), name, unrenderedRef(name))
			}
		})
	}
}

// T-DOC-3. KindOpaque must SKIP the URL checks: an access key id, a bucket
// name and a region are all DefectNoPassword under inspectDSN, so a KindOpaque
// that forgot to skip flags every healthy environment on every run.
//
// NON-VACUITY: each value goes through inspectDSN first and must be reported
// BAD there, which is what proves the case discriminates the two kinds.
func TestCheckDSNs_OpaqueSkipsURLChecks(t *testing.T) {
	requireDocumentRows(t)

	cases := []struct {
		variable string
		value    string
	}{
		{"DOCUMENT_ACCESS_KEY_ID", sentinelKeyID},
		{"DOCUMENT_BUCKET", sentinelBucket},
		{"DOCUMENT_REGION", sentinelRegion},
		{"DOCUMENT_ENDPOINT", sentinelEndpoint},
		{"DOCUMENT_SECRET_ACCESS_KEY", sentinelSecret},
		// Unparseable, not merely password-less: the other skipped defect.
		{"DOCUMENT_ACCESS_KEY_ID", "%zz"},
	}

	for _, tc := range cases {
		t.Run(tc.variable+"="+tc.value, func(t *testing.T) {
			if defect, bad := inspectDSN(tc.value); !bad {
				t.Fatalf("oracle: inspectDSN(%q) reports no defect, so this case cannot tell KindOpaque from KindDSN", tc.value)
			} else if defect != DefectNoPassword && defect != DefectUnparseable {
				t.Fatalf("oracle: inspectDSN(%q) = %q, want one of the URL-only defects -- this case is meant to prove those are skipped", tc.value, defect)
			}

			m := documentMap()
			m["invoice"][tc.variable] = tc.value

			offenders := CheckDSNs(m)
			if len(offenders) != 0 {
				t.Errorf("offenders = %v, want none: %s is opaque, so the URL checks must not run against it.", offenderStrings(offenders), tc.variable)
			}
		})
	}
}

// T-DOC-4. The report names the offender and never its value. Same obligation
// as T1-9, restated for the DOCUMENT_* rows because a secret access key is the
// most damaging value in the map -- and because the natural "helpful" mistake
// on a new defect path is to print what was found.
//
// Driven through the real binary: this is the operator-facing surface, and it
// is where a stray fmt of the whole map would show up.
func TestCheckDSNs_OpaqueNeverPrintsValue(t *testing.T) {
	t.Run("empty offender does not drag the healthy values out with it", func(t *testing.T) {
		m := documentMap()
		m["invoice"]["DOCUMENT_ACCESS_KEY_ID"] = ""

		stdout, stderr, code := runDSNCheck(t, m)
		if code == 0 {
			t.Fatalf("exit code = 0: an empty DOCUMENT_ACCESS_KEY_ID must fail the gate, and the hygiene assertions below are vacuous until it does; stdout = %q", stdout)
		}
		if !strings.Contains(stdout, "invoice") || !strings.Contains(stdout, "DOCUMENT_ACCESS_KEY_ID") {
			t.Fatalf("stdout does not name the offender -- 'it leaked nothing' means nothing until a report exists; stdout = %q", stdout)
		}
		for _, secret := range []string{sentinelBucket, sentinelEndpoint, sentinelRegion, sentinelSecret} {
			if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
				t.Errorf("the report echoed the value %q of a NON-offending DOCUMENT_* variable; stdout = %q, stderr = %q", secret, stdout, stderr)
			}
		}
		assertNoSentinel(t, stdout, stderr)
	})

	t.Run("unrendered offender does not echo the reference itself", func(t *testing.T) {
		ref := unrenderedRef("DOCUMENT_SECRET_ACCESS_KEY")
		m := documentMap()
		m["invoice"]["DOCUMENT_SECRET_ACCESS_KEY"] = ref

		stdout, stderr, code := runDSNCheck(t, m)
		if code == 0 {
			t.Fatalf("exit code = 0: an unrendered DOCUMENT_SECRET_ACCESS_KEY must fail the gate; stdout = %q", stdout)
		}
		if !strings.Contains(stdout, "invoice") || !strings.Contains(stdout, "DOCUMENT_SECRET_ACCESS_KEY") {
			t.Fatalf("stdout does not name the offender; stdout = %q", stdout)
		}
		// The generic `${{...}}` in DefectUnrendered's prose is fine; the
		// resolved-from value is not.
		if strings.Contains(stdout, ref) || strings.Contains(stderr, ref) {
			t.Errorf("the report echoed the offending value %q instead of just naming the variable; stdout = %q, stderr = %q", ref, stdout, stderr)
		}
		for _, secret := range []string{sentinelBucket, sentinelEndpoint, sentinelRegion, sentinelKeyID} {
			if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
				t.Errorf("the report echoed the value %q of a non-offending variable; stdout = %q", secret, stdout)
			}
		}
		assertNoSentinel(t, stdout, stderr)
	})
}

// T-DOC-5. Adding Kind must not move a single DATABASE offender. Every one of
// the six defects appears exactly once below, in table order, and the whole
// slice is compared -- so a Kind that leaks into the DSN path (skipping a check
// it should run) or five DOCUMENT rows landed as Required (five extra
// offenders, this fixture holds none of them) both fail here.
//
// HONESTY NOTE: green today. This is a regression guard for the Kind edit, not
// a specification of unbuilt behaviour.
func TestCheckDSNs_DSNKindUnchanged(t *testing.T) {
	m := healthyMap()
	m["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN
	delete(m["gateway"], "DATABASE_SUPERUSER_URL")
	m["tenancy"]["DATABASE_URL"] = danglingDSN
	m["portfolio"]["DATABASE_URL"] = ""
	m["invoice"]["DATABASE_URL"] = "postgres://nouser@" + railwayHost
	m["validation"]["DATABASE_URL"] = "%zz"
	delete(m["submission"], "DATABASE_URL")
	delete(m["notifications"], "DATABASE_URL")

	want := []Offender{
		{"gateway", "DATABASE_MIGRATION_URL", DefectEmptyPassword},
		{"tenancy", "DATABASE_URL", DefectUnrendered},
		{"portfolio", "DATABASE_URL", DefectEmptyValue},
		{"invoice", "DATABASE_URL", DefectNoPassword},
		{"validation", "DATABASE_URL", DefectUnparseable},
		{"submission", "DATABASE_URL", DefectMissing},
	}

	got := CheckDSNs(m)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the DSN offender set changed.\n got = %v\nwant = %v\nAdding Kind must leave every KindDSN row byte-identical, and must not add DOCUMENT_* offenders to a map that carries no DOCUMENT_* variables.", offenderStrings(got), offenderStrings(want))
	}
}

// T-DOC-6. KindDSN must be iota-0, for the same fail-safe reason Required is
// (dsn.go:29): a future row that omits Kind then gets the FULL checking, not
// the lax path. Every pre-existing DATABASE row carries that zero value.
func TestDSNRequirementKindIsTheStrictZeroValue(t *testing.T) {
	if len(DSNRequirements) == 0 {
		t.Fatalf("DSNRequirements is empty -- the loop below would pass vacuously")
	}

	checked := 0
	for _, req := range DSNRequirements {
		if !strings.HasPrefix(req.Variable, "DATABASE") {
			continue
		}
		checked++
		if k := kindOf(t, req); k != 0 {
			t.Errorf("row %s %s has Kind = %d, want the zero value: KindDSN must be iota-0 so a row that omits Kind gets the full DSN checking rather than the lax path.", req.Service, req.Variable, k)
		}
	}
	if checked == 0 {
		t.Fatalf("no DATABASE_* rows found in DSNRequirements -- the loop above passed vacuously")
	}
}

// T-DOC-7. The table itself: exactly five invoice DOCUMENT_* rows, all
// IfPresent, all one and the same non-zero Kind.
func TestDSNRequirementsCoverInvoiceDocumentVars(t *testing.T) {
	if len(DSNRequirements) == 0 {
		t.Fatalf("DSNRequirements is empty -- every check below would pass vacuously")
	}

	var documentRows []DSNRequirement
	for _, req := range DSNRequirements {
		if req.Service == "invoice" && strings.HasPrefix(req.Variable, "DOCUMENT_") {
			documentRows = append(documentRows, req)
		}
	}

	var gotNames []string
	for _, req := range documentRows {
		gotNames = append(gotNames, req.Variable)
	}
	sort.Strings(gotNames)
	if !reflect.DeepEqual(gotNames, documentVarNames()) {
		t.Fatalf("invoice DOCUMENT_* rows = %v, want exactly %v. These five are the complete set Railway exposes for a bucket; a missing row is a variable the gate never inspects, and an extra one is a variable that does not exist.", gotNames, documentVarNames())
	}

	for _, req := range documentRows {
		if req.Severity != IfPresent {
			t.Errorf("row %s %s has severity %v, want IfPresent. Required means 'the service opens a pool at boot and cannot run without it' (dsn.go:28) and cmd/invoice/main.go reads none of the five until DOC-01-03; shipping Required here reds every open PR via assert-db-dsns --source-only (dev-env.yml:315-327).", req.Service, req.Variable, req.Severity)
		}
	}

	opaque := map[int64][]string{}
	for _, req := range documentRows {
		k := kindOf(t, req)
		opaque[k] = append(opaque[k], req.Variable)
	}
	if len(opaque) != 1 {
		t.Fatalf("the five DOCUMENT_* rows carry %d different Kind values (%v), want one shared KindOpaque", len(opaque), opaque)
	}
	for k, vars := range opaque {
		if k == 0 {
			t.Errorf("rows %v carry the zero Kind (KindDSN), want KindOpaque: an access key id is not a URL and has no password component, so the DSN checks would flag every healthy environment.", vars)
		}
	}
}

// kindOf reads DSNRequirement.Kind by reflection. Direct reference would be a
// COMPILE error until dsn.go gains the field, which reds the whole package
// including main_test.go's existing suite -- see this file's header.
func kindOf(t *testing.T, req DSNRequirement) int64 {
	t.Helper()
	f := reflect.ValueOf(req).FieldByName("Kind")
	if !f.IsValid() {
		t.Fatalf("DSNRequirement has no Kind field. tools/prenv/dsn.go:37-41 must gain it, with the strict kind (KindDSN) as the iota-0 zero value; the five DOCUMENT_* rows then carry KindOpaque, which applies only DefectMissing/DefectEmptyValue/DefectUnrendered.")
	}
	if !f.CanInt() {
		t.Fatalf("DSNRequirement.Kind has kind %s, want an integer iota type mirroring Severity so the strict kind can be the zero value", f.Kind())
	}
	return f.Int()
}
