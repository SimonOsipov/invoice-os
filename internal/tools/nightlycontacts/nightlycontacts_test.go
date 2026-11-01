package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/notifications"
)

func greenStart(t *testing.T, w *world) result {
	t.Helper()
	return runPhase(t, w, baseEnv(), "start")
}

func TestIsRunContact(t *testing.T) {
	for addr, want := range map[string]bool{
		"x@example.com":                   true,
		"X@EXAMPLE.COM":                   true,
		"nightly-1-reg@x.resend.app":      true,
		"NIGHTLY-1-REG@X.RESEND.APP":      true,
		"nightly-ops@acme.com":            false,
		"x@example.com.evil.ng":           false,
		"x@sub.example.com":               false,
		"sam@ascomply.com":                false,
		"sam@x.resend.app":                false,
		"nightly-1@other.resend.app":      false,
		"nightly-1-reg@x.resend.app.evil": false,
		"":                                false,
		"x@example.com ":                  false,
		"x@example.com.au":                false,
		"n\u0130ghtly-1@x.resend.app":     false,
	} {
		if got := isRunContact(addr, "x.resend.app"); got != want {
			t.Errorf("isRunContact(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestPortalGuard_RefusesAndStops(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		detail string
	}{
		"live portal": {200, `{"portalId":148915098}`, "live portal"},
		"401":         {401, `{"portalId":999}`, "answered 401"},
		"bad JSON":    {200, `not json`, "no usable answer"},
		"id zero":     {200, `{"portalId":0}`, "portal id is missing"},
	} {
		for _, phase := range []string{"start", "check"} {
			t.Run(name+" "+phase, func(t *testing.T) {
				w := newWorld()
				w.portalStatus, w.portalBody = tc.status, tc.body
				w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
				w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
				res := runPhase(t, w, baseEnv(), phase)
				if res.code != 1 || res.step(stPortal).Status != "failed" || !strings.Contains(res.step(stPortal).Detail, tc.detail) {
					t.Fatalf("code %d, portal step %+v", res.code, res.step(stPortal))
				}
				if n := w.count("api.hubapi.com"); n != 1 {
					t.Errorf("HubSpot calls = %d, want 1", n)
				}
				if len(w.deleted) != 0 || w.count("api.resend.com") != 0 || w.count("gw.test") != 0 {
					t.Errorf("a vendor or the gateway was touched after the refusal: %v", w.deleted)
				}
			})
		}
	}
}

func TestPortalGuard_AcceptsATestPortal(t *testing.T) {
	w := newWorld()
	res := greenStart(t, w)
	if res.code != 0 || res.step(stPortal).Status != "passed" {
		t.Fatalf("code %d, steps %+v", res.code, res.sec.Steps)
	}
}

func TestFinish_LivePortalDeletesNothingFromHubSpot(t *testing.T) {
	w := newWorld()
	w.portalBody = `{"portalId":148915098}`
	w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
	w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
	res := finish(t, w, baseEnv())
	if res.code != 1 || res.step(stPortal).Status != "failed" {
		t.Fatalf("code %d, portal %+v", res.code, res.step(stPortal))
	}
	if n := w.count("api.hubapi.com"); n != 1 {
		t.Errorf("HubSpot calls = %d, want only the portal read", n)
	}
	if res.step(stDelHS).Status != "skipped" {
		t.Errorf("HubSpot delete step = %+v", res.step(stDelHS))
	}
	if !slices.Equal(w.deleted, []string{"rs:a@example.com"}) {
		t.Errorf("deleted = %v, want the Resend contact", w.deleted)
	}
}

func TestStart_FailsWhenThePortalLacksStageOrProperty(t *testing.T) {
	for name, set := range map[string]func(*world){
		"stage absent":    func(w *world) { w.stages = []string{"other"} },
		"property absent": func(w *world) { w.propStatus = 404 },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			set(w)
			w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
			w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
			res := greenStart(t, w)
			if res.code != 1 || res.step(stConfigured).Status != "failed" || res.step(stLeftovers).Status != "skipped" {
				t.Fatalf("code %d, steps %+v", res.code, res.sec.Steps)
			}
			if len(w.deleted) != 0 {
				t.Errorf("deleted %v", w.deleted)
			}
		})
	}
}

