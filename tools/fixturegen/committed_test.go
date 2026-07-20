// committed_test.go is M4-11-02's (task-193) regeneration guard, authored
// RED before the fixture files are committed. It asserts four things about
// the committed set under repo-root testdata/invoices/ (reached from this
// package as ../../testdata/invoices/):
//
//  0. TestCommittedDir_ContainsOnlyManifestFiles -- the directory's file set
//     is exactly manifest(...)'s name set, no more, no less. Added post-hoc
//     during M4-11-02 QA to close a gap the other three tests share: they
//     all iterate manifest(...) and look up specific filenames, so a stray
//     extra committed file is invisible to (and unbounded by) all of them.
//  1. TestCommittedFixtures_MatchRegeneration -- every committed file is the
//     verbatim byte output of manifest(defaultSeed, defaultInvoices)'s
//     corresponding entry. This is the no-hand-edit / no-drift guard: any
//     future hand-edit to a committed CSV, or any change to gen.go/main.go
//     that shifts what defaultSeed/defaultInvoices produce without
//     re-running the generator, fails this test.
//  2. TestCommittedGreen500_Dimensions -- green_500.csv has the canonical
//     header and exactly 500 invoices x 3 line rows = 1,500 data rows
//     (genInvoices always emits 3 lines per invoice; see gen.go).
//  3. TestNoOversizedBlobCommitted -- no committed file exceeds 1 MiB.
//
// Until M4-11-02's emit step runs `go run ./tools/fixturegen` and commits
// its output, testdata/invoices/ does not exist, so all three original
// tests fail on an explicit os.ReadFile/os.Stat error via t.Fatalf naming
// the missing file -- an assertion-style RED, not a panic or compile error.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// committedDir is the path from this package (tools/fixturegen/) to the
// repo-root directory the committed fixture set lives under.
var committedDir = filepath.Join("..", "..", "testdata", "invoices")

// --- 0: TestCommittedDir_ContainsOnlyManifestFiles ---------------------------

// TestCommittedDir_ContainsOnlyManifestFiles closes a gap the other three
// tests below all share: each of them iterates manifest(...) and looks up
// one specific committed filename, so a file present in committedDir but
// NOT listed in manifest(...) is invisible to every one of them --
// including TestNoOversizedBlobCommitted, whose 1 MiB bound only ever
// applies to the seven manifest entries. A stray extra file (leftover
// generator output from a different --seed/--invoices run, a hand-created
// scratch file, an editor backup) would sit in testdata/invoices/ silently
// uncompared and unbounded. This test lists committedDir directly and
// asserts its file set is exactly the manifest's name set, catching that
// case regardless of the stray file's content or size.
func TestCommittedDir_ContainsOnlyManifestFiles(t *testing.T) {
	entries, err := os.ReadDir(committedDir)
	if err != nil {
		t.Fatalf("reading committed dir %s: %v", committedDir, err)
	}

	want := make(map[string]bool)
	for _, f := range manifest(defaultSeed, defaultInvoices) {
		want[f.name] = true
	}

	got := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("committed dir %s contains unexpected subdirectory %q, want only the manifest's files", committedDir, e.Name())
			continue
		}
		got[e.Name()] = true
	}

	for name := range got {
		if !want[name] {
			t.Errorf("committed dir %s contains %q, which is not in manifest(defaultSeed, defaultInvoices) -- a stray file here is invisible to TestCommittedFixtures_MatchRegeneration and TestNoOversizedBlobCommitted, both of which only iterate the manifest", committedDir, name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("committed dir %s is missing manifest file %q", committedDir, name)
		}
	}
}

// --- 1: TestCommittedFixtures_MatchRegeneration -----------------------------

func TestCommittedFixtures_MatchRegeneration(t *testing.T) {
	for _, f := range manifest(defaultSeed, defaultInvoices) {
		f := f
		t.Run(f.name, func(t *testing.T) {
			path := filepath.Join(committedDir, f.name)
			committed, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("committed fixture %s missing -- M4-11-02 must emit it via `go run ./tools/fixturegen`: %v", f.name, err)
			}
			regenerated := f.build()
			if !bytes.Equal(committed, regenerated) {
				t.Errorf("committed fixture %s does not match manifest(defaultSeed, defaultInvoices)'s regenerated output -- it was hand-edited or has drifted from the generator; got %d bytes, want %d bytes", f.name, len(committed), len(regenerated))
			}
		})
	}
}

// --- 2: TestCommittedGreen500_Dimensions ------------------------------------

func TestCommittedGreen500_Dimensions(t *testing.T) {
	path := filepath.Join(committedDir, "green_500.csv")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("committed fixture green_500.csv missing -- M4-11-02 must emit it via `go run ./tools/fixturegen`: %v", err)
	}

	header, rows := mustParseCSV(t, data)

	if len(header) != len(canonicalHeaderCols) {
		t.Fatalf("green_500.csv header has %d fields, want %d (canonical); header = %v", len(header), len(canonicalHeaderCols), header)
	}
	for i, h := range header {
		if h != canonicalHeaderCols[i] {
			t.Errorf("green_500.csv header field %d = %q, want %q", i, h, canonicalHeaderCols[i])
		}
	}

	// defaultInvoices (500) invoices x 3 line rows/invoice (genInvoices
	// always emits exactly 3 lines -- see gen.go's invoiceRec.lines [3]lineRec
	// and TestGen_InvoiceCountFollowsFlag) = 1,500 data rows, exactly, since
	// generateGreen(defaultSeed, defaultInvoices) is deterministic.
	const wantInvoices = defaultInvoices
	const wantRows = wantInvoices * 3
	if len(rows) != wantRows {
		t.Fatalf("green_500.csv has %d data rows, want %d (%d invoices x 3 line rows)", len(rows), wantRows, wantInvoices)
	}

	groups := groupByInvoice(rows)
	if len(groups) != wantInvoices {
		t.Errorf("green_500.csv has %d distinct Invoice No groups, want %d", len(groups), wantInvoices)
	}
}

// --- 3: TestNoOversizedBlobCommitted -----------------------------------------

func TestNoOversizedBlobCommitted(t *testing.T) {
	const maxBytes = 1 << 20 // 1 MiB

	for _, f := range manifest(defaultSeed, defaultInvoices) {
		f := f
		t.Run(f.name, func(t *testing.T) {
			path := filepath.Join(committedDir, f.name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("committed fixture %s missing -- M4-11-02 must emit it via `go run ./tools/fixturegen`: %v", f.name, err)
			}
			if info.Size() >= maxBytes {
				t.Errorf("committed fixture %s is %d bytes, want < %d (1 MiB) -- no oversized blob should be committed (buildOversized is intentionally excluded from the manifest)", f.name, info.Size(), maxBytes)
			}
		})
	}
}
