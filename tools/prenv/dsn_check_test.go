// dsn_check_test.go pins the contract of the not-yet-implemented `prenv
// dsn-check` subcommand (M4-22-FU-01, task-178). Written RED, before the
// implementation, so the implementation is forced to satisfy assertions it
// did not get to author.
//
// WHY THIS FILE EXISTS AT ALL. M4-22's incident was a Railway variable
// holding `postgresql://invoice_migrator:${{MIGRATOR_PASSWORD}}@...` -- an
// unrendered reference -- and its sibling, a DSN whose password component
// rendered EMPTY. Both look like a DSN, both parse, both boot a service that
// then cannot authenticate. `dsn-check` is the gate that catches them before
// deploy.
//
// WHY EVERY TEST BELOW NAMES A MUTATION. While verifying this very task the
// RALPH lead wrote a DSN check that masked passwords with `[^@]*` -- which
// matches the EMPTY string, so a password-less DSN rendered identically to a
// healthy one and the check could not fail. A decorative guard, produced
// while verifying the task about decorative guards. See
// .ralph/ac3-development-dsn-readback.md:27-35. Every test here therefore
// states which named mutation it kills; a test that survives every mutation
// is decorative and does not belong in this file.
//
// Mutation set: M-invert (flip the offender predicate) - M-first (truncate
// the offender list to the first hit) - M-empty (empty the severity table) -
// M-mask (reintroduce the empty-matching mask) - M-sev (flip notifications'
// severity) - M-swallow (treat malformed input as clean).
//
// TWO-WAY EXIT CONTRACT. These tests assert exit 0 for clean and exit != 0
// for fail, and deliberately never assert a specific non-zero code. Both
// failure modes here (offenders found, malformed input) fail the gate
// identically, so a 1-vs-2 split would have no consumer -- and main_test.go
// :168-175 already documents that `go run` collapses every non-zero exit to
// 1 anyway. The operator-facing distinction lives in the MESSAGE (T1-2,
// T1-11), not in the exit code.
//
// STDIN, NEVER ARGV. The rendered map carries live credentials; argv is
// visible in `ps`. Every test here feeds the map on stdin, which is itself
// part of the pinned contract.
package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

// sentinelPW is the literal password used by EVERY healthy fixture entry in
// this file. Its only job is to be greppable: T1-9 asserts it never appears
// in stdout or stderr of any failing run. A real check reports WHICH DSN is
// bad; it never reports what the credential is.
const sentinelPW = "S3NT1NEL-PW"

const railwayHost = "postgres.railway.internal:5432/railway"

// incidentDSN is the VERBATIM shape from the M4-22 incident: role present,
// password component present-but-empty. Kept as a literal (not built from
// helpers) so the fixture cannot drift away from the thing that actually
// broke production.
const incidentDSN = "postgresql://invoice_migrator:@postgres.railway.internal:5432/railway"

// danglingDSN is the OTHER M4-22 shape: a Railway variable reference that
// was never rendered, so the literal `${{...}}` survived into the DSN.
const danglingDSN = "postgresql://invoice_migrator:${{MIGRATOR_PASSWORD}}@postgres.railway.internal:5432/railway"

// dsnMap is the rendered {serviceName: {varName: value}} map dsn-check reads
// on stdin.
type dsnMap map[string]map[string]string

// healthyMap returns the full 9-entry fleet map, every entry valid. The nine
// entries are exactly what a live read-back of the `development` environment
// returned on 2026-07-19 (.ralph/ac3-development-dsn-readback.md) -- this is
// the real fleet shape, not an invented one.
func healthyMap() dsnMap {
	app := "postgresql://invoice_app:" + sentinelPW + "@" + railwayHost
	m := dsnMap{
		"gateway": {
			"DATABASE_MIGRATION_URL": "postgresql://invoice_migrator:" + sentinelPW + "@" + railwayHost,
			"DATABASE_SUPERUSER_URL": "postgresql://postgres:" + sentinelPW + "@" + railwayHost,
		},
	}
	for _, svc := range []string{"tenancy", "portfolio", "invoice", "validation", "dashboard", "submission", "notifications"} {
		m[svc] = map[string]string{"DATABASE_URL": app}
	}
	return m
}

// runDSNCheck feeds m to `prenv dsn-check` on STDIN and returns the real
// process's stdout, stderr and exit code. Mirrors runCLI in main_test.go
// (same built-binary boundary) but adds the stdin the contract requires.
func runDSNCheck(t *testing.T, m dsnMap) (stdout, stderr string, exitCode int) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshalling fixture map: %v", err)
	}
	return runDSNCheckRaw(t, string(raw))
}

