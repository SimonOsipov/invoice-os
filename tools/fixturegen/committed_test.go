// committed_test.go is M4-11-02's (task-193) regeneration guard, authored
// RED before the fixture files are committed. It asserts three things about
// the committed set under repo-root testdata/invoices/ (reached from this
// package as ../../testdata/invoices/):
//
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
// its output, testdata/invoices/ does not exist, so all three tests fail on
// an explicit os.ReadFile/os.Stat error via t.Fatalf naming the missing
// file -- an assertion-style RED, not a panic or compile error.
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
