// Two source scans over the shipped retention and boot-rationale claims. Both
// assert an ABSENCE — that no uncorrected claim is left standing — which is the
// instrument class that reports all-clear while examining nothing. So both carry
// the same three defences: a planted control needle, a population floor whose
// enumerated sites must STILL match their needle after correction, and a
// discovery walk whose every exemption must still match something.
//
// A correction SCOPES a claim; it never deletes it. A site that stops matching
// its needle is a deletion, and fails here.
//
// Names are TestPurge* deliberately — ci.yml's -run alternation is what makes
// them run in CI at all (TestCIRunFiltersReachEveryTestInThePackage). Neither
// test touches a database.
//
// frontend/** and e2e/** are out of both walks: no ci.yml path filter routes a
// frontend-only or e2e-only commit to the Go job, so a Go assertion over them
// would be unreachable on exactly the commit it guards. SourceDocumentRail's
// copy is held by SourceDocumentRail.test.tsx instead.
//
// Known limits. The needles are literals, so a claim reworded around all of them
// escapes the discovery walk; an ENUMERATED site still fails, because its needle
// must keep matching. Scanner 1 walks no .yml, and blocksOf reads only // and --
// runs, so a Go string literal or a SQL COMMENT ON is invisible to both.
package db_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// exceptionDoc is the one doc every corrected claim must point at. It does not
// exist yet; the "exception doc exists" subtest asserts that deliberately.
const exceptionDoc = "docs/demo-reset.md"

// repoRootDir locates the worktree root: `go test` runs with cwd set to this
// package's directory, and every path below is repo-relative.
func repoRootDir(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root; every scan below would read nothing")
	}
	return root
}

// claimBlock is the unit a claim is read in: one contiguous comment run for
// .go/.sql/.yml, one paragraph for .md. line is 1-based; text is FLOWED —
// comment markers stripped, every run of whitespace collapsed to one space — so
// a needle matches whether or not the sentence wraps. Without that, rewrapping a
// corrected comment silently deletes the needle the floor below depends on.
type claimBlock struct {
	line int
	text string
}

var whitespaceRun = regexp.MustCompile(`\s+`)

// flowLines strips prefix from each line and joins them into one line.
func flowLines(prefix string, lines []string) string {
	var parts []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if prefix != "" {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
		parts = append(parts, trimmed)
	}
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(strings.Join(parts, " "), " "))
}

func commentPrefix(path string) string {
	switch filepath.Ext(path) {
	case ".go":
		return "//"
	case ".sql":
		return "--"
	case ".yml", ".yaml":
		return "#"
	}
	return ""
}

// blocksOf splits src into claim blocks. Scoping a claim means editing the
// sentence around it, so the enclosing block — not the line — is what must
// carry the exception.
func blocksOf(path, src string) []claimBlock {
	prefix := commentPrefix(path)
	inBlock := func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if prefix == "" {
			return trimmed != ""
		}
		return strings.HasPrefix(trimmed, prefix)
	}

	var out []claimBlock
	start, cur := -1, []string(nil)
	flush := func() {
		if start >= 0 {
			out = append(out, claimBlock{line: start + 1, text: flowLines(prefix, cur)})
		}
		start, cur = -1, nil
	}
	for i, line := range strings.Split(src, "\n") {
		if !inBlock(line) {
			flush()
			continue
		}
		if start < 0 {
			start = i
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

// claimText returns every flowed claim block in a repo-relative file, plus them
// joined for whole-file floor checks. Joining on a newline keeps a needle from
// matching across a block boundary.
func claimText(t *testing.T, root, rel string) ([]claimBlock, string) {
	t.Helper()
	blocks := blocksOf(rel, readRepoFile(t, root, rel))
	texts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		texts = append(texts, b.text)
	}
	return blocks, strings.Join(texts, "\n")
}

// matched returns the needles present in text, case-insensitively.
func matched(text string, needles []string) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, n := range needles {
		if strings.Contains(lower, strings.ToLower(n)) {
			out = append(out, n)
		}
	}
	return out
}

// readRepoFile reads a repo-relative path. A missing enumerated site is an
// instrument failure, not a finding: the scan can no longer prove anything
// about it.
func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read enumerated site %s: %v — the scan cannot report on a file it cannot read", rel, err)
	}
	return string(b)
}

