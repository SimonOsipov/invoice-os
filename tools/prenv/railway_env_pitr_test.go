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

// Production's Postgres values; the fork's own are in pitrArchive.
const (
	pitrProdBucket = "pitr-prod-bucket-planted-d00d"
	pitrProdKey    = "pitr-prod-key-planted-8e21"
	pitrProdSecret = "pitr-prod-secret-planted-6b73"
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

// requireBootedNoWrite: a booted Postgres is never written and never met the deployment refusal.
func requireBootedNoWrite(t *testing.T, s authShim, output string) {
	t.Helper()
	if n := pitrPostgresReads(t, s); n < 1 {
		t.Errorf("control: Postgres variable map reads = %d, want at least 1", n)
	}
	if w := pitrWrites(t, s); len(w) != 0 {
		t.Errorf("Postgres variableCollectionUpsert calls = %d, want 0 on a booted Postgres", len(w))
	}
	if strings.Contains(output, "already has a deployment") {
		t.Errorf("output carries the deployment refusal; output = %q", output)
	}
}

// requireBootedReuse: a passing booted fork follows the reuse path, which boots nothing.
func requireBootedReuse(t *testing.T, s authShim, stdout string) {
	t.Helper()
	requireNoBoot(t, s)
	for _, want := range []string{"postgres already has a successful deployment", "Fork reconciliation complete"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q; stdout = %q", want, stdout)
		}
	}
}

