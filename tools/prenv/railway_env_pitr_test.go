// railway_env_pitr_test.go drives reconcile-fork's blank of WAL_ARCHIVE_* on a fork's Postgres.
package main

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var pitrRedactedLine = regexp.MustCompile(`^  Postgres\.WAL_ARCHIVE_[A-Z0-9_]* = <redacted>$`)

const (
	pitrBucket   = "pitr-bucket-planted-7c1e"
	pitrKey      = "pitr-key-planted-55aa"
	pitrSecret   = "pitr-secret-planted-90ff"
	pitrEndpoint = "https://pitr-endpoint-planted.example"
)

// pitrArchive is every archive variable PITR sets; each value is a unique needle.
var pitrArchive = map[string]string{
	"WAL_ARCHIVE_BUCKET":       pitrBucket,
	"WAL_ARCHIVE_KEY":          pitrKey,
	"WAL_ARCHIVE_SECRET":       pitrSecret,
	"WAL_ARCHIVE_REGION":       "pitr-region-planted-3d2b",
	"WAL_ARCHIVE_ENDPOINT":     pitrEndpoint,
	"WAL_ARCHIVE_PATH":         "pitr-path-planted-b81c",
	"WAL_ARCHIVE_S3_URI_STYLE": "pitr-style-planted-e4d7",
}

// pitrVars is the planted Postgres map: the archive names plus extra.
func pitrVars(extra map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range pitrArchive {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// pitrFixture is a fork whose Postgres holds vars and has the given deployment status.
func pitrFixture(t *testing.T, status string, vars map[string]string) authShim {
	t.Helper()
	s := reconcileForkFixture(t)
	raw, err := json.Marshal(vars)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.dir, "store-"+retryPostgresID+".json"), string(raw))
	writeFile(t, filepath.Join(s.dir, "svcInstance.json"), postgresInstance(status))
	writeFile(t, filepath.Join(s.dir, "dep.json"), `{"data":{"deployment":{"id":"dep-pg-new","status":"SUCCESS"}}}`)
	return s
}

// pitrWrites are the variableCollectionUpsert calls that target Postgres.
func pitrWrites(t *testing.T, s authShim) []railwayCall {
	t.Helper()
	var out []railwayCall
	for _, c := range s.calls(t) {
		in, _ := c.Variables["input"].(map[string]any)
		if strings.Contains(c.Query, "variableCollectionUpsert(") && in["serviceId"] == retryPostgresID {
			out = append(out, c)
		}
	}
	return out
}

func pitrWriteIndex(t *testing.T, s authShim) int {
	t.Helper()
	for i, c := range s.calls(t) {
		in, _ := c.Variables["input"].(map[string]any)
		if strings.Contains(c.Query, "variableCollectionUpsert(") && in["serviceId"] == retryPostgresID {
			return i
		}
	}
	return -1
}

// pitrPostgresReads counts reads of the Postgres variable map.
func pitrPostgresReads(t *testing.T, s authShim) int {
	t.Helper()
	n := 0
	for _, c := range s.calls(t) {
		if strings.Contains(c.Query, "variables(projectId") && c.Variables["s"] == retryPostgresID {
			n++
		}
	}
	return n
}

func requireNoBoot(t *testing.T, s authShim) {
	t.Helper()
	for _, op := range []string{"volCreate", "svcDeploy", "svcRedeploy"} {
		if n := opCount(t, s, op); n != 0 {
			t.Errorf("%s calls = %d, want 0", op, n)
		}
	}
}

