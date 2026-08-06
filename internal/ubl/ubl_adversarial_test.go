// ubl_adversarial_test.go: BUG-04-01 (task-397) RED specs (QA Mode A) -- the purity,
// zero-value and import-boundary rows of the Test Specs table. Shares completeCanonical,
// wellFormed and mustRender with ubl_test.go.
package ubl_test

import (
	"bytes"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/ubl"
)

const submissionPkg = "github.com/SimonOsipov/invoice-os/internal/submission"

// deepCopyCanonical clones every pointer and the Lines backing array. A shallow copy would
// make the DeepEqual below vacuous: both copies would share the same *string, so a renderer
// that wrote through one would change the "before" snapshot too.
func deepCopyCanonical(c submission.Canonical) submission.Canonical {
	cp := c
	cp.IssueDate = nil
	if c.IssueDate != nil {
		d := *c.IssueDate
		cp.IssueDate = &d
	}
	cp.Supplier = submission.Party{TIN: copyStr(c.Supplier.TIN), Name: copyStr(c.Supplier.Name)}
	cp.Buyer = submission.Party{TIN: copyStr(c.Buyer.TIN), Name: copyStr(c.Buyer.Name)}
	cp.Currency = copyStr(c.Currency)
	cp.Subtotal = copyStr(c.Subtotal)
	cp.VAT = copyStr(c.VAT)
	cp.Total = copyStr(c.Total)
	cp.Lines = nil
	for _, l := range c.Lines {
		cp.Lines = append(cp.Lines, submission.CanonicalLine{
			LineID: l.LineID, LineNo: l.LineNo,
			Description: copyStr(l.Description), Quantity: copyStr(l.Quantity),
			UnitPrice: copyStr(l.UnitPrice), LineTotal: copyStr(l.LineTotal), LineTax: copyStr(l.LineTax),
		})
	}
	return cp
}

func copyStr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func TestRender_NeverMutatesTheCanonical(t *testing.T) {
	c := completeCanonical(t)
	before := deepCopyCanonical(c)

	mustRender(t, c)

	if !reflect.DeepEqual(c, before) {
		t.Errorf("Render mutated its input\n after: %#v\nbefore: %#v", c, before)
	}
}

func TestRender_ZeroCanonicalDoesNotPanic(t *testing.T) {
	out, err := ubl.Render(submission.Canonical{})

	if !errors.Is(err, ubl.ErrIncomplete) {
		t.Errorf("Render(zero) error = %v, want it to wrap ErrIncomplete", err)
	}
	if out != nil {
		t.Errorf("Render(zero) returned %d bytes, want nil", len(out))
	}
}

// TestRender_ControlCharactersStillProduceParseableXML: \x00 and \x0b are outside the XML
// character range. Go replaces them with U+FFFD rather than emitting an entity, so the
// document still parses. Measured.
func TestRender_ControlCharactersStillProduceParseableXML(t *testing.T) {
	c := completeCanonical(t)
	c.Lines[0].Description = ublStr("Widget\x00\x0bZ")

	out := mustRender(t, c)

	if err := wellFormed(out); err != nil {
		t.Errorf("document carrying control characters is not well-formed: %v\n---\n%s", err, out)
	}
	if bytes.ContainsRune(out, 0x00) {
		t.Error("output carries a raw NUL byte")
	}
	if bytes.ContainsRune(out, 0x0b) {
		t.Error("output carries a raw vertical-tab byte")
	}
	wantContains(t, out, "Widget", "the printable part of the description survives")
	if !bytes.ContainsRune(out, '\uFFFD') {
		t.Error("expected the out-of-range runes to become U+FFFD")
	}
}

// TestUBL_ImportsNoDatabaseOrTransport uses DIRECT imports, not `go list -deps`:
// internal/submission itself imports pgx and net/http, so a -deps assertion would fail on a
// CORRECT implementation. This is a baseline/regression guard -- it is green from the moment
// it is written and its job is to catch a future PR that reaches for a DB or a transport
// inside a pure renderer. It also pins [ubl-lives-in-its-own-package]: internal/submission is
// the ONLY non-stdlib package internal/ubl may import.
//
// The repo-root helper is duplicated from internal/submission/deps_test.go:44-52 rather than
// imported, for the reason stated there.
func TestUBL_ImportsNoDatabaseOrTransport(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", "./internal/ubl")
	cmd.Dir = repoRootForUBLTest(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list ./internal/ubl: %v\n%s", err, out)
	}

	var imports []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			imports = append(imports, line)
		}
	}
	// Non-vacuity: an empty or unresolved import list must not read as a clean bill of health.
	if len(imports) == 0 {
		t.Fatalf("go list returned no imports for ./internal/ubl -- the assertions below would be vacuous\n%s", out)
	}
	var sawSubmission bool

	for _, imp := range imports {
		switch {
		case imp == "net/http" || strings.HasPrefix(imp, "net/http/"):
			t.Errorf("internal/ubl imports %s -- the renderer is pure, it does no transport", imp)
		case strings.HasPrefix(imp, "github.com/jackc/"):
			t.Errorf("internal/ubl imports %s -- the renderer is pure, it does no database access", imp)
		}
		// A dot before the first path segment separator marks a non-stdlib path.
		first, _, _ := strings.Cut(imp, "/")
		if !strings.Contains(first, ".") {
			continue
		}
		if imp != submissionPkg {
			t.Errorf("internal/ubl imports %s; the only non-stdlib import it may have is %s", imp, submissionPkg)
			continue
		}
		sawSubmission = true
	}

	if !sawSubmission {
		t.Errorf("internal/ubl does not import %s -- Render and Missing take a submission.Canonical", submissionPkg)
	}
}

func repoRootForUBLTest(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}