func TestReconcileFork_C1_BootedBucketDiffersFromProductionPasses(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		kept []string
	}{
		{"all seven names set", pitrVars(nil), []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}},
		{"only BUCKET set", map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket}, []string{"WAL_ARCHIVE_BUCKET"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "SUCCESS", c.vars)
			pitrPlantB3(t, s, c.kept, pitrProdMap(nil))
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 0 {
				t.Fatalf("C1: exit %d, want 0: the booted fork's rendered BUCKET differs from production's; output = %q", code, stdout+stderr)
			}
			requireBootedNoWrite(t, s, stdout+stderr)
			for _, env := range []string{retryForkEnv, persistentEnvironmentID} {
				if n := pitrRenderedReads(t, s, env); n < 1 {
					t.Errorf("C1 control: rendered Postgres map reads of %s = %d, want at least 1: the compare is on rendered values", env, n)
				}
			}
			if n := strings.Count(stdout, "differs from production's"); n != 1 {
				t.Errorf("C1: lines saying the BUCKET differs from production's = %d, want 1; stdout = %q", n, stdout)
			}
			requireBootedReuse(t, s, stdout)
			if errs := errorLines(stdout + stderr); errs != "" {
				t.Errorf("C1: ::error:: lines = %q, want none", errs)
			}
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_C2_BootedBucketUnprovenFails(t *testing.T) {
	kept := []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}
	prodNoBucket := pitrProdMap(nil)
	delete(prodNoBucket, "WAL_ARCHIVE_BUCKET")
	cases := []struct {
		name  string
		setup func(*testing.T, authShim)
		reads []string
	}{
		{"rendered BUCKET equals production's", func(t *testing.T, s authShim) {
			pitrPlantB3(t, s, kept, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket}))
		}, []string{retryForkEnv, persistentEnvironmentID}},
		{"fork rendered map unreadable", func(t *testing.T, s authShim) {
			pitrPlantB3(t, s, kept, pitrProdMap(nil))
			writeFile(t, filepath.Join(s.dir, "rendered-"+retryPostgresID+"-"+retryForkEnv+".json"), "null")
		}, []string{retryForkEnv}},
		{"fork rendered BUCKET empty", func(t *testing.T, s authShim) {
			pitrPlantB3(t, s, kept, pitrProdMap(nil))
			writeFile(t, filepath.Join(s.dir, "rendered-"+retryPostgresID+"-"+retryForkEnv+".json"), `{"WAL_ARCHIVE_BUCKET":""}`)
		}, []string{retryForkEnv}},
		{"production rendered map unreadable", func(t *testing.T, s authShim) { pitrPlantB3(t, s, kept, nil) }, []string{retryForkEnv, persistentEnvironmentID}},
		{"production rendered BUCKET empty", func(t *testing.T, s authShim) {
			pitrPlantB3(t, s, kept, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": ""}))
		}, []string{retryForkEnv, persistentEnvironmentID}},
		{"production rendered BUCKET absent", func(t *testing.T, s authShim) { pitrPlantB3(t, s, kept, prodNoBucket) }, []string{retryForkEnv, persistentEnvironmentID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "SUCCESS", pitrVars(nil))
			c.setup(t, s)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("C2: exit %d, want 1: isolation from production is not proven; output = %q", code, stdout+stderr)
			}
			requireBootedNoWrite(t, s, stdout+stderr)
			for _, env := range c.reads {
				if n := pitrRenderedReads(t, s, env); n < 1 {
					t.Errorf("C2 control: rendered Postgres map reads of %s = %d, want at least 1: the refusal rests on the compare", env, n)
				}
			}
			if errs := errorLines(stdout + stderr); !strings.Contains(errs, "WAL_ARCHIVE_BUCKET") || !strings.Contains(errs, "production") {
				t.Errorf("C2: ::error:: lines do not name WAL_ARCHIVE_BUCKET and production: %q", errs)
			}
			if strings.Contains(stdout, "differs from production's") || strings.Contains(stdout, "Fork reconciliation complete") {
				t.Errorf("C2: stdout carries a pass line; stdout = %q", stdout)
			}
			requireNoBoot(t, s)
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

// C1, C2: every deployment status takes the booted path, whatever the later reuse step does with it.
func TestReconcileFork_C1C2_EveryDeploymentStatusTakesTheBootedPath(t *testing.T) {
	kept := []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}
	for _, status := range []string{"SUCCESS", "FAILED", "CRASHED", "BUILDING", "DEPLOYING", "REMOVED", "SLEEPING", "SKIPPED", "NEEDS_APPROVAL", "QUEUED", "INITIALIZING", "WAITING"} {
		t.Run(status+"/differs passes the gate", func(t *testing.T) {
			s := pitrFixture(t, status, pitrVars(nil))
			pitrPlantB3(t, s, kept, pitrProdMap(nil))
			stdout, stderr, _ := runReconcileFork(t, s)

			requireBootedNoWrite(t, s, stdout+stderr)
			if n := strings.Count(stdout, "differs from production's"); n != 1 {
				t.Errorf("C1: lines saying the BUCKET differs from production's = %d, want 1; output = %q", n, stdout+stderr)
			}
			requireNoArchiveNeedles(t, stdout+stderr)
		})
		t.Run(status+"/equal refuses", func(t *testing.T) {
			s := pitrFixture(t, status, pitrVars(nil))
			pitrPlantB3(t, s, kept, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket}))
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("C2: exit %d, want 1: the rendered BUCKET equals production's; output = %q", code, stdout+stderr)
			}
			requireBootedNoWrite(t, s, stdout+stderr)
			if errs := errorLines(stdout + stderr); !strings.Contains(errs, "equal to production's") {
				t.Errorf("C2: ::error:: lines lack the equal-to-production refusal: %q", errs)
			}
			requireNoBoot(t, s)
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_C3_BootedBucketNotSetPasses(t *testing.T) {
	absent := pitrVars(nil)
	delete(absent, "WAL_ARCHIVE_BUCKET")
	cases := []struct {
		name string
		vars map[string]string
	}{
		{"BUCKET empty, others set", pitrVars(map[string]string{"WAL_ARCHIVE_BUCKET": ""})},
		{"BUCKET absent, others set", absent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "SUCCESS", c.vars)
			// Production is unreadable: this pass must not need it.
			pitrPlantProduction(t, s, pitrProdMap(nil), "null")
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 0 {
				t.Fatalf("C3: exit %d, want 0: WAL_ARCHIVE_BUCKET is not set, so the fork does not archive; output = %q", code, stdout+stderr)
			}
			requireBootedNoWrite(t, s, stdout+stderr)
			for _, n := range []string{"WAL_ARCHIVE_ENDPOINT", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"} {
				if !strings.Contains(stdout, n) {
					t.Errorf("C3: stdout does not name the kept %s; stdout = %q", n, stdout)
				}
			}
			if !strings.Contains(stdout, "WAL_ARCHIVE_BUCKET is blank") {
				t.Errorf("C3: stdout lacks the B2 line; stdout = %q", stdout)
			}
			if strings.Contains(stdout, "differs from production's") {
				t.Errorf("C3: stdout carries the B3a line, so the pass went through the compare; stdout = %q", stdout)
			}
			requireBootedReuse(t, s, stdout)
			requireNoArchiveNeedles(t, stdout+stderr)
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

func TestReconcileFork_ArchiveValuesNeverPrinted(t *testing.T) {
	b3 := []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}
	cases := []struct {
		name, status string
		setup        func(*testing.T, authShim)
		readsProd    bool
	}{
		{"blank succeeds", "NONE", nil, false},
		{"booted: bucket differs from production's", "SUCCESS", func(t *testing.T, s authShim) { pitrPlantB3(t, s, b3, pitrProdMap(nil)) }, true},
		{"booted: bucket equals production's", "SUCCESS", func(t *testing.T, s authShim) {
			pitrPlantB3(t, s, b3, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket}))
		}, true},
		{"booted: production rendered map unreadable", "SUCCESS", func(t *testing.T, s authShim) { pitrPlantB3(t, s, b3, nil) }, true},
		{"booted: BUCKET blank, others kept", "SUCCESS", func(t *testing.T, s authShim) {
			pitrPlantProduction(t, s, pitrProdMap(nil))
			s.bendRead(t, retryPostgresID, `. + {"WAL_ARCHIVE_BUCKET": ""}`)
		}, false},
		{"B3a bucket kept, rendered values differ from production's", "NONE", func(t *testing.T, s authShim) { pitrPlantB3(t, s, b3, pitrProdMap(nil)) }, true},
		{"B3b bucket kept, rendered values equal production's", "NONE", func(t *testing.T, s authShim) {
			pitrPlantB3(t, s, b3, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket}))
		}, true},
		{"B3c production rendered bucket empty", "NONE", func(t *testing.T, s authShim) {
			pitrPlantB3(t, s, b3, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": ""}))
		}, true},
		{"B3c production rendered map unreadable", "NONE", func(t *testing.T, s authShim) { pitrPlantB3(t, s, b3, nil) }, true},
		{"B2 bucket blank, others kept", "NONE", func(t *testing.T, s authShim) {
			pitrPlantProduction(t, s, pitrProdMap(nil))
			s.bendRead(t, retryPostgresID, pitrKeepAfterBlank("WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"))
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, c.status, pitrVars(nil))
			pitrPlantProduction(t, s, pitrProdMap(nil))
			if c.setup != nil {
				c.setup(t, s)
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
			if c.readsProd && pitrRenderedReads(t, s, persistentEnvironmentID) < 1 {
				t.Fatalf("control: production rendered Postgres map reads = 0, so the scan below sees a run that never met production's values")
			}
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

// pitrProdMap is production's Postgres map: its own BUCKET, KEY, SECRET; ENDPOINT and REGION equal the fork's.
func pitrProdMap(over map[string]string) map[string]string {
	m := map[string]string{
		"WAL_ARCHIVE_BUCKET":   pitrProdBucket,
		"WAL_ARCHIVE_KEY":      pitrProdKey,
		"WAL_ARCHIVE_SECRET":   pitrProdSecret,
		"WAL_ARCHIVE_ENDPOINT": pitrEndpoint,
		"WAL_ARCHIVE_REGION":   pitrArchive["WAL_ARCHIVE_REGION"],
	}
	for k, v := range over {
		m[k] = v
	}
	return m
}

// pitrPlantProduction serves m, bent by filter when given, as the Postgres map of the source environment.
func pitrPlantProduction(t *testing.T, s authShim, m map[string]string, filter ...string) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.dir, "store-"+retryPostgresID+"-"+persistentEnvironmentID+".json"), string(raw))
	for _, f := range filter {
		writeFile(t, filepath.Join(s.dir, "read-"+retryPostgresID+"-"+persistentEnvironmentID+".jq"), f)
	}
}