// runDSNCheckRaw is runDSNCheck for input that is deliberately not valid
// JSON (T1-11).
func runDSNCheckRaw(t *testing.T, stdin string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binPath, "dsn-check")
	cmd.Stdin = strings.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("failed to run prenv dsn-check: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// assertNoSentinel is the T1-9 obligation, applied inline at every failing
// call site rather than only in one aggregate test: a credential must never
// reach stdout or stderr, and the cheapest way to keep that true is to check
// it on every single run that produces output.
func assertNoSentinel(t *testing.T, stdout, stderr string) {
	t.Helper()
	if strings.Contains(stdout, sentinelPW) {
		t.Errorf("stdout leaked the password sentinel %q -- a DSN check must report WHICH var is bad, never its credential; stdout = %q", sentinelPW, stdout)
	}
	if strings.Contains(stderr, sentinelPW) {
		t.Errorf("stderr leaked the password sentinel %q; stderr = %q", sentinelPW, stderr)
	}
}

// T1-1. The verbatim incident DSN must be reported, and the report must name
// BOTH the service and the variable -- an operator who is told only "a DSN is
// bad" across a 9-service fleet has not been told anything actionable.
//
// KILLS: M-invert (an inverted predicate passes this map), M-empty (an empty
// severity table checks nothing and exits 0), M-mask (the `[^@]*` mask makes
// an empty password indistinguishable from a healthy one, so the check cannot
// fail).
func TestDSNCheckFlagsVerbatimIncidentDSN(t *testing.T) {
	m := healthyMap()
	m["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN

	stdout, stderr, code := runDSNCheck(t, m)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero: the M4-22 incident DSN (empty password) must fail the gate")
	}
	if !strings.Contains(stdout, "gateway") {
		t.Errorf("stdout does not name the offending service %q; stdout = %q", "gateway", stdout)
	}
	if !strings.Contains(stdout, "DATABASE_MIGRATION_URL") {
		t.Errorf("stdout does not name the offending variable %q; stdout = %q", "DATABASE_MIGRATION_URL", stdout)
	}
	assertNoSentinel(t, stdout, stderr)
}

// T1-2. An unrendered `${{...}}` reference and an empty password are
// DIFFERENT defects with different operator remedies (fix the variable
// reference vs. set the password), so they must not collapse into one
// message.
//
// HOW THIS IS ASSERTED, AND WHY NOT VIA EXPORTED CONSTANTS. The task spec
// asked for a comparison against exported reason constants. Those constants
// live in dsn.go, which does not exist yet -- referencing them would make
// this test file fail to COMPILE, which fails the whole package (including
// main_test.go's existing green suite) and is the wrong kind of RED. So the
// property is asserted structurally instead: the two fixtures are byte-for-
// byte identical except for the one DSN value, and both name the same service
// and the same variable, so if the two reasons collapsed the two stdouts
// would be IDENTICAL. Asserting they differ pins the anti-collapse property
// with zero coupling to either constant names or prose wording -- strictly
// more robust than matching strings.
//
// VACUITY GUARD: if the tool echoed the offending DSN, the two stdouts would
// differ for a trivial reason even under a collapsed-reason implementation,
// making the comparison vacuous. The `postgresql://` assertion below closes
// that hole (and is a credential-hygiene requirement in its own right).
//
// KILLS: reason-collapse.
func TestDSNCheckDistinguishesDanglingReferenceFromEmptyPassword(t *testing.T) {
	empty := healthyMap()
	empty["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN
	emptyOut, emptyErr, emptyCode := runDSNCheck(t, empty)

	dangling := healthyMap()
	dangling["gateway"]["DATABASE_MIGRATION_URL"] = danglingDSN
	danglingOut, danglingErr, danglingCode := runDSNCheck(t, dangling)

	if emptyCode == 0 {
		t.Errorf("empty-password fixture: exit code = 0, want non-zero")
	}
	if danglingCode == 0 {
		t.Errorf("unrendered-reference fixture: exit code = 0, want non-zero")
	}

	// Vacuity guard -- see doc comment.
	if strings.Contains(emptyOut, "postgresql://") {
		t.Errorf("stdout echoed a raw DSN, which both leaks credentials and makes the reason-difference assertion vacuous; stdout = %q", emptyOut)
	}
	if strings.Contains(danglingOut, "postgresql://") {
		t.Errorf("stdout echoed a raw DSN; stdout = %q", danglingOut)
	}

	if emptyOut == danglingOut {
		t.Errorf("an unrendered ${{...}} reference and an empty password produced IDENTICAL output %q -- the two reasons have collapsed. They need different operator remedies and must be reported differently.", emptyOut)
	}

	assertNoSentinel(t, emptyOut, emptyErr)
	assertNoSentinel(t, danglingOut, danglingErr)
}

// T1-3. The false-positive guard. A gate that fails on a healthy fleet gets
// disabled by the first operator it blocks, so "clean input exits 0 and names
// no offender" is as load-bearing as any failure case.
//
// KILLS: M-invert (an inverted predicate flags all nine healthy entries).
func TestDSNCheckPassesFullyHealthyFleet(t *testing.T) {
	m := healthyMap()
	if got := len(m); got != 8 {
		t.Fatalf("fixture has %d services, want 8 (gateway + 7 context services)", got)
	}

	stdout, stderr, code := runDSNCheck(t, m)
	if code != 0 {
		t.Errorf("exit code = %d, want 0: every one of the 9 fleet DSNs is valid; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	// No offender may be named. Checking the service names directly is the
	// format-independent way to say "the offender list is empty".
	for svc := range m {
		if strings.Contains(stdout, svc) {
			t.Errorf("healthy fleet: stdout names service %q as an offender; stdout = %q", svc, stdout)
		}
	}
	assertNoSentinel(t, stdout, stderr)
}

// T1-4. A percent-encoded password must survive parsing. `%40` is an encoded
// `@`; an implementation that locates the userinfo/host boundary by string
// splitting rather than by net/url will mis-slice this DSN and can conclude
// the password is empty -- flagging a perfectly healthy credential.
//
// ON THE ORACLE. The task spec asserted "decoded password length is 11, not
// the 3 a naive first-`@` split yields". Neither number matches this fixture,
// so rather than transcribe a number that would bake in an arithmetic slip,
// the test COMPUTES the oracle with net/url below and asserts against that.
// (For the record, measured: the encoded form is 13 bytes, the correctly
// decoded password `p@ss/word` is 9, and a naive first-literal-`@` split
// yields the still-encoded 13 -- there is no 11 and no 3 here.)
//
// ON WHAT IS OBSERVABLE. The decoded length cannot be asserted at the CLI
// boundary: dsn-check must never print a credential or its length (T1-9), and
// asserting it in-process would require an exported parse function that does
// not exist yet (compile-RED -- see T1-2). So the oracle is established here
// against net/url, and the CLI is asserted on the behaviour that oracle
// implies: healthy encoded password => exit 0, not flagged.
//
// KILLS: naive string-split implementations.
func TestDSNCheckAcceptsPercentEncodedPassword(t *testing.T) {
	const encoded = "p%40ss%2Fword"
	const dsn = "postgresql://invoice_app:" + encoded + "@" + railwayHost

	// Establish the oracle: this is what a CORRECT parse yields, and it is
	// visibly not what a string split yields.
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("fixture DSN does not parse: %v", err)
	}
	decoded, hasPW := u.User.Password()
	if !hasPW {
		t.Fatalf("oracle: net/url found no password component in %q", dsn)
	}
	if decoded != "p@ss/word" {
		t.Fatalf("oracle: decoded password = %q, want %q", decoded, "p@ss/word")
	}
	if len(decoded) == len(encoded) {
		t.Fatalf("oracle is not discriminating: decoded and encoded lengths both %d", len(decoded))
	}

	m := healthyMap()
	m["invoice"]["DATABASE_URL"] = dsn

	stdout, stderr, code := runDSNCheck(t, m)
	if code != 0 {
		t.Errorf("exit code = %d, want 0: password %q decodes to %q (%d bytes), a valid non-empty credential -- flagging it means the parser split on a literal %q instead of using net/url; stdout = %q",
			code, encoded, decoded, len(decoded), "@", stdout)
	}
	if strings.Contains(stdout, "invoice") {
		t.Errorf("stdout flags service %q whose percent-encoded password is valid; stdout = %q", "invoice", stdout)
	}
	assertNoSentinel(t, stdout, stderr)
}

// T1-5. A required variable that is ABSENT is as fatal as one that is
// malformed -- the invoice service cannot boot without DATABASE_URL. A check
// that only validates the DSNs it happens to find passes a map that is
// missing half the fleet.
//
// KILLS: M-empty, present-only-checking.
func TestDSNCheckFlagsMissingRequiredVariable(t *testing.T) {
	m := healthyMap()
	delete(m["invoice"], "DATABASE_URL")

	stdout, stderr, code := runDSNCheck(t, m)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero: invoice.DATABASE_URL is required and absent")
	}
	if !strings.Contains(stdout, "invoice") {
		t.Errorf("stdout does not name the service %q with the missing required var; stdout = %q", "invoice", stdout)
	}
	if !strings.Contains(stdout, "DATABASE_URL") {
		t.Errorf("stdout does not name the missing variable %q; stdout = %q", "DATABASE_URL", stdout)
	}
	assertNoSentinel(t, stdout, stderr)
}

// T1-6 / T1-7, as one table because they are two halves of ONE property:
// notifications.DATABASE_URL is `if-present-must-be-valid`, not `required`.
//
// WHY. cmd/notifications/main.go opens no pool -- verified: the file contains
// no db/pgxpool/sql reference at all. Marking it `required` would fail the
// gate on a fleet that is completely healthy; marking it `never-check` would
// let a broken DSN through the day someone adds a pool. Only the middle
// severity is correct, and it takes BOTH directions to pin it: a test that
// only asserts one half is satisfied by the wrong severity.
//
// KILLS: M-sev in both directions -- flipping to `required` breaks the absent
// case, flipping to `never-check` breaks the present-but-broken case.
func TestDSNCheckNotificationsIsIfPresentMustBeValid(t *testing.T) {
	t.Run("absent is clean (T1-6)", func(t *testing.T) {
		m := healthyMap()
		delete(m["notifications"], "DATABASE_URL")

		stdout, stderr, code := runDSNCheck(t, m)
		if code != 0 {
			t.Errorf("exit code = %d, want 0: cmd/notifications/main.go opens no pool, so an absent DATABASE_URL is not a defect; stdout = %q", code, stdout)
		}
		if strings.Contains(stdout, "notifications") {
			t.Errorf("stdout flags notifications for an absent optional var; stdout = %q", stdout)
		}
		assertNoSentinel(t, stdout, stderr)
	})

	t.Run("present but broken is an offender (T1-7)", func(t *testing.T) {
		m := healthyMap()
		m["notifications"]["DATABASE_URL"] = "postgresql://invoice_app:@" + railwayHost

		stdout, stderr, code := runDSNCheck(t, m)
		if code == 0 {
			t.Errorf("exit code = 0, want non-zero: notifications.DATABASE_URL is present with an empty password -- optional means 'skip if absent', never 'never validate'")
		}
		if !strings.Contains(stdout, "notifications") {
			t.Errorf("stdout does not name %q; stdout = %q", "notifications", stdout)
		}
		if !strings.Contains(stdout, "DATABASE_URL") {
			t.Errorf("stdout does not name %q; stdout = %q", "DATABASE_URL", stdout)
		}
		assertNoSentinel(t, stdout, stderr)
	})
}

// T1-8. gateway.DATABASE_SUPERUSER_URL is optional-by-design. Promoting it to
// `required` would fail every guard-off boot -- internal/platform/db/
// provision.go:169-172 is the path that runs without a superuser URL -- so
// this is a guard against a severity table that is "safer" in a way that
// breaks a supported configuration.
//
// KILLS: superuser -> required.
func TestDSNCheckAllowsAbsentGatewaySuperuserURL(t *testing.T) {
	m := healthyMap()
	delete(m["gateway"], "DATABASE_SUPERUSER_URL")

	stdout, stderr, code := runDSNCheck(t, m)
	if code != 0 {
		t.Errorf("exit code = %d, want 0: DATABASE_SUPERUSER_URL is optional -- requiring it breaks every guard-off boot (internal/platform/db/provision.go:169-172); stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "DATABASE_SUPERUSER_URL") {
		t.Errorf("stdout flags the absent optional var DATABASE_SUPERUSER_URL; stdout = %q", stdout)
	}
	assertNoSentinel(t, stdout, stderr)
}

// T1-10. EVERY offender must be reported, not just the first. This is the
// difference between one deploy cycle and three: an operator who fixes the
// one DSN they were told about re-runs the gate and is handed the next one.
//
// KILLS: M-first (a check that returns on the first hit reports one of these
// three and exits).
func TestDSNCheckReportsEveryOffenderNotJustTheFirst(t *testing.T) {
	m := healthyMap()
	m["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN // empty password
	m["tenancy"]["DATABASE_URL"] = danglingDSN           // unrendered reference
	delete(m["submission"], "DATABASE_URL")              // absent required var

	stdout, stderr, code := runDSNCheck(t, m)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero: three simultaneous offenders")
	}

	var missing []string
	for _, svc := range []string{"gateway", "tenancy", "submission"} {
		if !strings.Contains(stdout, svc) {
			missing = append(missing, svc)
		}
	}
	if len(missing) > 0 {
		t.Errorf("stdout reports only some offenders -- missing %v. All three must appear so the operator fixes them in ONE cycle; stdout = %q", missing, stdout)
	}
	assertNoSentinel(t, stdout, stderr)
}

// T1-11. Malformed stdin must fail the gate, and must be DISTINGUISHABLE from
// "the map parsed fine and contained offenders". Those have opposite remedies:
// one means the renderer upstream is broken (and the fleet's real DSN health
// is UNKNOWN), the other means specific named variables need fixing. An
// implementation that treats unparseable input as an empty map exits 0 and
// reports a clean fleet it never actually looked at.
//
// The distinction is asserted structurally, not by prose matching -- same
// technique and same reason as T1-2: malformed output must not equal
// offenders-found output.
//
// Per the two-way exit contract this asserts only exit != 0, never a
// specific code.
//
// KILLS: M-swallow.
func TestDSNCheckRejectsMalformedInputDistinguishably(t *testing.T) {
	malformedOut, malformedErr, malformedCode := runDSNCheckRaw(t, "{")
	if malformedCode == 0 {
		t.Errorf("exit code = 0 on malformed stdin, want non-zero: unparseable input means the fleet's DSN health is UNKNOWN, which is not the same as clean")
	}

	offenders := healthyMap()
	offenders["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN
	offendersOut, _, _ := runDSNCheck(t, offenders)

	if malformedOut == offendersOut {
		t.Errorf("malformed-input output is IDENTICAL to offenders-found output %q -- an operator cannot tell 'your input is broken' from 'these variables are broken', and those have opposite remedies", malformedOut)
	}
	assertNoSentinel(t, malformedOut, malformedErr)
}

// T1-9, as its own aggregate test. assertNoSentinel already runs at every
// failing call site above; this test exists so the credential-hygiene
// property has ONE named owner that sweeps every failing shape in a single
// place, rather than surviving only as a side effect of other tests.
//
// KILLS: M-mask, and any debug-echo of the rendered map.
func TestDSNCheckNeverEchoesACredential(t *testing.T) {
	cases := []struct {
		desc string
		mut  func(dsnMap)
	}{
		{"empty password", func(m dsnMap) { m["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN }},
		{"unrendered reference", func(m dsnMap) { m["gateway"]["DATABASE_MIGRATION_URL"] = danglingDSN }},
		{"missing required var", func(m dsnMap) { delete(m["invoice"], "DATABASE_URL") }},
		{"broken optional var", func(m dsnMap) {
			m["notifications"]["DATABASE_URL"] = "postgresql://invoice_app:@" + railwayHost
		}},
		{"three offenders", func(m dsnMap) {
			m["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN
			m["tenancy"]["DATABASE_URL"] = danglingDSN
			delete(m["submission"], "DATABASE_URL")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			m := healthyMap()
			tc.mut(m)
			stdout, stderr, code := runDSNCheck(t, m)
			if code == 0 {
				t.Errorf("exit code = 0, want non-zero -- this case must FAIL for the credential-hygiene assertion below to be meaningful rather than vacuous")
			}
			// NON-VACUITY PRECONDITION. "Produced no output" trivially
			// satisfies "leaked no credential", so without this the whole
			// test passes against a binary that does nothing at all -- which
			// is exactly its state pre-implementation. Requiring an actual
			// offender report first is what makes the hygiene assertions
			// below load-bearing.
			if strings.TrimSpace(stdout) == "" {
				t.Errorf("stdout is empty -- an offender must be REPORTED before 'the report leaks nothing' means anything")
			}
			assertNoSentinel(t, stdout, stderr)
			// A whole-DSN echo leaks the credential even when the sentinel
			// happens not to be in the offending value (both incidentDSN and
			// danglingDSN are sentinel-free), so check the scheme too.
			if strings.Contains(stdout, "postgresql://") {
				t.Errorf("stdout echoed a raw DSN; report the service and variable NAME, never the value. stdout = %q", stdout)
			}
			if strings.Contains(stderr, "postgresql://") {
				t.Errorf("stderr echoed a raw DSN; stderr = %q", stderr)
			}
		})
	}
}
