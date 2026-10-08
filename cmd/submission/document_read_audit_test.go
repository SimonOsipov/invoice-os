// document_read_audit_test.go: where the review screen's audit row is SPELLED, what it CARRIES,
// where its recorder is WIRED, and where the new route is DECLARED. Nothing here serves a mux or
// opens a database (main() is not unit-testable, main_test.go:1-4): the row is read off a
// recording tx, everything else off the source.
//
// Helpers use a dr* prefix; ea wt ds are taken.
package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

const (
	drReadEvent   = "document.read"
	drAdapterFn   = "newDocumentReadAuditor"
	drExtractionD = "internal/extraction/"

	// ls internal/extraction/*.go | grep -v _test | wc -l -> 26 at 9ed1c501. A floor, not an
	// equality: the point is that the walk resolved a real directory, not that the package
	// stopped growing.
	drMinExtractionFiles = 26

	// A production string literal that IS under internal/extraction today (worker.go:28). The
	// absence proof below is worthless without it: a matcher that finds nothing anywhere
	// reports a clean extraction package too.
	drNeedleLiteral = "extraction-worker"
)

// drExtractionFiles narrows eaProdFiles' walk to internal/extraction and floors it.
func drExtractionFiles(t *testing.T, files []string) []string {
	t.Helper()
	var out []string
	for _, rel := range files {
		if strings.HasPrefix(rel, drExtractionD) {
			out = append(out, rel)
		}
	}
	if len(out) < drMinExtractionFiles {
		t.Fatalf("the walk found %d non-test .go file(s) under %s, want at least %d (26 measured) -- a scan over a path that stopped resolving reports all-clear",
			len(out), drExtractionD, drMinExtractionFiles)
	}
	return out
}

// AC 8's placement half. The event literal belongs in cmd/, never in internal/extraction: a
// const identifier inside the package reads as a non-literal to internal/platform/db's repo-wide
// audit.Record scan and lands the call site in no bucket (newExtractionAuditor's own rule).
func TestNewDocumentReadAuditor_SpellsTheEventInCmd(t *testing.T) {
	root, files := eaProdFiles(t)
	extractionFiles := drExtractionFiles(t, files)

	// Control needle: the literal matcher must find a literal that IS in internal/extraction,
	// or the zero-hit clearance below is a broken walk reporting an empty set.
	if got := eaLiteralSites(t, root, extractionFiles, drNeedleLiteral); len(got) != 1 {
		t.Fatalf("the literal matcher found %q at %v under %s, want exactly 1 site -- the matcher is broken, so the counts below answer a different question",
			drNeedleLiteral, got, drExtractionD)
	}

	sites := eaLiteralSites(t, root, files, drReadEvent)

	// Second control: internal/document/document.go:139 spells this exact string today. Without
	// this the whole test could pass on a matcher that never matches anything.
	var sawDocumentPkg bool
	for _, s := range sites {
		if strings.HasPrefix(s, "internal/document/") {
			sawDocumentPkg = true
		}
	}
	if !sawDocumentPkg {
		t.Fatalf("no %q literal found under internal/document (document.go:139 spells it) -- the matcher is broken; sites=%v", drReadEvent, sites)
	}

	var inCmd []string
	for _, s := range sites {
		if strings.HasPrefix(s, "cmd/submission/main.go:") {
			inCmd = append(inCmd, s)
		}
	}
	if len(inCmd) == 0 {
		t.Errorf("%q is spelled as a production literal at %v, none of them in cmd/submission/main.go -- %s is the composition root's job", drReadEvent, sites, drAdapterFn)
	}

	if got := eaLiteralSites(t, root, extractionFiles, drReadEvent); len(got) != 0 {
		t.Errorf("%q is spelled as a production literal under %s at %v -- a literal there drops the call site out of the repo-wide audit.Record partition",
			drReadEvent, drExtractionD, got)
	}
}