func TestSweep_DeletesOnlyMatchesAcrossPages(t *testing.T) {
	w := newWorld()
	w.pageSize = 2
	old := "2026-01-01T00:00:00Z"
	for _, phase := range []string{"start", "finish"} {
		w.deleted, w.firstDelete, w.hsListEnd, w.rsEnd = nil, -1, -1, -1
		w.hsContacts = []hsContactRow{
			{"1", "a@example.com", ""}, {"2", "nightly-ops@acme.com", ""}, {"3", "B@EXAMPLE.COM", ""},
			{"4", "sam@ascomply.com", ""}, {"5", "nightly-1-reg@x.resend.app", ""},
		}
		w.rsContacts = []rsContactRow{
			{"r1", "nightly-ops@acme.com", old}, {"r2", "c@example.com", old}, {"r3", "x@example.com.evil.ng", old},
			{"r4", "nightly-9-demo@x.resend.app", old}, {"r5", "sam@ascomply.com", old}, {"r6", "", old},
		}
		if phase == "start" {
			greenStart(t, w)
		} else {
			finish(t, w, baseEnv())
		}
		want := []string{"hs:a@example.com", "hs:B@EXAMPLE.COM", "hs:nightly-1-reg@x.resend.app", "rs:c@example.com", "rs:nightly-9-demo@x.resend.app"}
		got := slices.Clone(w.deleted)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s deleted %v, want %v", phase, got, want)
		}
		if w.hsListEnd < 0 || w.rsEnd < 0 || w.hsListEnd > w.firstDelete || w.rsEnd > w.firstDelete {
			t.Errorf("%s: a list ended at %d/%d after the first delete at %d", phase, w.hsListEnd, w.rsEnd, w.firstDelete)
		}
	}
}

func TestNoPhaseCallsASuppressionTopicOrSegmentPath(t *testing.T) {
	w := newWorld()
	w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
	w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
	dir := t.TempDir()
	runPhase(t, w, baseEnv(), "start", "-out", filepath.Join(dir, "start.json"))
	runPhase(t, w, baseEnv(), "check")
	runPhase(t, w, baseEnv(), "finish", "-in", dir, "-started", startedAt)
	if len(w.calls) < 20 {
		t.Fatalf("only %d calls: the phases did not run", len(w.calls))
	}
	for _, c := range w.calls {
		p := strings.ToLower(c.path)
		if strings.Contains(p, "suppression") || strings.Contains(p, "/topics") || strings.Contains(p, "/segments") {
			t.Errorf("%s %s%s", c.method, c.host, c.path)
		}
	}
}

func TestCheck_PassesWhenBothVendorsHoldTheContact(t *testing.T) {
	w := newWorld()
	w.hsMiss, w.rsMiss = 1, 1
	res := runPhase(t, w, baseEnv(), "check")
	if res.code != 0 || !slices.Equal(res.names(), []string{stRegistered, stRegInHS, stRegInRS, stDemoSent, stDemoDeal}) {
		t.Fatalf("code %d, steps %+v", res.code, res.sec.Steps)
	}
	for _, s := range res.sec.Steps {
		if s.Status != "passed" {
			t.Errorf("%+v", s)
		}
	}
	if len(res.sleeps) != 2 {
		t.Errorf("sleeps = %v, want one pause for each vendor", res.sleeps)
	}
}

