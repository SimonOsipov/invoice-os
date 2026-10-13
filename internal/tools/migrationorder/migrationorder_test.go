package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mainAtPR143 is the tail of migrations/ on main after PR #143 merged, the base PR #142 landed on.
var mainAtPR143 = []string{
	"migrations/20260804210704_demo_portfolio_repair.sql",
	"migrations/20260805075045_invoices_failure_kind.sql",
	"migrations/20260806184800_invoices_kept_as_is_failed.sql",
	"migrations/embed.go",
}

func mustCheck(t *testing.T, added, onBase []string) []Violation {
	t.Helper()
	got, err := Check(added, onBase)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return got
}

func TestCheck_PassesAMigrationNewerThanBase(t *testing.T) {
	got := mustCheck(t, []string{"migrations/20260807000000_later.sql"}, mainAtPR143)
	if len(got) != 0 {
		t.Fatalf("want no violation, got %+v", got)
	}
}

// The real break: PR #142 merged 20260806131239 after main already held 20260806184800.
func TestCheck_FailsPR142RuleSetV4(t *testing.T) {
	got := mustCheck(t, []string{"migrations/20260806131239_rule_set_v4.sql"}, mainAtPR143)
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %+v", got)
	}
	want := Violation{
		File:       "migrations/20260806131239_rule_set_v4.sql",
		Version:    20260806131239,
		NewestFile: "migrations/20260806184800_invoices_kept_as_is_failed.sql",
		Newest:     20260806184800,
	}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
}

// goose rejects two migrations with one version, so equal fails too.
func TestCheck_FailsAVersionEqualToBaseNewest(t *testing.T) {
	got := mustCheck(t, []string{"migrations/20260806184800_same_second.sql"}, mainAtPR143)
	if len(got) != 1 || got[0].Version != 20260806184800 {
		t.Fatalf("want the equal version flagged, got %+v", got)
	}
}

func TestCheck_MultipleAddedFlagsOnlyTheStaleOnes(t *testing.T) {
	added := []string{
		"migrations/20260901000000_after.sql",
		"migrations/20260801000000_before.sql",
		"migrations/20260902000000_after_too.sql",
		"migrations/20260806184800_equal.sql",
	}
	got := mustCheck(t, added, mainAtPR143)
	if len(got) != 2 {
		t.Fatalf("want 2 violations, got %+v", got)
	}
	if got[0].File != "migrations/20260801000000_before.sql" || got[1].File != "migrations/20260806184800_equal.sql" {
		t.Fatalf("wrong violations, got %+v", got)
	}
}

// Comparison is numeric, like goose: a string compare would rank 9_ after 10_.
func TestCheck_ComparesVersionsNumerically(t *testing.T) {
	got := mustCheck(t, []string{"migrations/9_old.sql"}, []string{"migrations/10_new.sql"})
	if len(got) != 1 {
		t.Fatalf("want 9 flagged against 10, got %+v", got)
	}
}

func TestCheck_EmptyBasePasses(t *testing.T) {
	if got := mustCheck(t, []string{"migrations/20260706153625_init.sql"}, nil); len(got) != 0 {
		t.Fatalf("want no violation, got %+v", got)
	}
}

func TestCheck_IgnoresNonSQLFiles(t *testing.T) {
	if got := mustCheck(t, []string{"migrations/embed.go"}, mainAtPR143); len(got) != 0 {
		t.Fatalf("want embed.go ignored, got %+v", got)
	}
}

func TestCheck_RejectsAnUnversionedMigration(t *testing.T) {
	if _, err := Check([]string{"migrations/rule_set_v5.sql"}, mainAtPR143); err == nil {
		t.Fatal("want an error for a migration with no numeric version")
	}
}

// Every real migration must parse, or Check would error on it; the floor catches a moved dir.
func TestVersion_ParsesEveryRealMigration(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", Dir))
	if err != nil {
		t.Fatalf("read %s: %v", Dir, err)
	}
	parsed := 0
	for _, e := range entries {
		_, ok, err := Version(e.Name())
		if err != nil {
			t.Errorf("real migration does not parse: %v", err)
		}
		if ok {
			parsed++
		}
	}
	if parsed < 50 {
		t.Fatalf("parsed %d migrations in %s, want at least 50", parsed, Dir)
	}
}

// A migration main already holds is skipped even when it sorts before base's newest;
// a branch-new one at the same position still violates.
func TestSkipOnMain_SkipsMainFilesButKeepsBranchNewOnes(t *testing.T) {
	onMain := []string{"migrations/20260806131239_rule_set_v4.sql", "migrations/embed.go"}
	added := []string{
		"migrations/20260806131239_rule_set_v4.sql",
		"migrations/20260806140000_branch_new.sql",
	}
	got := mustCheck(t, SkipOnMain(added, onMain), mainAtPR143)
	if len(got) != 1 || got[0].File != "migrations/20260806140000_branch_new.sql" {
		t.Fatalf("want only the branch-new file to violate, got %+v", got)
	}
}