// AC 8's wiring half. Every audit assertion in internal/extraction injects its own recorder, so
// all of them stay green on a fleet that registered the route over a Reader carrying none. This
// is the only thing that says production audits anything.
//
// Field-name agnostic on purpose: it asks that SOME extraction.Reader literal is built with the
// adapter's return value, which survives the reader being hoisted into a local.
func TestSubmissionMain_WiresTheDocumentReadAuditorOntoAReader(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	var readers, wired int
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Reader" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
			return true
		}
		readers++
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if call, ok := kv.Value.(*ast.CallExpr); ok && wtCallName(call.Fun) == drAdapterFn {
				wired++
			}
		}
		return true
	})

	// Control needle: main() already builds an extraction.Reader literal, so zero here means the
	// scan is broken rather than the wiring absent.
	if readers == 0 {
		t.Fatal("no extraction.Reader composite literal found in cmd/submission/main.go -- the scan is broken, so the assertion below is vacuous")
	}
	if wired == 0 {
		t.Errorf("none of the %d extraction.Reader literal(s) in cmd/submission/main.go carries a %s() call -- the detail route would then serve 200s and audit nothing, with every injected-recorder test still green",
			readers, drAdapterFn)
	}
}

// drSubjects are two distinct signed-in callers. Two, because one fixture cannot tell a
// pass-through from an adapter returning a constant that happens to equal it.
var drSubjects = []string{
	"6f1b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
	"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
}

const drDocumentID = "9ab1c2d3-e4f5-4a6b-8c7d-9e0f1a2b3c4d"

// AC 8's actor, read off the row the adapter writes. eaMainSites drops this site out of
// TestExtractionAuditor_ActorIsSystemByValue's population -- rightly, its actor is a parameter --
// and internal/extraction's audit specs inject their own recorder, so none of them sees the
// argument THIS adapter binds. Without this, the actor and the payload key are both unread.
func TestNewDocumentReadAuditor_WritesTheCallersSubjectAsActor(t *testing.T) {
	if len(drSubjects) < 2 {
		t.Fatalf("the subject table holds %d entry(ies), want at least 2 -- one fixture cannot tell a pass-through from a constant", len(drSubjects))
	}
	record := newDocumentReadAuditor()

	for _, subject := range drSubjects {
		t.Run(subject, func(t *testing.T) {
			tx := &eaTx{}
			if err := record(context.Background(), tx, subject, drDocumentID); err != nil {
				t.Fatalf("the recorder returned %v, want nil", err)
			}
			row := eaDecodeOne(t, tx)

			if row.actor != subject {
				t.Errorf("the row names actor %q, want the caller's own subject %q -- a document.read naming the wrong reader is worse than none, because the trail is what this product sells", row.actor, subject)
			}
			if row.actor == eaActor {
				t.Errorf("the row names the %q literal; this read has a signed-in caller, and internal/document's Get audits the same event as caller.Subject", eaActor)
			}
			if row.event != drReadEvent {
				t.Errorf("the row carries event %q, want %q", row.event, drReadEvent)
			}
			want := map[string]any{"id": drDocumentID}
			if !reflect.DeepEqual(row.payload, want) {
				t.Errorf("the row carries payload %v, want exactly %v -- internal/document writes this event under the same single key, and two spellings of one event's payload cannot both be read by one consumer", row.payload, want)
			}
		})
	}
}

// drWantAuditWriters is cmd/submission/main.go's COMPLETE audit.Record population, keyed on the
// enclosing func.
var drWantAuditWriters = map[string]int{eaAdapterFn: 2, drAdapterFn: 1, fcAdapterFn: 1, alAdapterFn: 1}

// eaMainSites floors main.go's site count and then narrows to newExtractionAuditor, so every
// assertion in extraction_audit_test.go is blind to a writer added elsewhere in the file --
// including a second one inside newDocumentReadAuditor, which would write two rows per screen
// open while every injected-recorder count in internal/extraction still read 1. The file-wide
// equality that filter gave up lives here.
func TestSubmissionMain_AuditWritersAreEachAccountedFor(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v -- a file the scan cannot read is a file it reports clean on", err)
	}
	sites, lits := eaSitesIn(fset, "cmd/submission/main.go", f)
	if lits < eaMinFuncLits {
		t.Fatalf("walked %d function literal(s) in cmd/submission/main.go, want at least %d -- every audit.Record here sits inside one, so a walk that descends into nothing finds no call site either", lits, eaMinFuncLits)
	}

	got := map[string]int{}
	for _, s := range sites {
		got[s.fn]++
	}
	if !reflect.DeepEqual(got, drWantAuditWriters) {
		t.Errorf("cmd/submission/main.go audits from %v, want exactly %v -- a new writer is a new row shape nothing in this package classifies; give it its own assertions and name it here", got, drWantAuditWriters)
	}
}