func TestReconcileFork_BlanksWALArchiveBeforePostgresBoots(t *testing.T) {
	cases := []struct {
		name      string
		volumeSeq bool
		boot      string
	}{
		{"volume exists", false, "svcDeploy"},
		{"volume is created", true, "volCreate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(map[string]string{"PGPASSWORD": "pitr-pg-password"}))
			if c.volumeSeq {
				writeFile(t, filepath.Join(s.dir, "vols-"+retryForkEnv+".seq"), volumesOf()+"\n"+volumesOf(retryPostgresID)+"\n")
			}
			stdout, stderr, code := runReconcileFork(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
			}

			writes := pitrWrites(t, s)
			if len(writes) != 1 {
				t.Fatalf("Postgres variableCollectionUpsert calls = %d, want exactly 1 for all seven names; ops = %v", len(writes), operations(s.calls(t)))
			}
			in, _ := writes[0].Variables["input"].(map[string]any)
			if in["skipDeploys"] != true {
				t.Errorf("input.skipDeploys = %v, want true", in["skipDeploys"])
			}
			wantNames := []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_ENDPOINT", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_PATH", "WAL_ARCHIVE_REGION", "WAL_ARCHIVE_S3_URI_STYLE", "WAL_ARCHIVE_SECRET"}
			var gotNames []string
			for _, u := range s.upserts(t) {
				if u.Service != retryPostgresID {
					continue
				}
				gotNames = append(gotNames, u.Name)
				if u.Value != "" {
					t.Errorf("Postgres.%s written as a non-empty value, want \"\"", u.Name)
				}
			}
			if !slices.Equal(gotNames, wantNames) {
				t.Errorf("Postgres names written = %v, want %v", gotNames, wantNames)
			}
			held := readStore(t, s, retryPostgresID)
			for _, n := range wantNames {
				if v, ok := held[n]; !ok || v != "" {
					t.Errorf("re-read Postgres.%s = %v (present %v), want \"\"", n, v, ok)
				}
			}

			ops := operations(s.calls(t))
			w, dom, boot := pitrWriteIndex(t, s), slices.Index(ops, "dom"), slices.Index(ops, c.boot)
			if dom < 0 || boot < 0 || opCount(t, s, c.boot) != 1 {
				t.Fatalf("control: dom at %d, %s at %d (count %d), want a domain read and exactly one %s; ops = %v", dom, c.boot, boot, opCount(t, s, c.boot), c.boot, ops)
			}
			if w < 0 || w > dom || w > boot {
				t.Errorf("Postgres write at call %d, first dom at %d, %s at %d: want the write before both", w, dom, c.boot, boot)
			}
			if first := slices.Index(ops, "svcDeploy"); w < 0 || w > first {
				t.Errorf("Postgres write at call %d, svcDeploy at %d: want the write first", w, first)
			}
		})
	}
}

func TestReconcileFork_LeavesOtherPostgresVariables(t *testing.T) {
	other := map[string]string{"PGPASSWORD": "pitr-pg-password", "DATABASE_URL": "postgresql://pitr-planted", "MY_WAL_ARCHIVE_NOTE": "pitr-note-planted"}
	vars := map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket}
	for k, v := range other {
		vars[k] = v
	}
	s := pitrFixture(t, "NONE", vars)
	stdout, stderr, code := runReconcileFork(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
	}

	var got []string
	for _, u := range s.upserts(t) {
		if u.Service == retryPostgresID {
			got = append(got, u.Name)
		}
	}
	if !slices.Equal(got, []string{"WAL_ARCHIVE_BUCKET"}) {
		t.Errorf("Postgres names written = %v, want only [WAL_ARCHIVE_BUCKET]", got)
	}
	held := readStore(t, s, retryPostgresID)
	for k, v := range other {
		if held[k] != v {
			t.Errorf("Postgres.%s = %v after the run, want its planted value", k, held[k])
		}
	}
}

func TestReconcileFork_BlanksOnlyTheNonEmptyArchiveNames(t *testing.T) {
	s := pitrFixture(t, "NONE", map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket, "WAL_ARCHIVE_KEY": "", "WAL_ARCHIVE_SECRET": pitrSecret})
	stdout, stderr, code := runReconcileFork(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	var got []string
	for _, u := range s.upserts(t) {
		if u.Service == retryPostgresID {
			got = append(got, u.Name)
		}
	}
	if !slices.Equal(got, []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_SECRET"}) {
		t.Errorf("Postgres names written = %v, want [WAL_ARCHIVE_BUCKET WAL_ARCHIVE_SECRET]: a name already \"\" is not rewritten", got)
	}
}

