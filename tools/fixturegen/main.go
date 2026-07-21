// Package main implements fixturegen: it emits the committed synthetic
// invoice fixtures under testdata/invoices/.
//
// Regenerate with:
//
//	go run ./tools/fixturegen --seed 42 --invoices 500 --out testdata/invoices
//
// Committed files are generated output -- do not hand-edit them;
// TestCommittedFixtures_MatchRegeneration (committed_test.go) enforces it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// defaultSeed / defaultInvoices / defaultOut are the CLI flags' defaults --
// also the (seed, invoices) pair testdata/invoices/ was regenerated from.
const defaultSeed = 42
const defaultInvoices = 500
const defaultOut = "testdata/invoices"

// edgeInvoices is the fixed invoice count every edge-case file uses,
// regardless of --invoices.
const edgeInvoices = 20

// fixtureFile is one manifest entry: a filename plus a thunk that builds
// its bytes (a thunk so each entry can pin its own seed/count).
type fixtureFile struct {
	name  string
	build func() []byte
}

// manifest returns the fixture manifest for a base seed/invoice count;
// each file's seed is baseSeed plus a fixed offset, so every file changes
// when baseSeed changes.
func manifest(baseSeed int64, baseInvoices int) []fixtureFile {
	return []fixtureFile{
		{"green_500.csv", func() []byte { return generateGreen(baseSeed, baseInvoices) }},
		{"green_second.csv", func() []byte { return generateGreen(baseSeed+1, baseInvoices/2) }},
		{"edge_missing_columns.csv", func() []byte { return buildEdgeMissingColumns(baseSeed+2, edgeInvoices) }},
		{"edge_in_file_dupes.csv", func() []byte { return buildEdgeInFileDupes(baseSeed + 3) }},
		{"edge_bad_encoding.csv", func() []byte { return buildEdgeBadEncoding(baseSeed+4, edgeInvoices) }},
		{"edge_bad_tin.csv", func() []byte { return buildEdgeBadTin(baseSeed+5, edgeInvoices) }},
		{"edge_vat_math_wrong.csv", func() []byte { return buildEdgeVatMathWrong(baseSeed+6, edgeInvoices) }},
	}
}

func main() {
	seed := flag.Int64("seed", defaultSeed, "base seed for the fixture manifest; each committed file derives its own seed as a fixed offset from this")
	invoices := flag.Int("invoices", defaultInvoices, "base invoice count for the fixture manifest's green files (edge files use a small fixed count)")
	out := flag.String("out", defaultOut, "output directory for the fixture set")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "fixturegen: creating output dir %q: %v\n", *out, err)
		os.Exit(1)
	}

	for _, f := range manifest(*seed, *invoices) {
		data := f.build()
		path := filepath.Join(*out, f.name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "fixturegen: writing %q: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(data))
	}
}
