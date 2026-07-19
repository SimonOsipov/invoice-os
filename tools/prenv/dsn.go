// dsn.go implements `prenv dsn-check` (M4-22-FU-01, task-178): given the
// RENDERED {service: {variable: value}} variable map of a Railway environment,
// report every DB DSN that is unset, empty-passworded, or still holding an
// unrendered `${{...}}` reference.
//
// M4-22 was a variable RENAME. The DSNs kept interpolating the deleted names,
// so they rendered with an EMPTY password: every service booted and then failed
// to authenticate. Only the RENDERED map shows that -- the stored variable
// still looks correct.
//
// CheckDSNs is pure (no network, no environment reads, no globals), same shape
// as ShouldReap in sweep.go and for the same reason: every branch is testable
// without a live environment.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Severity is how strictly one DSN variable is required.
type Severity int

const (
	// Required: the service opens a pool at boot and cannot run without it.
	Required Severity = iota
	// IfPresent: absent is fine, present-but-broken is not. It never means
	// "do not check" -- that would let a broken DSN through the day someone
	// adds a pool.
	IfPresent
)

// DSNRequirement is one row of the severity table.
type DSNRequirement struct {
	Service  string
	Variable string
	Severity Severity
}

// DSNRequirements is the severity table: the codified invariant this whole
// story exists to state. Verified against cmd/*/main.go and corroborated by a
// live read-back of `development` (.ralph/ac3-development-dsn-readback.md).
//
// This is the ONLY copy. scripts/ci/railway-env.sh deliberately does not know
// it -- it ships the whole environment here and lets this table decide.
//
// The order is fixed so the offender list is reproducible run to run.
var DSNRequirements = []DSNRequirement{
	{"gateway", "DATABASE_MIGRATION_URL", Required},
	// Optional by design: internal/platform/db/provision.go:169-172 is the
	// guard-off boot path that runs without a superuser URL. Promoting this to
	// Required would fail a supported configuration.
	{"gateway", "DATABASE_SUPERUSER_URL", IfPresent},
	{"tenancy", "DATABASE_URL", Required},
	{"portfolio", "DATABASE_URL", Required},
	{"invoice", "DATABASE_URL", Required},
	{"validation", "DATABASE_URL", Required},
	{"dashboard", "DATABASE_URL", Required},
	{"submission", "DATABASE_URL", Required},
	// cmd/notifications/main.go opens no pool -- it has no db/pgxpool/sql
	// reference at all -- so an absent DATABASE_URL is not a defect there.
	{"notifications", "DATABASE_URL", IfPresent},
}

// DSNDefect is why a variable is an offender. The defects are kept DISTINCT
// because they have different operator remedies: fix the variable reference,
// set the password, or set the variable at all.
type DSNDefect string

const (
	DefectMissing       DSNDefect = "is required but not set"
	DefectEmptyValue    DSNDefect = "is set to an empty value"
	DefectUnrendered    DSNDefect = "still holds an unrendered ${{...}} variable reference"
	DefectUnparseable   DSNDefect = "is not a parseable URL"
	DefectNoPassword    DSNDefect = "has no password component"
	DefectEmptyPassword DSNDefect = "has an empty password component"
)

// Offender is one failing (service, variable) pair. It carries NAMES only --
// never the value, which is a live credential.
type Offender struct {
	Service  string
	Variable string
	Defect   DSNDefect
}

func (o Offender) String() string {
	return fmt.Sprintf("  %s %s %s", o.Service, o.Variable, o.Defect)
}

// CheckDSNs walks the severity table against a rendered map and returns EVERY
// offender, never just the first: an operator who is handed one defect at a
// time needs one deploy cycle per defect.
//
// Services present in the map but absent from the table are ignored -- the
// caller ships the whole environment, including services that hold no DSN.
func CheckDSNs(rendered map[string]map[string]string) []Offender {
	var offenders []Offender
	for _, req := range DSNRequirements {
		value, present := rendered[req.Service][req.Variable]
		if !present {
			if req.Severity == Required {
				offenders = append(offenders, Offender{req.Service, req.Variable, DefectMissing})
			}
			continue
		}
		if defect, bad := inspectDSN(value); bad {
			offenders = append(offenders, Offender{req.Service, req.Variable, defect})
		}
	}
	return offenders
}

// inspectDSN classifies one rendered DSN value.
//
// The password is located with net/url, NOT by splitting on `@` or `:`: a
// password may legitimately contain percent-encoded `@` and `/`, and a split
// mis-slices those into a password that looks empty -- flagging a healthy
// credential.
func inspectDSN(value string) (DSNDefect, bool) {
	if strings.TrimSpace(value) == "" {
		return DefectEmptyValue, true
	}
	// Checked BEFORE parsing: an unrendered reference is its own defect with
	// its own remedy, and it can still parse as a well-formed URL.
	if strings.Contains(value, "${{") {
		return DefectUnrendered, true
	}
	u, err := url.Parse(value)
	if err != nil {
		return DefectUnparseable, true
	}
	if u.User == nil {
		return DefectNoPassword, true
	}
	password, hasPassword := u.User.Password()
	if !hasPassword {
		return DefectNoPassword, true
	}
	if password == "" {
		return DefectEmptyPassword, true
	}
	return "", false
}

// RunDSNCheck reads the rendered map from in, writes its report to out, and
// returns the process exit code.
//
// TWO-WAY EXIT: 0 = clean, 1 = fail. The two failure modes -- unreadable input
// and offenders found -- deliberately SHARE an exit code: both fail the gate,
// and `go run` collapses every non-zero exit to 1 anyway (main.go:52). They are
// told apart by the MESSAGE, which is what an operator reads.
//
// The report goes to STDOUT, following sweep-decide rather than `parse`: here
// the report IS the answer, not a diagnostic about a malformed call.
//
// The input is NEVER echoed back, on any path -- it carries live credentials.
func RunDSNCheck(in io.Reader, out io.Writer) int {
	raw, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintln(out, "dsn-check: could not read the rendered variable map on stdin. DSN health is UNKNOWN, which is NOT the same as clean.")
		return 1
	}

	var rendered map[string]map[string]string
	if err := json.Unmarshal(raw, &rendered); err != nil {
		fmt.Fprintln(out, "dsn-check: stdin is not a valid rendered variable map (expected a JSON object of {service: {variable: value}}). DSN health is UNKNOWN, which is NOT the same as clean.")
		return 1
	}

	offenders := CheckDSNs(rendered)
	if len(offenders) == 0 {
		fmt.Fprintf(out, "DSN check clean: 0 defect(s) across the %d-row severity table.\n", len(DSNRequirements))
		return 0
	}

	fmt.Fprintf(out, "DSN check FAILED: %d defect(s). A DSN that is unset, empty-passworded, or unrendered boots the service and then fails to authenticate (the M4-22 incident). Offenders:\n", len(offenders))
	for _, o := range offenders {
		fmt.Fprintln(out, o.String())
	}
	return 1
}