// pitrPlantB3 plants the measured shape: the unrendered BUCKET (kept in the fork, held in production) is the same
// template in both environments, and only the rendered maps tell them apart. A nil prodRendered is an unreadable read.
func pitrPlantB3(t *testing.T, s authShim, kept []string, prodRendered map[string]string) {
	t.Helper()
	s.bendRead(t, retryPostgresID, pitrKeep(kept...))
	tpl := map[string]string{}
	for _, n := range []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET", "WAL_ARCHIVE_ENDPOINT", "WAL_ARCHIVE_REGION"} {
		tpl[n] = pitrTemplate(n)
	}
	pitrPlantProduction(t, s, tpl)
	fork := map[string]string{}
	for _, n := range kept {
		fork[n] = pitrArchive[n]
	}
	for env, m := range map[string]map[string]string{retryForkEnv: fork, persistentEnvironmentID: prodRendered} {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(s.dir, "rendered-"+retryPostgresID+"-"+env+".json"), string(raw))
	}
}

// pitrRenderedReads counts reads of env's Postgres map made without unrendered: true.
func pitrRenderedReads(t *testing.T, s authShim, env string) int {
	t.Helper()
	n := 0
	for _, c := range s.calls(t) {
		if strings.Contains(c.Query, "variables(projectId") && !strings.Contains(c.Query, "unrendered") && c.Variables["s"] == retryPostgresID && c.Variables["e"] == env {
			n++
		}
	}
	return n
}

