// committed_test.go verifies the fixture set committed under
// testdata/invoices/ (../../testdata/invoices/ from this package): the
// directory has exactly manifest's files, each matches a fresh
// regeneration byte-for-byte (the no-hand-edit guard), green_500.csv has
// the right shape, and nothing committed exceeds 1 MiB.
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

// TestCommittedDir_ContainsOnlyManifestFiles asserts the committed dir's
// file set is exactly manifest(...)'s name set -- catches a stray extra
// file the other tests below (which only iterate manifest) would miss.
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

	// 500 invoices x 3 line rows (genInvoices always emits 3 lines; see
	// gen.go's invoiceRec.lines) = 1,500 data rows.
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
