package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	fileB   = "migrations/20260201000000_b.sql"
	endCopy = ". The gate stays red until this check runs."
	noneMsg = "No deliberate schema changes.\n"
)

// repo is a temp git repo with a base commit on main and a pr branch checked out.
type repo struct {
	t   *testing.T
	dir string
}

func (r *repo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *repo) write(path, body string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
}

// newRepo commits files on main, then branches pr.
func newRepo(t *testing.T, files map[string]string) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.git("config", "commit.gpgsign", "false")
	for p, b := range files {
		r.write(p, b)
	}
	r.commit("base")
	r.git("checkout", "-q", "-b", "pr")
	return r
}

func (r *repo) run(args ...string) (int, string) {
	r.t.Helper()
	var buf bytes.Buffer
	code := run(r.dir, args, &buf)
	return code, buf.String()
}

func (r *repo) runPR() (int, string) { return r.run("-base", "main", "-head", "pr") }

func mig(body string) string { return "-- +goose Up\n" + body + "\n-- +goose Down\nSELECT 1;\n" }

var baseFiles = map[string]string{"migrations/20260101000000_a.sql": mig("CREATE TABLE a (c int);")}

func TestRun_AddedUnmarkedDropFails(t *testing.T) {
	r := newRepo(t, baseFiles)
	r.write(fileB, mig("ALTER TABLE a DROP COLUMN c;"))
	r.commit("add")
	code, out := r.runPR()
	want := unmarkedLine(fileB, "ALTER TABLE a DROP COLUMN c", "a.c") + "\n"
	if code != 1 || out != want {
		t.Fatalf("code %d\n got: %q\nwant: %q", code, out, want)
	}
}

func TestRun_AddedMarkedDropPasses(t *testing.T) {
	r := newRepo(t, baseFiles)
	r.write(fileB, mig("-- contract-step a.c: no revision reads c since 2026-10-30\nALTER TABLE a DROP COLUMN c;"))
	r.commit("add")
	code, out := r.runPR()
	want := "Deliberate schema changes:\n" + fileB + ": a.c: no revision reads c since 2026-10-30\n"
	if code != 0 || out != want {
		t.Fatalf("code %d\n got: %q\nwant: %q", code, out, want)
	}
}

