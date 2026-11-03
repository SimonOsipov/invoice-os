package main

import (
	"strings"
	"testing"
)

const (
	fileA = "20260101_a.sql"
	fileB = "20260102_b.sql"
)

func pathOf(name string) string { return Dir + "/" + name }

type prRepo struct {
	t     *testing.T
	run   func(args ...string) string
	write func(name, body string)
}

func newPRRepo(t *testing.T) prRepo {
	run, write, _ := renameRepo(t)
	return prRepo{t, run, write}
}

func (r prRepo) commit(msg string) { r.run("add", "-A"); r.run("commit", "-qm", msg) }

func (r prRepo) rm(name string) { r.run("rm", "-q", pathOf(name)) }

// seedBase commits on pr, then points epic at it, so the commit is on base.
func (r prRepo) seedBase() { r.commit("seed"); r.run("branch", "-f", "epic", "pr") }

func (r prRepo) exec(head string) (int, string) {
	return runMain(r.t, r.run("rev-parse", "--show-toplevel"), "epic", head, "main")
}

func (r prRepo) want(code int, head string, has ...string) string {
	r.t.Helper()
	got, out := r.exec(head)
	if got != code {
		r.t.Fatalf("want exit %d, got %d\n%s", code, got, out)
	}
	for _, h := range has {
		if !strings.Contains(out, h) {
			r.t.Fatalf("output lacks %q:\n%s", h, out)
		}
	}
	return out
}

func TestMain_DeletedMigrationWithoutMarkFails(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.commit("del")
	r.want(1, "pr", pathOf(fileA)+" was deleted.", "add to migrations/deleted.txt: "+fileA+": <why the deletion is safe>")
}

func TestMain_DeletePlusSimilarAddIsADeletion(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.write("20260401_new.sql", sqlBody+"-- a\n-- n\n")
	r.commit("del+add")
	st := r.run("diff", "-M", "--name-status", "epic", "pr")
	if !strings.Contains(st, "R0") && !strings.Contains(st, "R9") || strings.Contains(st, "D\t") {
		t.Fatalf("setup: want an inexact R and no D:\n%s", st)
	}
	r.want(1, "pr", pathOf(fileA)+" was deleted.")
}

func TestMain_ExactRenameOfAMainFileIsADeletion(t *testing.T) {
	r := newPRRepo(t)
	renameOnPR(r.run, fileA, "20260401_a.sql")
	r.want(1, "pr", pathOf(fileA)+" was deleted.")
	r.write("deleted.txt", fileA+": renumbered\n")
	r.commit("mark")
	r.want(0, "pr", "Deliberate migration deletions:", pathOf(fileA)+": renumbered")
}

func TestMain_DeletedMigrationWithMarkPasses(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.write("deleted.txt", fileA+": never applied in production\n")
	r.commit("del+mark")
	r.want(0, "pr", "Deliberate migration deletions:", pathOf(fileA)+": never applied in production")
}

func TestMain_MarkInALaterCommitOfTheBranchPasses(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.commit("del")
	r.write("deleted.txt", fileA+": never applied\n")
	r.commit("mark")
	r.want(0, "pr")
}

func TestMain_MarkWithoutReasonFails(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.write("deleted.txt", fileA+":\n")
	r.commit("del+mark")
	r.want(1, "pr", "the mark for "+fileA+" has no reason")
}

func TestMain_MarkNamingAFileThatIsNotDeletedFails(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.write("deleted.txt", fileB+": r\n")
	r.commit("del+mark")
	r.want(1, "pr", orphanOf(fileB), unmarked(pathOf(fileA), fileA))
}

func TestMain_MarkWithNoDeletionFails(t *testing.T) {
	r := newPRRepo(t)
	r.write("deleted.txt", fileA+": r\n")
	r.commit("mark")
	r.want(1, "pr", orphanOf(fileA))
}

func TestMain_OneMarkReleasesOneDeletion(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.rm(fileB)
	r.write("deleted.txt", fileA+": r\n")
	r.commit("del+mark")
	out := r.want(1, "pr", unmarked(pathOf(fileB), fileB))
	if strings.Contains(out, pathOf(fileA)+" was deleted.") {
		t.Fatalf("a must be released:\n%s", out)
	}
}

