package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The shim logs argv to $D/calls, serves $D/describe-<svc>.json, and fails describe or update-traffic for services named in $D/fail-describe or $D/fail-update.
const gcpTrafficShim = `echo "$*" >> "$D/calls"
case "$3" in
describe) grep -qx "$4" "$D/fail-describe" 2>/dev/null && exit 1; cat "$D/describe-$4.json" ;;
update-traffic) grep -qx "$4" "$D/fail-update" 2>/dev/null && exit 1; exit 0 ;;
esac
`

var gcpTrafficServices = []string{"gateway", "auth", "tenancy", "portfolio", "invoice", "validation", "dashboard", "submission",
	"notifications", "worker", "docling", "app", "landing", "library", "blog", "check", "ops-console", "support-console"}

// describeJSON: tagged (if non-empty) is the tagged revision; the serving one holds 100 %; a newer revision is latest.
func describeJSON(svc, tag string) string {
	tagged := ""
	if tag != "" {
		tagged = fmt.Sprintf(`,{"revisionName":"%s-00411-hij","tag":"%s"}`, svc, tag)
	}
	return fmt.Sprintf(`{"spec":{"traffic":[{"revisionName":"%s-00100-old","percent":100}%s]},`+
		`"status":{"latestCreatedRevisionName":"%s-00155-hxm","latestReadyRevisionName":"%s-00155-hxm"}}`, svc, tagged, svc, svc)
}

// runGcpTraffic runs gcp-traffic.sh with the shim; setup may add files to the scratch dir.
func runGcpTraffic(t *testing.T, setup func(dir string), args ...string) (code int, calls []string, out string) {
	t.Helper()
	dir, shims := t.TempDir(), t.TempDir()
	setup(dir)
	writeShim(t, filepath.Join(shims, "gcloud"), "D='"+dir+"'\n"+gcpTrafficShim)
	for i, a := range args {
		if a == "SNAP" {
			args[i] = filepath.Join(dir, "snap")
		}
	}
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts/ci/gcp-traffic.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PROJECT=p", "REGION=r", "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := cmd.CombinedOutput()
	out = string(b)
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "calls")); err == nil {
		calls = strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	return code, calls, out
}

func updateTrafficCalls(calls []string, svc string) []string {
	var got []string
	for _, c := range calls {
		if strings.HasPrefix(c, "run services update-traffic "+svc+" ") {
			got = append(got, c)
		}
	}
	return got
}

func writeSnapshot(t *testing.T, dir string, svcs []string) {
	t.Helper()
	var b strings.Builder
	for _, s := range svcs {
		fmt.Fprintf(&b, "%s %s-00100-old\n", s, s)
	}
	writeIn(t, dir, "snap", b.String())
}

func TestGcpTrafficPromoteSendsTrafficToTheTaggedRevision(t *testing.T) {
	svcs := []string{"gateway", "auth", "support-console"}
	code, calls, out := runGcpTraffic(t, func(dir string) {
		for _, s := range svcs {
			writeIn(t, dir, "describe-"+s+".json", describeJSON(s, "c-abc123"))
		}
		writeSnapshot(t, dir, svcs)
	}, "promote", "SNAP", "c-abc123")
	if code != 0 {
		t.Fatalf("promote exited %d: %s", code, out)
	}
	for _, s := range svcs {
		ups := updateTrafficCalls(calls, s)
		if len(ups) != 1 {
			t.Fatalf("%s: want one update-traffic call, got %v", s, ups)
		}
		u := ups[0]
		if strings.Contains(u, "--to-latest") {
			t.Errorf("%s: promote sent traffic to latest: %s", s, u)
		}
		if !strings.Contains(u, "--to-revisions "+s+"-00411-hij=100") {
			t.Errorf("%s: want --to-revisions %s-00411-hij=100 (the tagged revision), got: %s", s, s, u)
		}
		if !strings.Contains(u, "--update-tags rollback="+s+"-00100-old") {
			t.Errorf("%s: want --update-tags rollback=%s-00100-old, got: %s", s, s, u)
		}
	}
}

func TestGcpTrafficPromoteMovesNothingWhenAServiceHasNoTaggedRevision(t *testing.T) {
	svcs := []string{"gateway", "auth", "support-console"}
	code, calls, out := runGcpTraffic(t, func(dir string) {
		writeIn(t, dir, "describe-gateway.json", describeJSON("gateway", "c-abc123"))
		writeIn(t, dir, "describe-auth.json", describeJSON("auth", ""))
		writeIn(t, dir, "describe-support-console.json", describeJSON("support-console", "c-abc123"))
		writeSnapshot(t, dir, svcs)
	}, "promote", "SNAP", "c-abc123")
	if code == 0 {
		t.Errorf("promote exited 0 although auth has no revision tagged c-abc123: %s", out)
	}
	for _, s := range svcs {
		if ups := updateTrafficCalls(calls, s); len(ups) != 0 {
			t.Errorf("%s: traffic moved although a service has no tagged revision: %v", s, ups)
		}
	}
}

func TestGcpTrafficUntagTriesEveryServiceAndFailsAtTheEnd(t *testing.T) {
	code, calls, out := runGcpTraffic(t, func(dir string) {
		for _, s := range gcpTrafficServices {
			writeIn(t, dir, "describe-"+s+".json", describeJSON(s, "c-abc123"))
		}
		writeIn(t, dir, "fail-update", "ops-console\n")
	}, "untag")
	if code == 0 {
		t.Errorf("untag exited 0 although ops-console failed to untag: %s", out)
	}
	for _, s := range gcpTrafficServices {
		ups := updateTrafficCalls(calls, s)
		if len(ups) != 1 || !strings.Contains(ups[0], "--remove-tags c-abc123") {
			t.Errorf("%s: want one --remove-tags c-abc123 call, got %v", s, ups)
		}
	}
}