// walkRepo returns every repo-relative path under roots whose extension is in
// exts, skipping node_modules and .git.
func walkRepo(t *testing.T, root string, roots, exts []string) []string {
	t.Helper()
	want := map[string]bool{}
	for _, e := range exts {
		want[e] = true
	}
	var out []string
	for _, r := range roots {
		// "." is the repo root's own files only; recursing there would walk
		// node_modules, frontend/ and e2e/, which are deliberately out of scope.
		if r == "." {
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("read repo root: %v", err)
			}
			for _, e := range entries {
				if !e.IsDir() && want[filepath.Ext(e.Name())] {
					out = append(out, e.Name())
				}
			}
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(r)), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "node_modules" || name == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !want[filepath.Ext(path)] {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", r, err)
		}
	}
	sort.Strings(out)
	return out
}

// sortedKeys keeps failure output stable across runs.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Scanner 1 — the retention claim
// ---------------------------------------------------------------------------

// retentionNeedles are the shipped permanence wordings. Precise on purpose:
// docs/approvals.md's "Permanent retention is a safe default" is a policy
// statement about an unconfirmed FIRS/NRS requirement, not a claim about
// shipped behaviour, and no needle reaches it.
var retentionNeedles = []string{
	"permanently retained",
	"retained **permanently**",
	"retention is permanent",
	"permanent record",
	"permanent/append-only",
	"append-only/permanent",
	"permanent dedupe ledger",
	"no purge job",
	"never deleted by the app",
	"owner-proof trigger",
	"immutable trail",
}

// retentionWantSites maps each enumerated site to the needles that must STILL
// match it after correction.
var retentionWantSites = map[string][]string{
	"docs/approvals.md": {
		"permanently retained",
		"no purge job",
		"retained **permanently**",
	},
	"docs/migrations.md": {"permanent/append-only"},
	// Page-image OBJECTS are never deleted by the app; the purge takes only the rows
	// that made them findable.
	"docs/page-image-storage.md": {"never deleted by the app"},
	"migrations/20260708062657_audit_log.sql": {
		"immutable trail",
		"permanent/append-only",
		"owner-proof trigger",
	},
	"migrations/20260809232011_approval_runs.sql": {
		"permanent record",
		"retention is permanent",
		"no purge job",
	},
	"migrations/20260707193000_river_and_idempotency.sql": {
		"permanent dedupe ledger",
		"append-only/permanent",
	},
	"migrations/20260722085427_submission_jobs.sql": {
		"permanent record",
		"never deleted by the app",
	},
	// Restates the migration above's unqualified permanence wording in its own
	// words rather than as a quotation, so it is scoped here too.
	"internal/platform/db/submission_jobs_rls_test.go": {"permanent record"},
	"internal/audit/audit.go":                          {"immutable trail", "owner-proof trigger"},
}

// retentionExempt maps a matching path to why it needs no demo carve-out. Every
// reason is asserted non-vacuous below: an exemption whose path stopped matching
// is stale and fails.
var retentionExempt = map[string]string{
	"migrations/20260722093218_app_exchange.sql":              "its header already leaves retention deliberately unanswered and requires cleanup to stay possible; it is the wording precedent the corrections follow",
	"migrations/20260714111246_invoice_status_history.sql":    "append-only here is a grant claim about invoice_app, still true; it asserts no permanence",
	"migrations/20260731090000_rule_set_v3.sql":               "its owner-proof triggers guard the GLOBAL rule-set tables, which carry no tenant_id and which the purge never touches",
	"migrations/20260717120000_rule_immutability_lock.sql":    "installs those same global rule-set triggers, citing audit_log only as the precedent",
	"internal/platform/db/idempotency_keys_rls_test.go":       "restates invoice_app's grant posture, which the purge does not change — the purge runs as superuser",
	"internal/platform/db/app_exchange_rls_test.go":           "quotes the app_exchange precedent verbatim",
	"internal/platform/db/invoice_status_history_rls_test.go": "quotes the invoice_status_history precedent verbatim",
	"internal/invoice/kept_as_is_test.go":                     "an audit row as the permanent record of the FIRST keep-as-is decision against a mutable column — a claim about rewriting, which the purge does not do",
}

var retentionWalkRoots = []string{".", "docs", "migrations", "internal", "cmd", "db", "tools"}

var retentionWalkExts = []string{".md", ".sql", ".go"}