func TestCheck_NamesTheMissingVendor(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*world)
		fail string
	}{
		"absent from HubSpot": {func(w *world) { w.noHS = true }, stRegInHS},
		"absent from Resend":  {func(w *world) { w.noRS = true }, stRegInRS},
		"no registered tag":   {func(w *world) { w.noTag = true }, stRegInHS},
		"no deal":             {func(w *world) { w.noDeal = true }, stDemoDeal},
		"wrong stage":         {func(w *world) { w.dealStage = "elsewhere" }, stDemoDeal},
		"wrong name":          {func(w *world) { w.dealName = "Nightly — sign-up" }, stDemoDeal},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			tc.set(w)
			res := runPhase(t, w, baseEnv(), "check")
			if res.code != 1 {
				t.Fatalf("code %d", res.code)
			}
			for _, s := range res.sec.Steps {
				want := "passed"
				if s.Name == tc.fail {
					want = "failed"
				}
				if s.Status != want {
					t.Errorf("%s = %s (%s), want %s", s.Name, s.Status, s.Detail, want)
				}
			}
			if d := res.step(tc.fail).Detail; !strings.Contains(d, "after 3m0s") {
				t.Errorf("detail %q does not name the limit", d)
			}
		})
	}
}

func TestCheck_RefusedRegistrationSkipsItsReadBacks(t *testing.T) {
	w := newWorld()
	w.registerStatus = 500
	res := runPhase(t, w, baseEnv(), "check")
	got := map[string]string{}
	for _, s := range res.sec.Steps {
		got[s.Name] = s.Status
	}
	want := map[string]string{stRegistered: "failed", stRegInHS: "skipped", stRegInRS: "skipped", stDemoSent: "passed", stDemoDeal: "passed"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if w.bodyOf("/auth/sign-in") != "" {
		t.Error("sign-in was called after a refused registration")
	}

	w = newWorld()
	w.demoStatus = 500
	res = runPhase(t, w, baseEnv(), "check")
	if res.step(stDemoSent).Status != "failed" || res.step(stDemoDeal).Status != "skipped" || res.step(stRegInHS).Status != "passed" {
		t.Errorf("demo refused: steps %+v", res.sec.Steps)
	}
}

func TestCheck_AddressesCarryTheRunID(t *testing.T) {
	w := newWorld()
	runPhase(t, w, baseEnv(), "check")
	if b := w.bodyOf("/auth/register"); !strings.Contains(b, `"email":"`+regAddr+`"`) {
		t.Errorf("register body lacks the run address")
	}
	if b := w.bodyOf("/contacts/demo-request"); !strings.Contains(b, `"email":"`+demoAddr+`"`) {
		t.Errorf("demo body lacks the run address")
	}
	var si map[string]string
	_ = json.Unmarshal([]byte(w.bodyOf("/auth/sign-in")), &si)
	if len(si["state"]) != 43 {
		t.Errorf("state length = %d, want 43", len(si["state"]))
	}

	w = newWorld()
	env := baseEnv()
	env["NIGHTLY_RUN_ID"] = ""
	res := runPhase(t, w, env, "check")
	if res.code != 2 || len(w.calls) != 0 {
		t.Errorf("empty run id: code %d, calls %d", res.code, len(w.calls))
	}
}

func TestFinish_ReportsWhatIsLeft(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*world)
		want string
	}{
		"contact remains": {func(w *world) {
			w.noopDeletes = true
			w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
		}, "failed"},
		"resend contact remains": {func(w *world) {
			w.noopDeletes = true
			w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
		}, "failed"},
		"sign-up deal survives its delete": {func(w *world) {
			w.noopDeletes = true
			w.hsDeals = []hsDealRow{{"d1", "Acme — sign-up", testStage, "none"}}
		}, "failed"},
		"contactless sign-up deal is deleted": {func(w *world) { w.hsDeals = []hsDealRow{{"d1", "Acme — sign-up", testStage, "none"}} }, "passed"},
		"other deal remains":                  {func(w *world) { w.hsDeals = []hsDealRow{{"d1", "Acme — other", testStage, "none"}} }, "passed"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			tc.set(w)
			res := finish(t, w, baseEnv())
			if got := res.step(stNoneLeft).Status; got != tc.want {
				t.Errorf("%s = %s (%s), want %s", stNoneLeft, got, res.step(stNoneLeft).Detail, tc.want)
			}
			if name == "other deal remains" && (len(w.hsDeals) != 1 || len(dealCalls(w, "DELETE")) != 0) {
				t.Errorf("a deal outside the rule was touched: %v", w.hsDeals)
			}
			if d := res.step(stNoneLeft).Detail; tc.want == "failed" && !strings.Contains(d, "after 2m0s") {
				t.Errorf("detail %q does not name the limit", d)
			}
		})
	}
}

