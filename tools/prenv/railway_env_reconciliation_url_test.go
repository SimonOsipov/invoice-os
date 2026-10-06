// railway_env_reconciliation_url_test.go drives railway-env.sh set-fork-reconciliation-url against a stateful scripted Railway.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	reconciliationURL       = "http://reconciliation.railway.internal:8080"
	reconciliationURLUsage  = "usage: railway-env.sh set-fork-reconciliation-url <environment-id>"
	reconciliationURLRunCmd = `bash scripts/ci/railway-env.sh set-fork-reconciliation-url "$ENV_ID"`
	reconciliationDBNeedle  = "postgresql://invoice_app:planted-recon-db-password@h:5432/railway"
)

var reconciliationGatewayID = sentrySvcID("gateway")

// reconciliationConfirmed reports whether a non-error line confirms gateway.RECONCILIATION_URL in the fork.
func reconciliationConfirmed(out string) bool {
	for _, l := range strings.Split(out, "\n") {
		if !strings.Contains(l, "::error::") && strings.Contains(strings.ToLower(l), "confirmed") &&
			strings.Contains(l, "RECONCILIATION_URL") && strings.Contains(l, forkEnvID) {
			return true
		}
	}
	return false
}

func TestSetForkReconciliationURLAgainstAScriptedRailway(t *testing.T) {
	noUpsert := func(says string) func(*testing.T, authShim, string) {
		return func(t *testing.T, s authShim, out string) {
			if !strings.Contains(errorLines(out), says) {
				t.Errorf("no ::error:: line says %q; output = %q", says, out)
			}
			if ups := s.upserts(t); len(ups) != 0 {
				t.Errorf("a refused environment received upserts %v", names(ups))
			}
		}
	}
	// A failed write attempted exactly the one upsert and never re-read the gateway.
	writeFailed := func(t *testing.T, s authShim, _ string) {
		if ups := s.upserts(t); len(ups) != 1 {
			t.Errorf("upserts = %v, want the one attempted write", names(ups))
		}
		if at := s.lastCallIndex(t, reconciliationGatewayID, "RECONCILIATION_URL"); at < 0 || s.readAfter(t, reconciliationGatewayID, at) {
			t.Errorf("a failed write was followed by a re-read (write at call %d)", at)
		}
	}
	rereadRefused := func(t *testing.T, s authShim, out string) {
		if !strings.Contains(errorLines(out), "gateway.RECONCILIATION_URL") {
			t.Errorf("no ::error:: line names gateway.RECONCILIATION_URL; output = %q", out)
		}
		if at := s.lastCallIndex(t, reconciliationGatewayID, "RECONCILIATION_URL"); at < 0 || !s.readAfter(t, reconciliationGatewayID, at) {
			t.Errorf("the refusal did not come from a re-read after the write (write at call %d)", at)
		}
	}

	// The batched read precedes the write, so an unreadable map refuses with no write.
	unreadableRefused := func(t *testing.T, s authShim, out string) {
		if errs := errorLines(out); !strings.Contains(errs, "gateway") || !strings.Contains(errs, "unreadable") {
			t.Errorf("no ::error:: line names the gateway and says it is unreadable; output = %q", out)
		}
		if ups := s.upserts(t); len(ups) != 0 {
			t.Errorf("an unreadable map was followed by writes %v", names(ups))
		}
	}
	// The write landed and only its re-read is unreadable.
	rereadUnreadable := func(t *testing.T, s authShim, out string) {
		if errs := errorLines(out); !strings.Contains(errs, "gateway") || !strings.Contains(errs, "written but not confirmed") {
			t.Errorf("no ::error:: line names the gateway as written but not confirmed; output = %q", out)
		}
		if at := s.lastCallIndex(t, reconciliationGatewayID, "RECONCILIATION_URL"); at < 0 || !s.readAfter(t, reconciliationGatewayID, at) {
			t.Errorf("the refusal did not come from a re-read after the write (write at call %d)", at)
		}
	}

	cases := []struct {
		name    string
		envList string
		settle  string
		bend    string            // jq filter over the gateway's re-read
		files   map[string]string // shim file -> body
		code    int
		check   func(t *testing.T, s authShim, out string)
	}{
		{name: "writes_and_confirms", code: 0, check: checkReconciliationURLWritten},
		{name: "not_ephemeral", envList: sentryEnvList(false, true), code: 1, check: noUpsert("is NOT ephemeral")},
		{name: "foreign_id", envList: sentryEnvList(true, false), code: 1, check: noUpsert("No environment with id " + forkEnvID)},
		{name: "gateway_not_listed", settle: sentrySettle("gateway"), code: 1, check: noUpsert("RECONCILIATION_URL was NOT set")},
		{name: "write_refused", files: map[string]string{"upsert-RECONCILIATION_URL.json": `{"errors":[{"message":"Not Authorized"}]}`}, code: 1, check: writeFailed},
		{name: "write_transport_failure", files: map[string]string{"upsert-RECONCILIATION_URL.fail": "curl: (22) The requested URL returned error: 400"}, code: 1, check: writeFailed},
		{name: "reread_absent", bend: `del(.RECONCILIATION_URL)`, code: 1, check: rereadRefused},
		{name: "reread_empty", bend: `.RECONCILIATION_URL = ""`, code: 1, check: rereadRefused},
		{name: "reread_different", bend: `.RECONCILIATION_URL = "http://reconciliation.railway.internal:8081"`, code: 1, check: rereadRefused},
		{name: "read_unreadable", bend: `"not-a-map"`, code: 1, check: unreadableRefused},
		// Only the re-read after the write is unreadable.
		{name: "reread_unreadable", bend: `if .RECONCILIATION_URL == "` + reconciliationURL + `" then "not-a-map" else . end`, code: 1, check: rereadUnreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := map[string]string{"envList": sentryEnvList(true, true), "settle": sentrySettle("")}
			if c.envList != "" {
				resp["envList"] = c.envList
			}
			if c.settle != "" {
				resp["settle"] = c.settle
			}
			s := newAuthShim(t, resp, map[string]map[string]string{
				reconciliationGatewayID: {"DATABASE_URL": reconciliationDBNeedle},
			})
			if c.bend != "" {
				s.bendRead(t, reconciliationGatewayID, c.bend)
			}
			for f, body := range c.files {
				writeFile(t, filepath.Join(s.dir, f), body)
			}

			stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-fork-reconciliation-url", forkEnvID)
			out := stdout + stderr
			if code != c.code {
				t.Fatalf("exit %d, want %d; output = %q", code, c.code, out)
			}
			c.check(t, s, out)
			if c.code != 0 && reconciliationConfirmed(out) {
				t.Errorf("a failed run printed the confirmation line; output = %q", out)
			}
			for _, n := range []string{reconciliationDBNeedle, forkToken} {
				if strings.Contains(out, n) {
					t.Errorf("output leaks %q; output = %q", n, out)
				}
			}
		})
	}
}