// pitrProductionReads counts reads of the source environment's Postgres variable map.
func pitrProductionReads(t *testing.T, s authShim) int {
	t.Helper()
	n := 0
	for _, c := range s.calls(t) {
		if strings.Contains(c.Query, "variables(projectId") && c.Variables["s"] == retryPostgresID && c.Variables["e"] == persistentEnvironmentID {
			n++
		}
	}
	return n
}

// pitrTemplate is the unrendered value Railway holds for a bucket-linked name, in fork and production alike.
func pitrTemplate(name string) string {
	return "${{Postgres-PITR." + strings.TrimPrefix(name, "WAL_ARCHIVE_") + "}}"
}

// pitrKeepObj is a jq object that holds each name at its reference template, as the unrendered read shows it.
func pitrKeepObj(names ...string) string {
	kept := map[string]string{}
	for _, n := range names {
		kept[n] = pitrTemplate(n)
	}
	raw, _ := json.Marshal(kept)
	return string(raw)
}

// pitrKeep is a read filter for a Railway that does not keep "" on names.
func pitrKeep(names ...string) string { return ". + " + pitrKeepObj(names...) }

// pitrKeepAfterBlank keeps names but lets WAL_ARCHIVE_BUCKET read "" once the write landed.
func pitrKeepAfterBlank(names ...string) string {
	return `if .WAL_ARCHIVE_BUCKET == "" then . + ` + pitrKeepObj(names...) + ` else . end`
}