func TestFinish_NewContactOutsideTheRule(t *testing.T) {
	for name, tc := range map[string]struct{ created, want string }{
		"new":          {"2026-10-31T03:00:00Z", "failed"},
		"old":          {"2026-10-30T03:00:00Z", "passed"},
		"at the start": {startedAt, "failed"},
		"unreadable":   {"yesterday", "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			w.rsContacts = []rsContactRow{{"r1", "stranger@acme.com", tc.created}}
			res := finish(t, w, baseEnv())
			s := res.step(stNoOther)
			if s.Status != tc.want {
				t.Fatalf("%+v, want %s", s, tc.want)
			}
			if tc.want == "failed" && !strings.Contains(s.Detail, "read the addresses in Resend") {
				t.Errorf("detail %q", s.Detail)
			}
			if len(w.deleted) != 0 {
				t.Errorf("deleted a non-matching contact: %v", w.deleted)
			}
		})
	}
}

func TestFinish_DeleteFailureIsAFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		phase, step string
		set         func(*world)
	}{
		"finish Resend":  {"finish", stDelRS, func(w *world) { w.rsDeleteStatus = 500 }},
		"finish HubSpot": {"finish", stDelHS, func(w *world) { w.hsDeleteStatus = 500 }},
		"start Resend":   {"start", stLeftovers, func(w *world) { w.rsDeleteStatus = 500 }},
		"start HubSpot":  {"start", stLeftovers, func(w *world) { w.hsDeleteStatus = 500 }},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			tc.set(w)
			w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
			w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
			var res result
			if tc.phase == "finish" {
				res = finish(t, w, baseEnv())
			} else {
				res = greenStart(t, w)
			}
			if res.code != 1 || res.step(tc.step).Status != "failed" || res.sec.OK {
				t.Fatalf("code %d, ok %v, step %+v", res.code, res.sec.OK, res.step(tc.step))
			}
			if len(res.sec.Failures) == 0 || res.sec.Failures[0].Suite != "hubspot" {
				t.Errorf("failures = %+v", res.sec.Failures)
			}
		})
	}
}

func TestSweep_ListFailureDeletesNothing(t *testing.T) {
	for name, set := range map[string]func(*world){
		"HubSpot page 2": func(w *world) { w.hsListFail = true },
		"Resend page 2":  func(w *world) { w.rsListFail = true },
	} {
		for _, phase := range []string{"start", "finish"} {
			t.Run(name+" "+phase, func(t *testing.T) {
				w := newWorld()
				w.pageSize = 1
				set(w)
				w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}, {"2", "b@example.com", ""}}
				w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}, {"r2", "b@example.com", "2026-01-01T00:00:00Z"}}
				var res result
				if phase == "finish" {
					res = finish(t, w, baseEnv())
				} else {
					res = greenStart(t, w)
				}
				if res.code != 1 {
					t.Fatalf("code %d, steps %+v", res.code, res.sec.Steps)
				}
				failed := stLeftovers
				if phase == "finish" {
					failed = stDelRS
					if w.hsListFail {
						failed = stDelHS
					}
				}
				if res.step(failed).Status != "failed" {
					t.Errorf("%s = %+v, want failed", failed, res.step(failed))
				}
				// A vendor whose list is cut short deletes nothing; the other vendor's list is whole.
				for _, d := range w.deleted {
					cut := strings.HasPrefix(d, "hs:") == w.hsListFail
					if phase == "start" || cut {
						t.Errorf("%s deleted %s after a cut list", phase, d)
					}
				}
			})
		}
	}
}

func healthyRun(t *testing.T) (w *world, final result) {
	t.Helper()
	w = newWorld()
	dir := t.TempDir()
	for _, phase := range []string{"start", "check"} {
		res := runPhase(t, w, baseEnv(), phase, "-out", filepath.Join(dir, phase+".json"))
		if res.code != 0 {
			t.Fatalf("%s: code %d, steps %+v", phase, res.code, res.sec.Steps)
		}
	}
	final = runPhase(t, w, baseEnv(), "finish", "-in", dir, "-started", startedAt)
	return w, final
}