func TestReconcileFork_NoArchiveValueNoWrite(t *testing.T) {
	blank := map[string]string{}
	for k := range pitrArchive {
		blank[k] = ""
	}
	cases := []struct {
		name string
		vars map[string]string
	}{
		{"no WAL_ARCHIVE_ name", map[string]string{"PGPASSWORD": "pitr-pg-password", "MY_WAL_ARCHIVE_NOTE": "pitr-note-planted"}},
		{"all seven blank", blank},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "SUCCESS", c.vars)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 0 {
				t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
			}
			if n := pitrPostgresReads(t, s); n < 1 {
				t.Errorf("control: Postgres variable map reads = %d, want at least 1, so the empty write list is a verdict and not a skipped step", n)
			}
			if w := pitrWrites(t, s); len(w) != 0 {
				t.Errorf("Postgres variableCollectionUpsert calls = %d, want 0", len(w))
			}
			if want := "Postgres in " + retryForkEnv + ": no WAL_ARCHIVE_* value to blank."; !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q; stdout = %q", want, stdout)
			}
			if !strings.Contains(stdout, "Fork reconciliation complete") {
				t.Errorf("stdout lacks the completion line; stdout = %q", stdout)
			}
		})
	}
}

func TestReconcileFork_RefusesPostgresThatBootedWithArchiving(t *testing.T) {
	for _, status := range []string{"SUCCESS", "DEPLOYING", "CRASHED"} {
		t.Run(status, func(t *testing.T) {
			s := pitrFixture(t, status, pitrVars(nil))
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			errs := errorLines(stdout + stderr)
			for _, want := range []string{retryForkEnv, status, "delete"} {
				if !strings.Contains(errs, want) {
					t.Errorf("::error:: lines do not contain %q: %q", want, errs)
				}
			}
			if ups := s.upserts(t); len(ups) != 0 {
				t.Errorf("the refusal wrote %v", names(ups))
			}
			requireNoBoot(t, s)
		})
	}
}

func TestReconcileFork_UnreadablePostgresVariablesFail(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, authShim)
		want  string
	}{
		{"null map", func(t *testing.T, s authShim) { s.bendRead(t, retryPostgresID, "null") }, "NOT evidence"},
		{"GraphQL error", func(t *testing.T, s authShim) { setFaults(t, s, "authVars", "gqlerr") }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			c.setup(t, s)
			stdout, stderr, code := runReconcileFork(t, s)

			if code == 0 {
				t.Errorf("exit 0, want non-zero; output = %q", stdout+stderr)
			}
			if n := opCount(t, s, "authVars"); n < 1 {
				t.Errorf("control: authVars calls = %d, want the Postgres map read", n)
			}
			if c.want != "" && !strings.Contains(errorLines(stdout+stderr), c.want) {
				t.Errorf("::error:: lines lack %q: %q", c.want, errorLines(stdout+stderr))
			}
			if ups := s.upserts(t); len(ups) != 0 {
				t.Errorf("an unreadable map still wrote %v", names(ups))
			}
			requireNoBoot(t, s)
		})
	}
}

func TestReconcileFork_ReReadStillSetFails(t *testing.T) {
	s := pitrFixture(t, "NONE", pitrVars(nil))
	s.bendRead(t, retryPostgresID, `. + {"WAL_ARCHIVE_BUCKET":"`+pitrBucket+`"}`)
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := len(pitrWrites(t, s)); n != 1 {
		t.Errorf("control: Postgres variableCollectionUpsert calls = %d, want 1, so the re-read verdict follows a write", n)
	}
	errs := errorLines(stdout + stderr)
	if !strings.Contains(errs, "WAL_ARCHIVE_BUCKET") || !strings.Contains(errs, "Value not printed") {
		t.Errorf("::error:: lines do not name WAL_ARCHIVE_BUCKET and say \"Value not printed\": %q", errs)
	}
	requireNoBoot(t, s)
}