// demoExceptionPhrase is what a corrected block must SAY, over and above
// linking to the doc. Matched on the block with every mention of the doc's
// filename removed: exceptionDoc contains "demo", so a bare link satisfies a
// naive "contains demo" check on its own — an uncorrected sentence plus a link
// then passes clean, which is what AC-6 exists to catch.
const demoExceptionPhrase = "demo tenant"

// exceptionDocFile is exceptionDoc's base name, which markdown cross-references
// also use in their relative form.
var exceptionDocFile = filepath.Base(exceptionDoc)

// retentionFaults reports why block states a permanence claim without scoping it
// to the demo exception. Empty means the block is corrected.
func retentionFaults(block string) []string {
	var faults []string
	prose := strings.ReplaceAll(strings.ToLower(block), exceptionDocFile, "")
	if !strings.Contains(prose, demoExceptionPhrase) {
		faults = append(faults, "names no "+demoExceptionPhrase+" exception outside the link")
	}
	if !strings.Contains(block, exceptionDoc) {
		faults = append(faults, "points at no "+exceptionDoc)
	}
	return faults
}

func TestPurgeAuditRetentionClaimNamesTheDemoException(t *testing.T) {
	// First, so the scanner is proved able to see both outcomes even on the runs
	// where every real site is still uncorrected.
	t.Run("control needle", func(t *testing.T) {
		uncorrected := flowLines("--", []string{
			"-- audit_log is append-only and permanently",
			"-- retained: it holds no UPDATE/DELETE grant",
		})
		if len(matched(uncorrected, retentionNeedles)) == 0 {
			t.Fatal("no needle matches a fixture carrying an uncorrected retention claim — the scanner cannot see what it looks for")
		}
		if faults := retentionFaults(uncorrected); len(faults) == 0 {
			t.Fatal("the scanner reports an uncorrected fixture clean — a clean report against the repo would prove nothing")
		}

		corrected := flowLines("--", []string{
			"-- audit_log is append-only and permanently retained, except for the four demo",
			"-- tenants, whose rows a gated boot deletes (" + exceptionDoc + ").",
		})
		if len(matched(corrected, retentionNeedles)) == 0 {
			t.Fatal("no needle matches the CORRECTED fixture — the floor below cannot tell a scoped claim from a deleted one")
		}
		if faults := retentionFaults(corrected); len(faults) != 0 {
			t.Fatalf("the scanner reports %v against a fixture carrying both the demo exception and %s — it cannot recognise a correction", faults, exceptionDoc)
		}

		// The bypass: an uncorrected claim with a bare cross-reference bolted on.
		// exceptionDoc contains "demo", so any check that looks for "demo" across
		// the whole block reports this clean while the sentence stays false.
		linkOnly := flowLines("--", []string{
			"-- audit_log is append-only and permanently retained: it holds no",
			"-- UPDATE/DELETE grant for the application role ([" + exceptionDoc + "](" + exceptionDocFile + ")).",
		})
		if faults := retentionFaults(linkOnly); len(faults) == 0 {
			t.Fatalf("an uncorrected claim carrying only a link to %s reports clean — the demo-exception half of this scan is satisfied by the link's own path", exceptionDoc)
		}

		// Both fixtures wrap mid-needle. If flowLines stopped joining, the two
		// checks above would still pass on their other needles while every real
		// wrapped claim went unseen.
		wrapped := flowLines("--", []string{"-- ... a permanent", "-- record of the attempt"})
		if len(matched(wrapped, []string{"permanent record"})) == 0 {
			t.Fatal("a needle broken across two comment lines is invisible to the scanner — rewrapping a corrected sentence would silently delete the claim this scan depends on")
		}
	})

	if len(retentionWantSites) < 8 {
		t.Fatalf("retentionWantSites enumerates %d site(s), want at least 8 — trimming the map is how this scan goes quietly vacuous", len(retentionWantSites))
	}

	root := repoRootDir(t)

	t.Run("exception doc exists", func(t *testing.T) {
		// Deliberate: every corrected block points here, so a missing file makes
		// each correction a dangling cross-reference.
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(exceptionDoc))); err != nil {
			t.Errorf("%s does not exist, so every retention correction points at nothing: %v", exceptionDoc, err)
		}
	})

	for _, rel := range sortedKeys(retentionWantSites) {
		blocks, whole := claimText(t, root, rel)
		if len(blocks) == 0 {
			t.Fatalf("%s yields no claim blocks — blocksOf is broken for %s, and every check on this file would pass having read nothing", rel, filepath.Ext(rel))
		}

		for _, needle := range retentionWantSites[rel] {
			if len(matched(whole, []string{needle})) == 0 {
				t.Errorf("%s no longer carries %q. A correction SCOPES a claim, it does not delete it — the claim stays true off the demo tenants, and a deleted sentence leaves this scan proving nothing about the file", rel, needle)
			}
		}

		for _, b := range blocks {
			hits := matched(b.text, retentionNeedles)
			if len(hits) == 0 {
				continue
			}
			if faults := retentionFaults(b.text); len(faults) != 0 {
				t.Errorf("%s:%d states %v but %s", rel, b.line, hits, strings.Join(faults, " and "))
			}
		}
	}

	// Discovery walk: an unenumerated site is an unowned one.
	t.Run("discovery walk", func(t *testing.T) {
		files := walkRepo(t, root, retentionWalkRoots, retentionWalkExts)
		if len(files) < 100 {
			t.Fatalf("the walk found %d file(s) under %v — too few for a repo this size, so a clean report means nothing", len(files), retentionWalkRoots)
		}

		exemptHit := map[string]bool{}
		for _, rel := range files {
			_, whole := claimText(t, root, rel)
			hits := matched(whole, retentionNeedles)
			if len(hits) == 0 {
				continue
			}
			if _, ok := retentionExempt[rel]; ok {
				exemptHit[rel] = true
				continue
			}
			if _, ok := retentionWantSites[rel]; ok {
				continue
			}
			if rel == exceptionDoc {
				continue // the doc that explains the exception, not a claim
			}
			t.Errorf("%s states %v and is in neither retentionWantSites nor retentionExempt — an unenumerated retention claim is an unowned one", rel, hits)
		}

		for _, rel := range sortedKeys(retentionExempt) {
			if !exemptHit[rel] {
				t.Errorf("retentionExempt lists %s (%s) but no needle matches it any more — a stale exemption hides the next real site", rel, retentionExempt[rel])
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Scanner 2 — db.Reset as the every-boot rationale
// ---------------------------------------------------------------------------

// bootRationaleNeedles find blocks explaining why a demo seeder must converge on
// every boot. db.Reset is not that reason on the persistent environment:
// resettableEnvironment matches only pr-<N>, so Reset never runs there. The
// boot-time purge is.
var bootRationaleNeedles = []string{
	"db.Reset truncates",
	"runs Reset again",
	"already emptied",
}

// bootRationaleRequired is what every such block must carry once corrected.
var bootRationaleRequired = []string{"PurgeDemoTenants", exceptionDoc}

// resetIsPRScoped: a block that keeps naming db.Reset must say where Reset
// actually reaches, or it still reads as the persistent-environment rationale.
var resetIsPRScoped = []string{"pr-", "PR environment"}

// bootRationaleSite is the needles a site must still match, plus any fragment
// that site alone must carry.
type bootRationaleSite struct {
	needles []string
	extra   []string
}

var bootRationaleWantSites = map[string]bootRationaleSite{
	"cmd/invoice/main.go":                    {needles: []string{"db.Reset truncates"}},
	"internal/demopolicy/demopolicy.go":      {needles: []string{"db.Reset truncates", "runs Reset again"}},
	"internal/demopolicy/demopolicy_test.go": {needles: []string{"db.Reset truncates"}},
	".github/workflows/ci.yml":               {needles: []string{"db.Reset truncates"}},
	// The purge is non-fatal and spares four tables, so neither "already emptied"
	// nor "clean slate" holds unconditionally.
	"db/seed.dev.sql": {needles: []string{"already emptied"}, extra: []string{"non-fatal", "memberships"}},
}

var bootRationaleExempt = map[string]string{
	"internal/platform/db/reset_test.go":                "describes Reset's own truncation inside Reset's own suite; Reset is the subject there, not the rationale for a demo seeder",
	"internal/platform/db/retention_claim_gate_test.go": "this file; a needle is quoted in the prose beside bootRationaleWantSites",
}

var bootRationaleWalkRoots = []string{"docs", "migrations", "internal", "cmd", "db", ".github/workflows"}

var bootRationaleWalkExts = []string{".md", ".sql", ".go", ".yml"}

// bootRationaleFaults reports what a block citing db.Reset as the every-boot
// rationale is still missing.
func bootRationaleFaults(block string, extra []string) []string {
	var faults []string
	for _, want := range append(append([]string(nil), bootRationaleRequired...), extra...) {
		if !strings.Contains(block, want) {
			faults = append(faults, "carries no "+want)
		}
	}
	if len(matched(block, []string{"db.Reset truncates", "runs Reset again"})) != 0 &&
		len(matched(block, resetIsPRScoped)) == 0 {
		faults = append(faults, "names db.Reset without scoping it to the pr-<N> environments it actually reaches")
	}
	return faults
}

func TestPurgeIsTheBootRationaleNotReset(t *testing.T) {
	t.Run("control needle", func(t *testing.T) {
		uncorrected := flowLines("//", []string{
			"// It CONVERGES rather than inserting-if-absent. db.Reset truncates",
			"// approval_runs and deliberately leaves the three policy tables standing.",
		})
		if len(matched(uncorrected, bootRationaleNeedles)) == 0 {
			t.Fatal("no needle matches a fixture citing db.Reset as the every-boot rationale — the scanner cannot see what it looks for")
		}
		if faults := bootRationaleFaults(uncorrected, nil); len(faults) == 0 {
			t.Fatal("the scanner reports an uncorrected fixture clean — a clean report against the repo would prove nothing")
		}

		corrected := flowLines("//", []string{
			"// It CONVERGES rather than inserting-if-absent. db.PurgeDemoTenants empties the",
			"// demo tenants on every gated boot and leaves the three policy tables standing;",
			"// db.Reset truncates the same rows, but only in a pr-<N> fork (" + exceptionDoc + ").",
		})
		if len(matched(corrected, bootRationaleNeedles)) == 0 {
			t.Fatal("no needle matches the CORRECTED fixture — the floor below cannot tell a scoped rationale from a deleted one")
		}
		if faults := bootRationaleFaults(corrected, nil); len(faults) != 0 {
			t.Fatalf("the scanner reports %v against a corrected fixture — it cannot recognise a correction", faults)
		}
	})

	if len(bootRationaleWantSites) < 5 {
		t.Fatalf("bootRationaleWantSites enumerates %d site(s), want at least 5", len(bootRationaleWantSites))
	}

	root := repoRootDir(t)

	for _, rel := range sortedKeys(bootRationaleWantSites) {
		site := bootRationaleWantSites[rel]
		blocks, whole := claimText(t, root, rel)
		if len(blocks) == 0 {
			t.Fatalf("%s yields no claim blocks — blocksOf is broken for %s", rel, filepath.Ext(rel))
		}

		for _, needle := range site.needles {
			if len(matched(whole, []string{needle})) == 0 {
				t.Errorf("%s no longer carries %q. Reset's behaviour is unchanged and the sentence stays — what changes is the rationale beside it", rel, needle)
			}
		}

		for _, b := range blocks {
			hits := matched(b.text, bootRationaleNeedles)
			if len(hits) == 0 {
				continue
			}
			if faults := bootRationaleFaults(b.text, site.extra); len(faults) != 0 {
				t.Errorf("%s:%d cites %v as the every-boot rationale but %s", rel, b.line, hits, strings.Join(faults, ", "))
			}
		}
	}

	t.Run("discovery walk", func(t *testing.T) {
		files := walkRepo(t, root, bootRationaleWalkRoots, bootRationaleWalkExts)
		if len(files) < 100 {
			t.Fatalf("the walk found %d file(s) under %v — too few, so a clean report means nothing", len(files), bootRationaleWalkRoots)
		}

		exemptHit := map[string]bool{}
		for _, rel := range files {
			_, whole := claimText(t, root, rel)
			hits := matched(whole, bootRationaleNeedles)
			if len(hits) == 0 {
				continue
			}
			if _, ok := bootRationaleExempt[rel]; ok {
				exemptHit[rel] = true
				continue
			}
			if _, ok := bootRationaleWantSites[rel]; ok {
				continue
			}
			if rel == exceptionDoc {
				continue
			}
			t.Errorf("%s cites %v and is in neither bootRationaleWantSites nor bootRationaleExempt", rel, hits)
		}

		for _, rel := range sortedKeys(bootRationaleExempt) {
			if !exemptHit[rel] {
				t.Errorf("bootRationaleExempt lists %s (%s) but no needle matches it any more — a stale exemption hides the next real site", rel, bootRationaleExempt[rel])
			}
		}
	})
}