func TestSection_ShapeAndOrder(t *testing.T) {
	_, res := healthyRun(t)
	if res.code != 0 || !res.sec.OK || len(res.sec.Failures) != 0 {
		t.Fatalf("code %d, section %+v", res.code, res.sec)
	}
	wantOrder := []string{
		"portal is the test account", "test portal is configured", "leftovers deleted", "registration accepted",
		"registrant in HubSpot", "registrant in Resend", "demo request accepted", "demo deal in HubSpot",
		"run contacts deleted from HubSpot", "run contacts deleted from Resend", "no run contact or deal left", "no other new Resend contact",
	}
	if !slices.Equal(res.names(), wantOrder) {
		t.Errorf("steps = %v", res.names())
	}
	if !strings.Contains(res.raw, `"failures": []`) {
		t.Errorf("a green section must hold failures as an empty array: %s", res.raw)
	}
	for _, s := range res.sec.Steps {
		if s.Status != "passed" {
			t.Errorf("%+v", s)
		}
	}
	if strings.Contains(res.stdout+res.raw, "@") {
		t.Error("output holds an address")
	}
}

func TestSection_TheAssemblerReadsIt(t *testing.T) {
	_, res := healthyRun(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	section := write("hubspot.json", res.raw)
	out := filepath.Join(dir, "summary.json")
	cmd := exec.Command("go", "run", "../nightlysummary",
		"-suites", write("suites.txt", "unit=Unit\n"), "-jobs", write("jobs.json", "[]"),
		"-results", filepath.Join(dir, "results"), "-out", out, "-section", "hubspot="+section)
	_ = cmd.Run() // red for the unit suite it cannot find; only the hubspot section matters
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("no summary: %v", err)
	}
	var sum struct {
		Failures []struct{ Suite, Test string }
		Hubspot  struct{ OK bool }
	}
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	for _, f := range sum.Failures {
		if f.Suite == "hubspot" {
			t.Errorf("assembler failure: %+v", f)
		}
	}
	if !sum.Hubspot.OK {
		t.Error("summary holds no green hubspot section")
	}
}

func TestFinish_MissingPhaseFileIsAFailedStep(t *testing.T) {
	w := newWorld()
	w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
	res := runPhase(t, w, baseEnv(), "finish", "-in", t.TempDir(), "-started", startedAt)
	if res.code != 1 || res.step("check phase ran").Status != "failed" || res.step("start phase ran").Status != "failed" {
		t.Fatalf("code %d, steps %+v", res.code, res.sec.Steps)
	}
	if !slices.Equal(w.deleted, []string{"rs:a@example.com"}) {
		t.Errorf("cleanup did not run: %v", w.deleted)
	}
}

func TestOutput_HoldsNoAddressOrToken(t *testing.T) {
	for name, set := range map[string]func(*world){
		"bodies echo secrets":                    func(w *world) { w.failAll = true },
		"errors hold the URL":                    func(w *world) { w.netError = true },
		"gateway refuses and echoes the address": func(w *world) { w.registerStatus, w.demoStatus = 500, 500 },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			set(w)
			dir := t.TempDir()
			var all string
			for _, args := range [][]string{
				{"start"}, {"check"}, {"finish", "-in", dir, "-started", startedAt},
			} {
				res := runPhase(t, w, baseEnv(), args...)
				if res.code == 2 {
					t.Errorf("%v: the phase did not run", args)
				}
				all += res.stdout + res.stderr + res.raw
			}
			for _, secret := range []string{testToken, testKey, "nightly-77-2", "@", "gw.test"} {
				if strings.Contains(all, secret) {
					t.Errorf("output holds %q", secret)
				}
			}
		})
	}
}

type scripted struct {
	statuses []int
	n        int
}