func checkReconciliationURLWritten(t *testing.T, s authShim, out string) {
	t.Helper()
	ups := s.upserts(t)
	if want := (authUpsert{reconciliationGatewayID, "RECONCILIATION_URL", reconciliationURL}); len(ups) != 1 || ups[0] != want {
		t.Errorf("upserts = %v, want exactly [%v]", ups, want)
	}
	ws := collectionWrites(t, s)
	for _, w := range ws {
		if w.SkipDeploys != true || w.Env != forkEnvID {
			t.Errorf("the %s write: skipDeploys=%v environmentId=%v, want true and %s", w.Service, w.SkipDeploys, w.Env, forkEnvID)
		}
	}
	if len(ws) != 1 || ws[0].Vars["RECONCILIATION_URL"] != reconciliationURL {
		t.Errorf("collection writes = %v, want one carrying RECONCILIATION_URL", writeNames(ws))
	}
	if at := s.lastCallIndex(t, reconciliationGatewayID, "RECONCILIATION_URL"); at < 0 || !s.readAfter(t, reconciliationGatewayID, at) {
		t.Errorf("the gateway's variables were not re-read after the RECONCILIATION_URL write (write at call %d)", at)
	}
	if !reconciliationConfirmed(out) {
		t.Errorf("no confirmation line names RECONCILIATION_URL and the fork %s; output = %q", forkEnvID, out)
	}
	// Control for the leak needle: the map the command read still carried it.
	raw, err := os.ReadFile(filepath.Join(s.dir, "store-"+reconciliationGatewayID+".json"))
	if err != nil || !strings.Contains(string(raw), reconciliationDBNeedle) {
		t.Errorf("control: the gateway's store no longer holds the DATABASE_URL needle (%v), so its absence from output proves nothing", err)
	}
}

func TestSetForkReconciliationURLUsageAndPersistentRefusal(t *testing.T) {
	t.Run("no_argument", func(t *testing.T) {
		shim := newCurlShim(t)
		stdout, stderr, code := runBashScript(t, shim.prelude+"bash '"+railwayEnvScript(t)+"' set-fork-reconciliation-url\n")
		out := stdout + stderr
		if code != 2 {
			t.Errorf("exit %d, want 2; output = %q", code, out)
		}
		if !strings.Contains(out, reconciliationURLUsage) {
			t.Errorf("output lacks its own usage line %q; output = %q", reconciliationURLUsage, out)
		}
		if calls := shim.calls(t); calls != "" {
			t.Errorf("usage called curl:\n%s", calls)
		}
		shim.requireOnPath(t)
	})
	t.Run("persistent_id", func(t *testing.T) {
		shim := newCurlShim(t)
		stdout, stderr, code := runBashScript(t,
			shim.prelude+"export RAILWAY_DEV_ENVIRONMENT_ID="+persistentEnvironmentID+"\nbash '"+railwayEnvScript(t)+"' set-fork-reconciliation-url \"$@\"\n",
			persistentEnvironmentID)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if !strings.Contains(errorLines(out), persistentEnvironmentID) {
			t.Errorf("no ::error:: line names the persistent environment %s; output = %q", persistentEnvironmentID, out)
		}
		if strings.Contains(out, "RAILWAY_API_TOKEN is not set") {
			t.Errorf("the refusal ran after require_env; output = %q", out)
		}
		if calls := shim.calls(t); calls != "" {
			t.Errorf("the refusal called curl:\n%s", calls)
		}
		shim.requireOnPath(t)
	})
}