func TestReconcileFork_B2_KeptNamesPassWhenBucketBlank(t *testing.T) {
	kept := []string{"WAL_ARCHIVE_ENDPOINT", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_REGION", "WAL_ARCHIVE_SECRET"}
	cases := []struct{ name, filter string }{
		{"bucket reads empty", pitrKeepAfterBlank(kept...)},
		{"bucket reads absent", `if .WAL_ARCHIVE_BUCKET == "" then del(.WAL_ARCHIVE_BUCKET) + ` + pitrKeepObj(kept...) + ` else . end`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			// Production is unreadable: B2 must pass without it.
			pitrPlantProduction(t, s, pitrProdMap(nil), "null")
			s.bendRead(t, retryPostgresID, c.filter)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 0 {
				t.Fatalf("B2: exit %d, want 0: a kept name is fine while WAL_ARCHIVE_BUCKET is blank; output = %q", code, stdout+stderr)
			}
			if n := len(pitrWrites(t, s)); n != 1 {
				t.Errorf("B2 control: Postgres variableCollectionUpsert calls = %d, want 1, so the pass follows the blank", n)
			}
			for _, n := range kept {
				if !strings.Contains(stdout, n) {
					t.Errorf("B2: stdout does not name the kept %s; stdout = %q", n, stdout)
				}
			}
			if n := opCount(t, s, "svcDeploy"); n != 1 {
				t.Errorf("B2: svcDeploy calls = %d, want 1: the fork goes on to boot Postgres", n)
			}
			if !strings.Contains(stdout, "Fork reconciliation complete") {
				t.Errorf("B2: stdout lacks the completion line; stdout = %q", stdout)
			}
			if errs := errorLines(stdout + stderr); errs != "" {
				t.Errorf("B2: ::error:: lines = %q, want none", errs)
			}
			if strings.Contains(stdout, "differs from production's") {
				t.Errorf("B2: stdout carries the B3a line, so the pass went through the production compare; stdout = %q", stdout)
			}
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_B3a_BucketDiffersFromProductionPasses(t *testing.T) {
	all := []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_ENDPOINT", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_REGION", "WAL_ARCHIVE_SECRET"}
	cases := []struct {
		name   string
		kept   []string
		prod   map[string]string
		listed []string
	}{
		{"measured: unrendered templates identical, rendered BUCKET, KEY, SECRET differ", all, pitrProdMap(nil), []string{"WAL_ARCHIVE_ENDPOINT", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_REGION", "WAL_ARCHIVE_SECRET"}},
		{"only BUCKET kept", []string{"WAL_ARCHIVE_BUCKET"}, pitrProdMap(nil), nil},
		{"rendered BUCKET differs while KEY, SECRET equal production's", []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}, pitrProdMap(map[string]string{"WAL_ARCHIVE_KEY": pitrKey, "WAL_ARCHIVE_SECRET": pitrSecret}), []string{"WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}},
		{"rendered BUCKET differs while production's KEY equals the fork's BUCKET", []string{"WAL_ARCHIVE_BUCKET"}, pitrProdMap(map[string]string{"WAL_ARCHIVE_KEY": pitrBucket}), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			pitrPlantB3(t, s, c.kept, c.prod)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 0 {
				t.Fatalf("B3a: exit %d, want 0: the fork's rendered BUCKET differs from production's; output = %q", code, stdout+stderr)
			}
			for _, env := range []string{retryForkEnv, persistentEnvironmentID} {
				if n := pitrRenderedReads(t, s, env); n < 1 {
					t.Errorf("B3a control: rendered Postgres map reads of %s = %d, want at least 1: the compare is on rendered values", env, n)
				}
			}
			var lines []string
			for _, l := range strings.Split(stdout, "\n") {
				if strings.Contains(l, "differs from production's") {
					lines = append(lines, l)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("B3a: lines saying the BUCKET differs from production's = %d, want 1; stdout = %q", len(lines), stdout)
			}
			for _, want := range append([]string{"Postgres in " + retryForkEnv + " keeps WAL_ARCHIVE_", "so the fork archives to its own bucket."}, c.listed...) {
				if !strings.Contains(lines[0], want) {
					t.Errorf("B3a: the pass line lacks %q: %q", want, lines[0])
				}
			}
			if n := opCount(t, s, "svcDeploy"); n != 1 {
				t.Errorf("B3a: svcDeploy calls = %d, want 1: the fork goes on to boot Postgres", n)
			}
			if !strings.Contains(stdout, "Fork reconciliation complete") {
				t.Errorf("B3a: stdout lacks the completion line; stdout = %q", stdout)
			}
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_B3b_BucketEqualsProductionFails(t *testing.T) {
	all := []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}
	cases := []struct {
		name string
		kept []string
		prod map[string]string
	}{
		{"rendered BUCKET equal", []string{"WAL_ARCHIVE_BUCKET"}, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket})},
		{"rendered BUCKET equal while KEY, SECRET differ", all, pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": pitrBucket})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			pitrPlantB3(t, s, c.kept, c.prod)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("B3b: exit %d, want 1: the fork would archive into production's bucket; output = %q", code, stdout+stderr)
			}
			for _, env := range []string{retryForkEnv, persistentEnvironmentID} {
				if n := pitrRenderedReads(t, s, env); n < 1 {
					t.Errorf("B3b control: rendered Postgres map reads of %s = %d, want at least 1: the refusal rests on rendered values", env, n)
				}
			}
			errs := strings.ToLower(errorLines(stdout + stderr))
			for _, want := range []string{strings.ToLower(retryForkEnv), "archive", "production", "bucket"} {
				if !strings.Contains(errs, want) {
					t.Errorf("B3b: ::error:: lines lack %q: %q", want, errs)
				}
			}
			if strings.Contains(stdout, "differs from production's") || strings.Contains(stdout, "Fork reconciliation complete") {
				t.Errorf("B3b: stdout carries a pass line; stdout = %q", stdout)
			}
			requireNoBoot(t, s)
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_B3c_ProductionBucketUnprovenFails(t *testing.T) {
	prodNoBucket := pitrProdMap(nil)
	delete(prodNoBucket, "WAL_ARCHIVE_BUCKET")
	cases := []struct {
		name string
		prod map[string]string
	}{
		{"production rendered map unreadable", nil},
		{"production rendered BUCKET empty", pitrProdMap(map[string]string{"WAL_ARCHIVE_BUCKET": ""})},
		{"production rendered BUCKET absent", prodNoBucket},
		{"production rendered map empty", map[string]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pitrFixture(t, "NONE", pitrVars(nil))
			pitrPlantB3(t, s, []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}, c.prod)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("B3c: exit %d, want 1: isolation from production is not proven; output = %q", code, stdout+stderr)
			}
			if n := pitrRenderedReads(t, s, persistentEnvironmentID); n < 1 {
				t.Errorf("B3c control: rendered source-environment Postgres map reads = %d, want at least 1: the refusal rests on that read", n)
			}
			if errs := strings.ToLower(errorLines(stdout + stderr)); !strings.Contains(errs, "production") {
				t.Errorf("B3c: ::error:: lines do not name production: %q", errs)
			}
			if strings.Contains(stdout, "differs from production's") || strings.Contains(stdout, "Fork reconciliation complete") {
				t.Errorf("B3c: stdout carries a pass line; stdout = %q", stdout)
			}
			requireNoBoot(t, s)
			requireNoArchiveNeedles(t, stdout+stderr)
		})
	}
}

func TestReconcileFork_B3c_ForkRenderedBucketUnprovenFails(t *testing.T) {
	s := pitrFixture(t, "NONE", pitrVars(nil))
	pitrPlantB3(t, s, []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}, pitrProdMap(nil))
	writeFile(t, filepath.Join(s.dir, "rendered-"+retryPostgresID+"-"+retryForkEnv+".json"), "null")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("B3c: exit %d, want 1: a kept reference with no rendered value proves nothing; output = %q", code, stdout+stderr)
	}
	if n := pitrRenderedReads(t, s, retryForkEnv); n < 1 {
		t.Errorf("B3c control: rendered fork Postgres map reads = %d, want at least 1: the refusal rests on that read", n)
	}
	if strings.Contains(stdout, "differs from production's") || strings.Contains(stdout, "Fork reconciliation complete") {
		t.Errorf("B3c: stdout carries a pass line; stdout = %q", stdout)
	}
	requireNoBoot(t, s)
	requireNoArchiveNeedles(t, stdout+stderr)
}

// B3c, B4: a rendered read that fails hard exits inside $( ), so the caller sees an empty value and must still stop.
// The failing body holds secrets and must never reach the output.
func TestReconcileFork_B3c_RenderedReadTransportFailureFailsClosed(t *testing.T) {
	kept := []string{"WAL_ARCHIVE_BUCKET", "WAL_ARCHIVE_KEY", "WAL_ARCHIVE_SECRET"}
	secretBody := `{"data":{"variables":{"WAL_ARCHIVE_BUCKET":"` + pitrBucket + `","WAL_ARCHIVE_KEY":"` + pitrKey + `","WAL_ARCHIVE_SECRET":"` + pitrSecret +
		`","PROD_BUCKET":"` + pitrProdBucket + `","PROD_KEY":"` + pitrProdKey + `","PROD_SECRET":"` + pitrProdSecret + `"}},"errors":[{"message":"boom","extensions":{"code":"BAD_USER_INPUT"}}]}`
	// nth svcVars call that is the rendered read of env: a baseline run counts the ones before it.
	renderedIndex := func(t *testing.T, env string) int {
		s := pitrFixture(t, "NONE", pitrVars(nil))
		pitrPlantB3(t, s, kept, pitrProdMap(nil))
		runReconcileFork(t, s)
		i := 0
		for _, c := range s.calls(t) {
			if !strings.Contains(c.Query, "svcVars") {
				continue
			}
			if c.Variables["s"] == retryPostgresID && c.Variables["e"] == env {
				return i
			}
			i++
		}
		t.Fatalf("control: no rendered Postgres read of %s in the baseline run", env)
		return -1
	}
	cases := []struct {
		name  string
		fault []string
	}{
		{"HTTP 400 whose body holds secrets", []string{"400"}},
		{"GraphQL error", []string{"gqlerr"}},
		{"HTTP 500 on every attempt", []string{"500", "500", "500"}},
	}
	for _, side := range []struct{ name, env string }{{"fork", retryForkEnv}, {"production", persistentEnvironmentID}} {
		env := side.env
		for _, c := range cases {
			t.Run(side.name+"/"+c.name, func(t *testing.T) {
				idx := renderedIndex(t, env)
				s := pitrFixture(t, "NONE", pitrVars(nil))
				pitrPlantB3(t, s, kept, pitrProdMap(nil))
				writeFile(t, filepath.Join(s.dir, "faultbody-svcVars"), secretBody)
				setFaults(t, s, "svcVars", append(slices.Repeat([]string{"ok"}, idx), c.fault...)...)
				stdout, stderr, code := runReconcileFork(t, s)

				if code != 1 {
					t.Errorf("B3c: exit %d, want 1: a failed rendered read proves no isolation; output = %q", code, stdout+stderr)
				}
				if n := pitrRenderedReads(t, s, env); n < 1 {
					t.Errorf("B3c control: rendered reads of %s = %d, so the fault never met the read", env, n)
				}
				if want := "rendered variables in environment " + env; !strings.Contains(errorLines(stdout+stderr), want) {
					t.Errorf("B3c: ::error:: lines lack %q, so the fault missed the rendered read: %q", want, errorLines(stdout+stderr))
				}
				if strings.Contains(stdout, "differs from production's") || strings.Contains(stdout, "Fork reconciliation complete") {
					t.Errorf("B3c: stdout carries a pass line; stdout = %q", stdout)
				}
				requireNoBoot(t, s)
				requireNoArchiveNeedles(t, stdout+stderr)
			})
		}
	}
}

func TestReconcileFork_B4_UnreadableReReadFails(t *testing.T) {
	s := pitrFixture(t, "NONE", pitrVars(nil))
	pitrPlantProduction(t, s, pitrProdMap(nil))
	s.bendRead(t, retryPostgresID, `if .WAL_ARCHIVE_BUCKET == "" then null else . end`)
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("B4: exit %d, want 1: an unreadable re-read proves nothing; output = %q", code, stdout+stderr)
	}
	if n := len(pitrWrites(t, s)); n != 1 {
		t.Errorf("B4 control: Postgres variableCollectionUpsert calls = %d, want 1, so the verdict follows a write", n)
	}
	requireNoBoot(t, s)
	requireNoArchiveNeedles(t, stdout+stderr)

	// set_service_vars is unchanged by the batched passes; its re-read must still follow its write.
	for _, fault := range setServiceVarsReReadFaults(shellCode(shellFunctionBody(t, "set_service_vars")), shellCode(shellFunctionBody(t, "auth_read"))) {
		t.Errorf("set_service_vars: %s", fault)
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
	for k, needle := range map[string]string{"production WAL_ARCHIVE_BUCKET": pitrProdBucket, "production WAL_ARCHIVE_KEY": pitrProdKey, "production WAL_ARCHIVE_SECRET": pitrProdSecret} {
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
		{"deployment status null", func(t *testing.T, s authShim) {
			writeFile(t, filepath.Join(s.dir, "svcInstance.json"), `{"data":{"serviceInstance":{"serviceName":"Postgres","latestDeployment":{"id":"dep-x","status":null}}}}`)
		}, []string{retryForkEnv, "delete", "status unknown"}},
		{"deployment status empty string", func(t *testing.T, s authShim) {
			writeFile(t, filepath.Join(s.dir, "svcInstance.json"), `{"data":{"serviceInstance":{"serviceName":"Postgres","latestDeployment":{"id":"dep-x","status":""}}}}`)
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