func (s *scripted) RoundTrip(*http.Request) (*http.Response, error) {
	st := s.statuses[min(s.n, len(s.statuses)-1)]
	s.n++
	return reply(st, `{}`), nil
}

func TestRetry_WaitsOn429ThenGivesUp(t *testing.T) {
	for name, tc := range map[string]struct {
		statuses []int
		calls    int
		sleeps   int
		want     int
	}{
		"429 twice then 200": {[]int{429, 429, 200}, 3, 2, 200},
		"429 always":         {[]int{429}, 5, 4, 429},
	} {
		t.Run(name, func(t *testing.T) {
			tr := &scripted{statuses: tc.statuses}
			var sleeps []time.Duration
			v := &vendors{hc: &http.Client{Transport: tr}, sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
			status, _ := v.do("GET", "https://api.resend.com/contacts", "k", nil)
			if status != tc.want || tr.n != tc.calls || len(sleeps) != tc.sleeps {
				t.Errorf("status %d, calls %d, sleeps %v", status, tr.n, sleeps)
			}
			for _, d := range sleeps {
				if d != 2*time.Second {
					t.Errorf("slept %v, want 2s", d)
				}
			}
		})
	}

	w := newWorld()
	w.portalStatus = 429
	res := greenStart(t, w)
	if res.step(stPortal).Status != "failed" || w.count("api.hubapi.com") != 5 {
		t.Errorf("portal step %+v after %d calls", res.step(stPortal), w.count("api.hubapi.com"))
	}
}

func TestReceivingDomain_RefusedBeforeAnyVendorCall(t *testing.T) {
	domains := []string{"", "@x.resend.app", "ascomply.com", "resend.app", "x.resend.app.evil.ng", "a.b.resend.app", "X.resend.app", "x.resend.app\n"}
	for _, d := range domains {
		for _, phase := range []string{"start", "check", "finish"} {
			t.Run(phase+" "+strings.TrimSpace(d), func(t *testing.T) {
				w := newWorld()
				env := baseEnv()
				env["RECEIVING_DOMAIN"] = d
				args := []string{phase}
				if phase == "finish" {
					dir := t.TempDir()
					writePhases(t, dir)
					args = append(args, "-in", dir, "-started", startedAt)
				}
				res := runPhase(t, w, env, args...)
				if res.code != 1 || len(w.calls) != 0 || res.step(stDomain).Status != "failed" {
					t.Fatalf("code %d, calls %d, steps %+v", res.code, len(w.calls), res.sec.Steps)
				}
				if v := strings.TrimSpace(d); v != "" && v != "resend.app" && strings.Contains(res.stdout+res.raw, v) {
					t.Errorf("output holds the value")
				}
			})
		}
	}
}

func TestReceivingDomain_AcceptsAResendSubdomain(t *testing.T) {
	w := newWorld()
	w.rsContacts = []rsContactRow{{"r1", "nightly-1-reg@abc123.resend.app", "2026-01-01T00:00:00Z"}}
	env := baseEnv()
	env["RECEIVING_DOMAIN"] = "abc123.resend.app"
	res := runPhase(t, w, env, "start")
	if res.step(stDomain).Status != "absent" || !slices.Equal(w.deleted, []string{"rs:nightly-1-reg@abc123.resend.app"}) {
		t.Errorf("steps %+v, deleted %v", res.sec.Steps, w.deleted)
	}
}

// dealCalls lists the deal calls of a method, in order.
func dealCalls(w *world, method string) (idx []int) {
	for i, c := range w.calls {
		if c.method == method && strings.HasPrefix(c.path, "/crm/v3/objects/deals/") {
			idx = append(idx, i)
		}
	}
	return
}

func TestSweep_DeletesARunDealWithNoContact(t *testing.T) {
	for _, phase := range []string{"start", "finish"} {
		t.Run(phase, func(t *testing.T) {
			w := newWorld()
			w.pageSize = 1
			w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
			w.hsDeals = []hsDealRow{
				{"d1", "Acme — sign-up", testStage, "none"},
				{"d2", "Acme — other", testStage, "none"},
				{"d3", "Acme — demo request", testStage, "none"},
				{"d4", "Erased contact", testStage, "none"},
			}
			want, got := "HubSpot 4 deleted, 0 failed", ""
			if phase == "start" {
				got = greenStart(t, w).step(stLeftovers).Detail
			} else {
				got, want = finish(t, w, baseEnv()).step(stDelHS).Detail, "4 deleted, 0 failed"
			}
			if !strings.HasPrefix(got, want) {
				t.Errorf("sweep detail %q, want it to start %q", got, want)
			}
			var left []string
			for _, d := range w.hsDeals {
				left = append(left, d.id)
			}
			if !slices.Equal(left, []string{"d2"}) {
				t.Fatalf("deals left %v, want only d2 (Acme — other)", left)
			}
			dels := dealCalls(w, "DELETE")
			if len(dels) != 3 {
				t.Fatalf("%d deal deletes, want 3", len(dels))
			}
			// every deal list page comes before the first deal delete, and the contact sweep before both
			lastList, firstGdpr := 0, -1
			for i, c := range w.calls {
				if c.method == "GET" && strings.HasPrefix(c.path, "/crm/v3/objects/deals?") && i < dels[0] {
					lastList = i
				}
				if c.method == "POST" && strings.Contains(c.path, "gdpr-delete") && firstGdpr < 0 {
					firstGdpr = i
				}
			}
			pages := 0
			for _, c := range w.calls[:dels[0]] {
				if c.method == "GET" && strings.HasPrefix(c.path, "/crm/v3/objects/deals?") {
					pages++
				}
			}
			if pages != 4 || lastList > dels[0] || firstGdpr > lastList {
				t.Errorf("deal list pages before the first delete = %d (want 4), gdpr at %d, last list at %d", pages, firstGdpr, lastList)
			}
		})
	}
}

func TestSweep_LivePortalDeletesNoDeal(t *testing.T) {
	w := newWorld()
	w.portalBody = `{"portalId":148915098}`
	w.hsDeals = []hsDealRow{{"d1", "Acme — sign-up", testStage, "none"}}
	finish(t, w, baseEnv())
	if len(w.hsDeals) != 1 || len(dealCalls(w, "GET"))+len(dealCalls(w, "DELETE")) != 0 {
		t.Errorf("deals %v, calls %v", w.hsDeals, w.calls)
	}
}

func TestSweep_DealDeleteFailureIsAFailure(t *testing.T) {
	for phase, step := range map[string]string{"start": stLeftovers, "finish": stDelHS} {
		t.Run(phase, func(t *testing.T) {
			w := newWorld()
			w.hsDeleteStatus = 500
			w.hsDeals = []hsDealRow{{"d1", "Acme — sign-up", testStage, "none"}}
			var res result
			if phase == "start" {
				res = greenStart(t, w)
			} else {
				res = finish(t, w, baseEnv())
			}
			if res.code != 1 || res.step(step).Status != "failed" || len(w.hsDeals) != 1 {
				t.Errorf("code %d, step %+v", res.code, res.step(step))
			}
		})
	}
}

func TestFinish_RepeatsADeleteThatFailed(t *testing.T) {
	w := newWorld()
	w.hsFailFirst, w.rsFailFirst, w.dealFailFirst = 1, 1, 1
	w.hsDeals = []hsDealRow{{"d1", "Acme — sign-up", testStage, "none"}}
	w.hsContacts = []hsContactRow{{"1", "a@example.com", ""}}
	w.rsContacts = []rsContactRow{{"r1", "a@example.com", "2026-01-01T00:00:00Z"}}
	res := finish(t, w, baseEnv())
	if got := res.step(stNoneLeft); got.Status != "passed" {
		t.Errorf("%s = %+v, want a repeat to delete what the first delete missed", stNoneLeft, got)
	}
	if res.step(stDelHS).Status != "failed" || res.code != 1 {
		t.Errorf("the failed first delete must still show: code %d, %+v", res.code, res.step(stDelHS))
	}
	if len(w.hsContacts)+len(w.rsContacts)+len(w.hsDeals) != 0 {
		t.Errorf("left: %v %v", w.hsContacts, w.rsContacts)
	}
}

func TestRetry_ReadsRetryServerErrorsAndNoAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		statuses []int
		calls    int
		want     int
	}{
		"503 then 200":   {[]int{503, 200}, 2, 200},
		"500 then 200":   {[]int{500, 200}, 2, 200},
		"no answer once": {[]int{0, 200}, 2, 200},
		"503 always":     {[]int{503}, 5, 503},
		"401 is final":   {[]int{401, 200}, 1, 401},
		"404 is final":   {[]int{404, 200}, 1, 404},
	} {
		t.Run(name, func(t *testing.T) {
			tr := &scripted{statuses: tc.statuses}
			v := &vendors{hc: &http.Client{Transport: tr}, sleep: func(time.Duration) {}}
			if status, _ := v.do("GET", "https://api.hubapi.com/x", "k", nil); status != tc.want || tr.n != tc.calls {
				t.Errorf("status %d after %d calls, want %d after %d", status, tr.n, tc.want, tc.calls)
			}
		})
	}

	w := newWorld()
	w.portalStatus = 503
	if res := greenStart(t, w); res.step(stPortal).Status != "failed" || w.count("api.hubapi.com") != 5 {
		t.Errorf("a portal read that keeps failing: %+v after %d calls", res.step(stPortal), w.count("api.hubapi.com"))
	}
}