func TestReconcileFork_ArchiveValuesNeverPrinted(t *testing.T) {
	cases := []struct {
		name, status string
		bend         string
	}{
		{"blank succeeds", "NONE", ""},
		{"refused: Postgres booted", "SUCCESS", ""},
		{"re-read still set", "NONE", `. + {"WAL_ARCHIVE_BUCKET":"` + pitrBucket + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, c.status, pitrVars(nil))
			if c.bend != "" {
				s.bendRead(t, retryPostgresID, c.bend)
			}
			before := readStore(t, s, retryPostgresID)
			for k, want := range pitrArchive {
				if before[k] != want {
					t.Fatalf("control: store holds %s = %v before the run, want the planted needle", k, before[k])
				}
			}
			stdout, stderr, _ := runReconcileFork(t, s)
			if n := pitrPostgresReads(t, s); n < 1 {
				t.Fatalf("control: Postgres variable map reads = %d, so the scan below sees a run that never met the values", n)
			}
			for k, needle := range pitrArchive {
				if strings.Contains(stdout+stderr, needle) {
					t.Errorf("the output carries the value of %s", k)
				}
			}
		})
	}
}

// pitrPostgresNames lists the Postgres variable names the run wrote, in call order.
func pitrPostgresNames(t *testing.T, s authShim) []string {
	t.Helper()
	var got []string
	for _, u := range s.upserts(t) {
		if u.Service == retryPostgresID {
			got = append(got, u.Name)
		}
	}
	return got
}

func requireNoArchiveNeedles(t *testing.T, output string) {
	t.Helper()
	for k, needle := range pitrArchive {
		if strings.Contains(output, needle) {
			t.Errorf("the output carries the value of %s", k)
		}
	}
}

func TestReconcileFork_ArchivePrefixMatch(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"whitespace-only value is non-empty", map[string]string{"WAL_ARCHIVE_BUCKET": "   "}, []string{"WAL_ARCHIVE_BUCKET"}},
		{"the bare prefix is a match", map[string]string{"WAL_ARCHIVE_": "pitr-bare-planted"}, []string{"WAL_ARCHIVE_"}},
		{"a name beyond the seven is a match", map[string]string{"WAL_ARCHIVE_NEW_KNOB": "pitr-knob-planted"}, []string{"WAL_ARCHIVE_NEW_KNOB"}},
		{"lowercase name is not a match", map[string]string{"wal_archive_bucket": pitrBucket}, nil},
		{"prefix inside a name is not a match", map[string]string{"X_WAL_ARCHIVE_BUCKET": pitrBucket}, nil},
		{"prefix without its underscore is not a match", map[string]string{"WAL_ARCHIVE": pitrBucket}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", c.vars)
			stdout, stderr, code := runReconcileFork(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
			}
			if n := pitrPostgresReads(t, s); n < 1 {
				t.Fatalf("control: Postgres variable map reads = %d, want at least 1", n)
			}
			got := pitrPostgresNames(t, s)
			if !slices.Equal(got, c.want) {
				t.Errorf("Postgres names written = %v, want %v", got, c.want)
			}
			if len(c.want) == 0 {
				if want := "Postgres in " + retryForkEnv + ": no WAL_ARCHIVE_* value to blank."; !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q; stdout = %q", want, stdout)
				}
				held := readStore(t, s, retryPostgresID)
				for k, v := range c.vars {
					if held[k] != v {
						t.Errorf("Postgres.%s = %v after the run, want its planted value", k, held[k])
					}
				}
				return
			}
			if want := "Postgres WAL archiving blank in " + retryForkEnv + ": " + strings.Join(c.want, " ") + "."; !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q; stdout = %q", want, stdout)
			}
			held := readStore(t, s, retryPostgresID)
			for _, n := range c.want {
				if v, ok := held[n]; !ok || v != "" {
					t.Errorf("re-read Postgres.%s = %v (present %v), want \"\"", n, v, ok)
				}
			}
		})
	}
}