func TestSkipOnMain_EmptyMainSkipsNothing(t *testing.T) {
	added := []string{"migrations/20260806131239_rule_set_v4.sql"}
	if got := SkipOnMain(added, nil); len(got) != 1 {
		t.Fatalf("want nothing skipped, got %v", got)
	}
}

func TestMain_UnreadableMainRefExitsTwo(t *testing.T) {
	if os.Getenv("MIGRATIONORDER_RUN_MAIN") == "1" {
		os.Args = []string{"migrationorder", "-base", "HEAD", "-main", "refs/nope/missing"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_UnreadableMainRefExitsTwo$")
	cmd.Env = append(os.Environ(), "MIGRATIONORDER_RUN_MAIN=1")
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("want exit 2, got %v", err)
	}
}

// Post-merge push to main: -main must be main before the push, or every added
// migration reads as already on main and the reverse-order break goes silent.
func TestMain_ReverseOrderMergeFailsWhenMainIsTheBeforeSha(t *testing.T) {
	if os.Getenv("MIGRATIONORDER_RUN_MAIN") == "2" {
		os.Args = []string{"migrationorder", "-base", os.Getenv("MO_BEFORE"), "-head", os.Getenv("MO_HEAD")}
		if m := os.Getenv("MO_MAIN"); m != "" {
			os.Args = append(os.Args, "-main", m)
		}
		main()
		return
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	add := func(name string) {
		if err := os.MkdirAll(filepath.Join(repo, Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, Dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-qm", name)
	}
	run("init", "-q", "-b", "main")
	add("20260101000000_a.sql")
	run("checkout", "-qb", "pr1")
	add("20260301000000_late.sql")
	run("checkout", "-q", "main")
	run("checkout", "-qb", "pr2")
	add("20260201000000_early.sql")
	run("checkout", "-q", "main")
	run("merge", "-q", "--no-ff", "pr1", "-m", "m1")
	before := run("rev-parse", "HEAD")
	run("merge", "-q", "--no-ff", "pr2", "-m", "m2")

	run("checkout", "-qb", "pr3", "pr2~1")
	run("commit", "-q", "--allow-empty", "-m", "pr3")
	run("merge", "-q", "--no-ff", "pr2", "-m", "pr3 merges pr2")
	pr3 := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")

	exit := func(mainRef, head string) int {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMain_ReverseOrderMergeFailsWhenMainIsTheBeforeSha$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MIGRATIONORDER_RUN_MAIN=2", "MO_BEFORE="+before, "MO_HEAD="+head, "MO_MAIN="+mainRef)
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	}
	if got := exit(before, "HEAD"); got != 1 {
		t.Fatalf("-main=before sha: want exit 1 (violation), got %d", got)
	}
	// Positive pair: a head outside main whose file main already holds is skipped.
	if got := exit("main", pr3); got != 0 {
		t.Fatalf("head outside main, file on main: want exit 0 (skipped), got %d", got)
	}
	// Push to main with the default -main: main == head after the merge.
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	if got := exit("", "HEAD"); got != 1 {
		t.Fatalf("push to main, -main=origin/main==head: want exit 1, got %d", got)
	}
}

// A -main that ls-tree reads but is-ancestor rejects (a tree, git exit 128) must exit 2, not skip nothing.
func TestMain_IsAncestorFailureExitsTwo(t *testing.T) {
	if os.Getenv("MIGRATIONORDER_RUN_MAIN") == "3" {
		os.Args = []string{"migrationorder", "-base", "HEAD", "-main", "HEAD^{tree}"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_IsAncestorFailureExitsTwo$")
	cmd.Env = append(os.Environ(), "MIGRATIONORDER_RUN_MAIN=3")
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("want exit 2, got %v", err)
	}
}

func TestSplit_GroupsEachChangeKind(t *testing.T) {
	onBase := []string{"migrations/20260101_a.sql", "migrations/20260102_b.sql", "migrations/20260105_x.sql"}
	onMain := []string{"migrations/20260101_a.sql", "migrations/20260102_b.sql"}
	changes := []Change{
		{New: "migrations/20260110_new.sql"},
		{Old: "migrations/20260105_x.sql", New: "migrations/20260110_x.sql", Exact: true},
		{Old: "migrations/20260102_b.sql", New: "migrations/20260110_b.sql", Exact: true},
		{Old: "migrations/20260105_x.sql", New: "migrations/20260111_x.sql"},
		{Old: "migrations/20260001_gone.sql", New: "migrations/20260112_g.sql", Exact: true},
		{New: "migrations/20260101_a.sql"},
	}
	vsBase, vsMain := Split(changes, onBase, onMain, true)
	wantBase := []string{
		"migrations/20260110_new.sql", "migrations/20260110_b.sql",
		"migrations/20260111_x.sql", "migrations/20260112_g.sql",
	}
	if strings.Join(vsBase, ",") != strings.Join(wantBase, ",") {
		t.Fatalf("vsBase = %v, want %v", vsBase, wantBase)
	}
	if len(vsMain) != 1 || vsMain[0] != "migrations/20260110_x.sql" {
		t.Fatalf("vsMain = %v, want only the exact rename of x", vsMain)
	}
	vsBase, vsMain = Split(changes, onBase, onMain, false)
	if len(vsMain) != 0 || len(vsBase) != 5 {
		t.Fatalf("exempt=false: vsBase=%v vsMain=%v, want no exemption", vsBase, vsMain)
	}
}

const sqlBody = "-- +goose Up\nSELECT 1;\nSELECT 2;\nSELECT 3;\nSELECT 4;\n-- +goose Down\nSELECT 5;\n"

// renameRepo builds main {a, b}, epic {a, b, x, late} and a pr branch off epic.
// The returned func runs main() in a child process and returns its exit code.
func renameRepo(t *testing.T) (run func(args ...string) string, write func(name, body string), exit func(head string) int) {
	t.Helper()
	if os.Getenv("MIGRATIONORDER_RUN_MAIN") == "4" {
		os.Args = []string{"migrationorder", "-base", os.Getenv("MO_BASE"), "-head", os.Getenv("MO_HEAD"), "-main", os.Getenv("MO_MAIN")}
		main()
		os.Exit(0)
	}
	repo := t.TempDir()
	run = func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write = func(name, body string) {
		if err := os.MkdirAll(filepath.Join(repo, Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, Dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) {
		run("add", "-A")
		run("commit", "-qm", msg)
	}
	run("init", "-q", "-b", "main")
	write("20260101_a.sql", sqlBody+"-- a\n")
	write("20260102_b.sql", sqlBody+"-- b\n")
	commit("main")
	run("checkout", "-qb", "epic")
	write("20260105_x.sql", sqlBody+"-- x\n")
	write("20260301_late.sql", sqlBody+"-- late\n")
	commit("epic")
	run("checkout", "-qb", "pr")
	exit = func(head string) int {
		cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MIGRATIONORDER_RUN_MAIN=4", "MO_BASE=epic", "MO_HEAD="+head, "MO_MAIN=main")
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	}
	return run, write, exit
}

func renameOnPR(run func(args ...string) string, from, to string) {
	run("mv", Dir+"/"+from, Dir+"/"+to)
	run("commit", "-qam", "rename")
}

func TestMain_RenameOfAnUnmergedMigrationPassesAboveMainNewest(t *testing.T) {
	run, _, exit := renameRepo(t)
	renameOnPR(run, "20260105_x.sql", "20260110_x.sql")
	if got := exit("pr"); got != 0 {
		t.Fatalf("want exit 0, got %d", got)
	}
}

func TestMain_RenameToAtOrBelowMainNewestFails(t *testing.T) {
	for _, to := range []string{"20260102_x.sql", "20251231_x.sql"} {
		run, _, exit := renameRepo(t)
		renameOnPR(run, "20260105_x.sql", to)
		if got := exit("pr"); got != 1 {
			t.Fatalf("rename to %s: want exit 1, got %d", to, got)
		}
	}
}

func TestMain_NewFileInTheRenameGapStillFails(t *testing.T) {
	run, write, exit := renameRepo(t)
	write("20260110_new.sql", sqlBody+"-- new\n")
	run("add", "-A")
	run("commit", "-qm", "new")
	if got := exit("pr"); got != 1 {
		t.Fatalf("want exit 1, got %d", got)
	}
}

func TestMain_RenameOfAMainMigrationStaysChecked(t *testing.T) {
	run, _, exit := renameRepo(t)
	renameOnPR(run, "20260102_b.sql", "20260110_b.sql")
	if got := exit("pr"); got != 1 {
		t.Fatalf("want exit 1, got %d", got)
	}
}

// git still reports R (similarity under 100) here, so the exemption must key on R100.
func TestMain_SimilarButChangedRenameStaysChecked(t *testing.T) {
	run, write, exit := renameRepo(t)
	write("20260110_x.sql", strings.Replace(sqlBody, "SELECT 3;", "SELECT 33;", 1)+"-- x\n")
	run("rm", "-q", Dir+"/20260105_x.sql")
	run("add", "-A")
	run("commit", "-qm", "rename and edit")
	if st := run("diff", "-M", "--name-status", "epic", "pr"); !strings.Contains(st, "R0") && !strings.Contains(st, "R9") {
		t.Fatalf("setup: want a partial-similarity rename, got %q", st)
	}
	if got := exit("pr"); got != 1 {
		t.Fatalf("want exit 1, got %d", got)
	}
}

// Head in main (push to main): the rename of a base-only file is not exempt.
func TestMain_RenameWhenHeadIsInMainIsNotExempt(t *testing.T) {
	run, _, exit := renameRepo(t)
	renameOnPR(run, "20260105_x.sql", "20260110_x.sql")
	run("checkout", "-q", "main")
	run("merge", "-q", "--no-ff", "pr", "-m", "merge pr")
	if got := exit("main"); got != 1 {
		t.Fatalf("want exit 1, got %d", got)
	}
}