func TestMain_OldMarkLineOnBaseIsNeitherOrphanNorRelease(t *testing.T) {
	r := newPRRepo(t)
	r.write("deleted.txt", fileB+": old\n")
	r.seedBase()
	r.rm(fileA)
	r.write("deleted.txt", fileB+": old\n"+fileA+": new\n")
	r.commit("del+mark")
	out := r.want(0, "pr", pathOf(fileA)+": new")
	if strings.Contains(out, ": old") {
		t.Fatalf("old line must not be listed:\n%s", out)
	}

	r = newPRRepo(t)
	r.write("deleted.txt", fileB+": old\n")
	r.seedBase()
	r.rm(fileB)
	r.commit("del")
	r.want(1, "pr", pathOf(fileB)+" was deleted.")
}

func TestMain_EditedOldMarkLineFailsAsOrphan(t *testing.T) {
	r := newPRRepo(t)
	r.write("deleted.txt", fileB+": old\n")
	r.seedBase()
	r.write("deleted.txt", fileB+": older\n")
	r.commit("edit")
	r.want(1, "pr", orphanOf(fileB))
}

func TestMain_ExactRenameOfABaseOnlyFileIsNotADeletion(t *testing.T) {
	r := newPRRepo(t)
	renameOnPR(r.run, "20260105_x.sql", "20260310_x.sql")
	out := r.want(0, "pr")
	if strings.Contains(out, "was deleted") {
		t.Fatalf("unexpected deletion copy:\n%s", out)
	}
}

func TestMain_DeletingEmbedGoIsNotADeletion(t *testing.T) {
	r := newPRRepo(t)
	r.write("embed.go", "package migrations\n")
	r.seedBase()
	r.run("rm", "-q", Dir+"/embed.go")
	r.commit("del embed")
	r.want(0, "pr")
}

func TestMain_FileBaseAlreadyDeletedIsSkipped(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.commit("pr del")
	r.run("checkout", "-q", "epic")
	r.rm(fileA)
	r.commit("epic del")
	r.run("checkout", "-q", "pr")
	out := r.want(0, "pr")
	if strings.Contains(out, "was deleted") {
		t.Fatalf("unexpected deletion copy:\n%s", out)
	}
}

func TestMain_NoDeletionOutputIsUnchanged(t *testing.T) {
	r := newPRRepo(t)
	r.write("20260401_c.sql", sqlBody+"-- c\n")
	r.commit("add")
	out := r.want(0, "pr")
	if want := "migrationorder: every file added under migrations/ sorts after base's newest migration\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestMain_OrderViolationAndUnmarkedDeletionBothReported(t *testing.T) {
	r := newPRRepo(t)
	r.write("20260110_early.sql", sqlBody+"-- early\n")
	r.rm(fileB)
	r.commit("early+del")
	r.want(1, "pr", "sorts at or before", unmarked(pathOf(fileB), fileB))
}

func TestMain_PushToMainShapeFailsAnUnmarkedDeletion(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.commit("del")
	r.run("checkout", "-q", "main")
	r.run("merge", "-q", "--no-ff", "-m", "merge", "pr")
	r.run("checkout", "-q", "pr")
	r.want(1, "main", unmarked(pathOf(fileA), fileA))
}

func TestMain_DeletingTheMarksFileIsNotADeletionAndReleasesNothing(t *testing.T) {
	r := newPRRepo(t)
	r.write("deleted.txt", fileB+": old\n")
	r.seedBase()
	r.run("rm", "-q", Dir+"/deleted.txt")
	r.rm(fileA)
	r.commit("del marks file + a")
	r.want(1, "pr", unmarked(pathOf(fileA), fileA))

	r = newPRRepo(t)
	r.write("deleted.txt", fileB+": old\n")
	r.seedBase()
	r.run("rm", "-q", Dir+"/deleted.txt")
	r.commit("del marks file")
	r.want(0, "pr")
}

func TestMain_MarkAddedAsANewFileWithCRLFReleasesTheDeletion(t *testing.T) {
	r := newPRRepo(t)
	r.rm(fileA)
	r.write("deleted.txt", "\r\n"+fileA+": crlf reason\r\n")
	r.commit("del+mark")
	r.want(0, "pr", pathOf(fileA)+": crlf reason\n")
}