// deadlines records the time each call had left.
type deadlines struct {
	scripted
	left []time.Duration
}

func (d *deadlines) RoundTrip(r *http.Request) (*http.Response, error) {
	if dl, ok := r.Context().Deadline(); ok {
		d.left = append(d.left, time.Until(dl))
	}
	if d.statuses[min(d.n, len(d.statuses)-1)] == 0 {
		d.n++
		return nil, io.ErrUnexpectedEOF
	}
	return d.scripted.RoundTrip(r)
}

func TestPost_RetriesAColdGatewayWithALongTimeout(t *testing.T) {
	for name, tc := range map[string]struct {
		statuses []int
		calls    int
		want     int
	}{
		"502 then 202":    {[]int{502, 202}, 2, 202},
		"503 then 202":    {[]int{503, 202}, 2, 202},
		"504 then 202":    {[]int{504, 202}, 2, 202},
		"no answer":       {[]int{0, 202}, 2, 202},
		"502 always":      {[]int{502}, 3, 502},
		"500 is final":    {[]int{500, 202}, 1, 500},
		"400 is final":    {[]int{400, 202}, 1, 400},
		"429 is final":    {[]int{429, 202}, 1, 429},
		"202 first try":   {[]int{202}, 1, 202},
		"401 is final":    {[]int{401, 202}, 1, 401},
		"501 is final":    {[]int{501, 202}, 1, 501},
		"403 is final":    {[]int{403, 202}, 1, 403},
		"404 is final":    {[]int{404, 202}, 1, 404},
		"409 is final":    {[]int{409, 202}, 1, 409},
		"422 is final":    {[]int{422, 202}, 1, 422},
		"408 is final":    {[]int{408, 202}, 1, 408},
		"505 is final":    {[]int{505, 202}, 1, 505},
		"200 first try":   {[]int{200}, 1, 200},
		"502 then 500":    {[]int{502, 500, 202}, 2, 500},
		"0 then 0 then 0": {[]int{0}, 3, 0},
	} {
		t.Run(name, func(t *testing.T) {
			tr := &deadlines{scripted: scripted{statuses: tc.statuses}}
			v := &vendors{hc: notifications.NewHTTPClient(tr), sleep: func(time.Duration) {}}
			if status := v.post("https://gw.test/auth/register", map[string]string{"a": "b"}); status != tc.want || tr.n != tc.calls {
				t.Errorf("status %d after %d calls, want %d after %d", status, tr.n, tc.want, tc.calls)
			}
			for _, l := range tr.left {
				if l < 35*time.Second {
					t.Errorf("a call had %v left, want about 40s", l)
				}
			}
		})
	}
}