func allUpdateTrafficCalls(calls []string) []string {
	var got []string
	for _, c := range calls {
		if strings.HasPrefix(c, "run services update-traffic ") {
			got = append(got, c)
		}
	}
	return got
}

func TestGcpTrafficPromoteWithoutATagMovesNothing(t *testing.T) {
	svcs := []string{"gateway", "auth"}
	code, calls, out := runGcpTraffic(t, func(dir string) {
		for _, s := range svcs {
			writeIn(t, dir, "describe-"+s+".json", describeJSON(s, "c-abc123"))
		}
		writeSnapshot(t, dir, svcs)
	}, "promote", "SNAP")
	if code == 0 {
		t.Errorf("promote without a tag exited 0: %s", out)
	}
	if ups := allUpdateTrafficCalls(calls); len(ups) != 0 {
		t.Errorf("traffic moved without a tag: %v", ups)
	}
}

func TestGcpTrafficPromoteMovesNothingWhenTheTagLookupFails(t *testing.T) {
	svcs := []string{"gateway", "auth", "support-console"}
	code, calls, out := runGcpTraffic(t, func(dir string) {
		for _, s := range svcs {
			writeIn(t, dir, "describe-"+s+".json", describeJSON(s, "c-abc123"))
		}
		writeIn(t, dir, "fail-describe", "support-console\n")
		writeSnapshot(t, dir, svcs)
	}, "promote", "SNAP", "c-abc123")
	if code == 0 {
		t.Errorf("promote exited 0 although describe failed for support-console: %s", out)
	}
	if len(calls) == 0 {
		t.Fatal("no gcloud calls recorded")
	}
	if ups := allUpdateTrafficCalls(calls); len(ups) != 0 {
		t.Errorf("traffic moved although a tag lookup failed: %v", ups)
	}
}

func TestGcpTrafficPromoteFindsATaggedRevisionThatIsNotReady(t *testing.T) {
	// status.traffic omits the tagged revision (never ready); only spec.traffic carries it.
	code, calls, out := runGcpTraffic(t, func(dir string) {
		writeIn(t, dir, "describe-gateway.json", `{"spec":{"traffic":[{"revisionName":"gateway-00100-old","percent":100},`+
			`{"revisionName":"gateway-00411-hij","tag":"c-abc123"}]},`+
			`"status":{"traffic":[{"revisionName":"gateway-00100-old","percent":100}],"latestReadyRevisionName":"gateway-00100-old"}}`)
		writeSnapshot(t, dir, []string{"gateway"})
	}, "promote", "SNAP", "c-abc123")
	if code != 0 {
		t.Fatalf("promote exited %d: %s", code, out)
	}
	ups := updateTrafficCalls(calls, "gateway")
	if len(ups) != 1 || !strings.Contains(ups[0], "--to-revisions gateway-00411-hij=100") {
		t.Errorf("want one call to gateway-00411-hij, got %v", ups)
	}
}

func TestGcpTrafficPromoteIgnoresAnotherRunsTag(t *testing.T) {
	code, calls, out := runGcpTraffic(t, func(dir string) {
		writeIn(t, dir, "describe-gateway.json", describeJSON("gateway", "c-other999"))
		writeSnapshot(t, dir, []string{"gateway"})
	}, "promote", "SNAP", "c-abc123")
	if code == 0 {
		t.Errorf("promote exited 0 with only another run's tag present: %s", out)
	}
	if ups := allUpdateTrafficCalls(calls); len(ups) != 0 {
		t.Errorf("traffic moved: %v", ups)
	}
}

func TestGcpTrafficUntagContinuesWhenDescribeFailsForOneService(t *testing.T) {
	code, calls, out := runGcpTraffic(t, func(dir string) {
		for _, s := range gcpTrafficServices {
			writeIn(t, dir, "describe-"+s+".json", describeJSON(s, "c-abc123"))
		}
		writeIn(t, dir, "fail-describe", "docling\n")
	}, "untag")
	if code == 0 {
		t.Errorf("untag exited 0 although describe failed for docling: %s", out)
	}
	if ups := updateTrafficCalls(calls, "docling"); len(ups) != 0 {
		t.Errorf("docling: untag called update-traffic without reading its tags: %v", ups)
	}
	for _, s := range gcpTrafficServices {
		if s == "docling" {
			continue
		}
		if ups := updateTrafficCalls(calls, s); len(ups) != 1 {
			t.Errorf("%s: want one --remove-tags call, got %v", s, ups)
		}
	}
}

func TestGcpTrafficUntagSucceedsAndLeavesOtherTags(t *testing.T) {
	code, calls, out := runGcpTraffic(t, func(dir string) {
		for _, s := range gcpTrafficServices {
			writeIn(t, dir, "describe-"+s+".json", describeJSON(s, "c-abc123"))
		}
		// auth carries only the rollback tag.
		writeIn(t, dir, "describe-auth.json", describeJSON("auth", "rollback"))
	}, "untag")
	if code != 0 {
		t.Fatalf("untag exited %d: %s", code, out)
	}
	if ups := updateTrafficCalls(calls, "auth"); len(ups) != 0 {
		t.Errorf("auth: untag touched a non-c- tag: %v", ups)
	}
	if ups := updateTrafficCalls(calls, "gateway"); len(ups) != 1 || !strings.Contains(ups[0], "--remove-tags c-abc123") {
		t.Errorf("gateway: want one --remove-tags c-abc123 call, got %v", ups)
	}
}