func TestReconcileFork_ArchiveWriteFailureStopsTheFork(t *testing.T) {
	cases := []struct{ name, file, body string }{
		{"transport failure", "upsert-WAL_ARCHIVE_BUCKET.fail", "curl: (22) The requested URL returned error: 500\n"},
		{"GraphQL error", "upsert-WAL_ARCHIVE_BUCKET.json", gqlNotAuthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			writeFile(t, filepath.Join(s.dir, c.file), c.body)
			stdout, stderr, code := runReconcileFork(t, s)

			if code == 0 {
				t.Errorf("exit 0, want non-zero; output = %q", stdout+stderr)
			}
			if n := len(pitrWrites(t, s)); n < 1 {
				t.Errorf("control: Postgres variableCollectionUpsert calls = %d, want the write that failed", n)
			}
			requireNoBoot(t, s)
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_ReReadMustShowEveryNameBlank(t *testing.T) {
	cases := []struct{ name, filter, variable string }{
		{"map null after the write", `if .WAL_ARCHIVE_BUCKET == "" then null else . end`, "WAL_ARCHIVE_BUCKET"},
		{"name absent after the write", `if .WAL_ARCHIVE_BUCKET == "" then del(.WAL_ARCHIVE_BUCKET) else . end`, "WAL_ARCHIVE_BUCKET"},
		{"last name still set", `. + {"WAL_ARCHIVE_SECRET":"` + pitrSecret + `"}`, "WAL_ARCHIVE_SECRET"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			s.bendRead(t, retryPostgresID, c.filter)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := len(pitrWrites(t, s)); n != 1 {
				t.Errorf("control: Postgres variableCollectionUpsert calls = %d, want 1, so the verdict follows a write", n)
			}
			errs := errorLines(stdout + stderr)
			if !strings.Contains(errs, c.variable) || !strings.Contains(errs, "Value not printed") {
				t.Errorf("::error:: lines do not name %s and say \"Value not printed\": %q", c.variable, errs)
			}
			requireNoBoot(t, s)
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_UnreadableDeploymentStateRefusesWithoutWriting(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, authShim)
		want  []string
	}{
		{"GraphQL error reading the instance", func(t *testing.T, s authShim) { setFaults(t, s, "svcInstance", "gqlerr") }, nil},
		{"deployment object without a status", func(t *testing.T, s authShim) {
			writeFile(t, filepath.Join(s.dir, "svcInstance.json"), `{"data":{"serviceInstance":{"serviceName":"Postgres","latestDeployment":{"id":"dep-x"}}}}`)
		}, []string{retryForkEnv, "delete", "status unknown"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			c.setup(t, s)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, "svcInstance"); n < 1 {
				t.Errorf("control: svcInstance calls = %d, want the instance read", n)
			}
			for _, w := range c.want {
				if !strings.Contains(errorLines(stdout+stderr), w) {
					t.Errorf("::error:: lines lack %q: %q", w, errorLines(stdout+stderr))
				}
			}
			if ups := s.upserts(t); len(ups) != 0 {
				t.Errorf("an unreadable deployment state still wrote %v", names(ups))
			}
			requireNoBoot(t, s)
		})
	}
}

func TestReconcileFork_ArchiveWriteLinesAreRedacted(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want int
	}{
		{"one name", map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket}, 1},
		{"seven names", pitrVars(nil), 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", c.vars)
			stdout, stderr, code := runReconcileFork(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
			}
			var lines []string
			for _, l := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(l, "  Postgres.WAL_ARCHIVE_") {
					lines = append(lines, l)
				}
			}
			if len(lines) != c.want {
				t.Fatalf("Postgres write lines = %d, want %d; stdout = %q", len(lines), c.want, stdout)
			}
			for _, l := range lines {
				if !pitrRedactedLine.MatchString(l) {
					t.Errorf("write line %q is not `Postgres.<NAME> = <redacted>`", l)
				}
			}
		})
	}
}
