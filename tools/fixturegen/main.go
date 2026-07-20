// main.go implements the fixturegen CLI: `go run ./tools/fixturegen
// --seed S --invoices N --out DIR` writes the full committed fixture set
// -- one canonical green file, a second green file for demo variety, and
// five edge-case mutations -- into DIR, each produced by gen.go's
// deterministic builders.
//
// manifest is the single source of truth mapping each committed filename
// to the seed/invoice-count that produces it: every file's seed is a
// fixed offset from the base --seed (so passing a different --seed
// changes every file's content, not just one), and green files scale with
// the base --invoices while edge files use a small pinned count (their
// purpose is a specific defect shape, not dataset scale). M4-11-02's
// regeneration guard is expected to call manifest the same way -- with
// the same (seed, invoices) pair used to produce the committed files --
// to reconstruct the exact bytes it diffs against them, so a change here
// changes what "regenerated matches committed" means for BOTH subtasks:
// keep this the only list, do not fork a second one.
//
// buildOversized is deliberately absent from the manifest: it is an
// in-memory-only check (gen_test.go's TestGen_OversizedInflator_...) and
// is never written to disk.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// defaultSeed / defaultInvoices / defaultOut are the CLI flags' defaults,
// and also what a plain `go run ./tools/fixturegen` (no flags) produces --
// i.e. the exact (seed, invoices) pair whose output is expected to match
// the files committed under testdata/invoices/ in M4-11-02.
const defaultSeed = 42
const defaultInvoices = 500
const defaultOut = "testdata/invoices"

// edgeInvoices is the small, fixed invoice count every edge-case file
// uses regardless of --invoices -- these fixtures exist to exercise one
// specific defect shape, not to scale with the dataset the green files
// simulate.
const edgeInvoices = 20

// fixtureFile is one manifest entry: a committed filename plus the
// builder call that produces its bytes. build is a thunk (not the
// builder function directly) so entries can pin different seeds/counts
// per file while sharing one []fixtureFile literal.
type fixtureFile struct {
	name  string
	build func() []byte
}

// manifest returns the fixture manifest for a given base seed and base
// invoice count. Each file's seed is baseSeed plus a fixed per-file
// offset, so every file's content changes when baseSeed changes, and no
// two files ever share a seed.
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