func TestRun_MarkWithoutReasonAndOrphanFail(t *testing.T) {
	r := newRepo(t, baseFiles)
	r.write(fileB, mig("-- contract-step a.c:\nALTER TABLE a DROP COLUMN c;"))
	r.write("migrations/20260301000000_c.sql", mig("-- contract-step z.z: why\nSELECT 1;"))
	r.commit("add")
	code, out := r.runPR()
	for _, want := range []string{
		fileB + ": the mark for a.c has no reason. Write why no running revision reads it.\n",
		"migrations/20260301000000_c.sql: the mark for z.z names no statement in this Up section. Remove it or correct the name.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
}

func TestRun_NoAddedMigrationIsSilentEvenWithAnOldUnmarkedDrop(t *testing.T) {
	r := newRepo(t, map[string]string{"migrations/20260101000000_a.sql": mig("DROP TABLE t;")})
	r.write("README", "hi")
	r.write("migrations/embed.go", "package migrations\n")
	r.commit("add")
	if code, out := r.runPR(); code != 0 || out != noneMsg {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestRun_ModifiedOldMigrationIsNotChecked(t *testing.T) {
	r := newRepo(t, baseFiles)
	r.write("migrations/20260101000000_a.sql", mig("CREATE TABLE a (c int);\nDROP TABLE t;"))
	r.commit("edit")
	if code, out := r.runPR(); code != 0 || out != noneMsg {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestRun_BaseEqualToHeadAddsNothing(t *testing.T) {
	r := newRepo(t, map[string]string{"migrations/20260101000000_a.sql": mig("DROP TABLE t;")})
	if code, out := r.run("-base", "HEAD", "-head", "HEAD"); code != 0 || out != noneMsg {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestRun_RenamedMigrationCountsAsAdded(t *testing.T) {
	r := newRepo(t, map[string]string{"migrations/20260101000000_a.sql": mig("DROP TABLE t;")})
	r.git("mv", "migrations/20260101000000_a.sql", "migrations/20260901000000_a.sql")
	r.commit("rename")
	code, out := r.runPR()
	if code != 1 || !strings.Contains(out, "migrations/20260901000000_a.sql: DROP TABLE t deletes") {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestRun_CannotRunCases(t *testing.T) {
	cases := []struct {
		name string
		args []string
		file string
		body string
		in   string
	}{
		{name: "empty base", args: []string{"-base", ""}, in: "no base commit"},
		{name: "zero base", args: []string{"-base", strings.Repeat("0", 40)}, in: "no base commit"},
		{name: "unknown base", args: []string{"-base", "refs/nope/x", "-head", "pr"}},
		{name: "unknown head", args: []string{"-base", "main", "-head", "refs/nope/x"}},
		{name: "no up line", args: []string{"-base", "main", "-head", "pr"}, file: fileB, body: "DROP TABLE t;\n", in: fileB},
		{name: "unterminated quote", args: []string{"-base", "main", "-head", "pr"}, file: fileB, body: mig("SELECT 'abc;"), in: fileB},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t, baseFiles)
			if c.file != "" {
				r.write(c.file, c.body)
				r.commit("add")
			}
			code, out := r.run(c.args...)
			if code != 2 || !strings.HasPrefix(out, "Could not check migrations: ") || !strings.HasSuffix(out, endCopy+"\n") {
				t.Fatalf("code %d, out %q", code, out)
			}
			if !strings.Contains(out, c.in) {
				t.Fatalf("out %q lacks %q", out, c.in)
			}
		})
	}
}

func TestRun_ReportsEveryFailureOfEveryAddedFile(t *testing.T) {
	r := newRepo(t, baseFiles)
	r.write(fileB, mig("DROP TABLE x;\nALTER TABLE a DROP COLUMN c;"))
	r.write("migrations/20260301000000_c.sql", mig("DROP TABLE y;"))
	r.commit("add")
	code, out := r.runPR()
	want := []string{
		unmarkedLine(fileB, "DROP TABLE x", "x"),
		unmarkedLine(fileB, "ALTER TABLE a DROP COLUMN c", "a.c"),
		unmarkedLine("migrations/20260301000000_c.sql", "DROP TABLE y", "y"),
	}
	if got := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); code != 1 || !slices.Equal(got, want) {
		t.Fatalf("code %d\n got: %q\nwant: %q", code, got, want)
	}
}

func shipped(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestScan_ParsesEveryShippedMigration(t *testing.T) {
	files := shipped(t)
	if len(files) < 100 {
		t.Fatalf("only %d migrations found", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(f, string(b)); err != nil {
			t.Errorf("%v", err)
		}
	}
}

// Expected objects read from the Up sections of these four files.
func TestScan_FindsTheRealDropsInTheShippedMigrations(t *testing.T) {
	want := map[string][]string{
		"20260707193000_river_and_idempotency.sql":     {"river_job_notify", "river_migration", "river_migration_old", "river_client_queue", "river_client"},
		"20261010110000_drop_rule_set_active_flag.sql": {"rule_set_versions.is_active"},
		"20261013041941_staff_by_house_membership.sql": {"public.staff_members"},
		"20261024123347_health_samples_job_kinds.sql":  {"public.staff_read_health_samples"},
	}
	for name, objs := range want {
		b, err := os.ReadFile(filepath.Join("../../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		r, err := Scan(name, string(b))
		if err != nil {
			t.Fatal(err)
		}
		if got := objects(r); !slices.Equal(got, objs) {
			t.Errorf("%s\n got: %q\nwant: %q", name, got, objs)
		}
	}
}

func TestRun_NonASCIIMigrationPathIsChecked(t *testing.T) {
	for _, name := range []string{"café", "a b", "a\nb"} {
		file := "migrations/20260201000000_" + name + ".sql"
		r := newRepo(t, baseFiles)
		r.write(file, mig("DROP TABLE t;"))
		r.commit("add")
		if code, out := r.runPR(); code != 1 || !strings.Contains(out, file+": DROP TABLE t deletes") {
			t.Errorf("%q: code %d, out %q", name, code, out)
		}
	}
}
