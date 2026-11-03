package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const fake = "7654321"

func fakeNeedles() []needle {
	return []needle{{"test id", 7, sum(fake)}}
}

func TestScan_FindsTheValueAsASubstring(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"x7654321x", "987654321", "76543219", "7654321", "a\n7654321"} {
		got := scan("f", []byte(text), fakeNeedles())
		wantLine := 1 + strings.Count(text, "\n")
		if len(got) != 1 || got[0].line != wantLine || got[0].kind != "test id" {
			t.Errorf("%q: got %+v, want one finding on line %d", text, got, wantLine)
		}
	}
}

func TestScan_ReadsBinaryBytes(t *testing.T) {
	t.Parallel()
	if got := scan("f", []byte("\x00\x01PK7654321\x00"), fakeNeedles()); len(got) != 1 {
		t.Errorf("got %+v, want 1 finding", got)
	}
}

func TestScan_IgnoresNearMisses(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"765432", "765\n4321", "7654320", "7654-321"} {
		if got := scan("f", []byte(text), fakeNeedles()); len(got) != 0 {
			t.Errorf("%q: got %+v, want none", text, got)
		}
	}
}

func TestScan_CountsLines(t *testing.T) {
	t.Parallel()
	got := scan("f", []byte("a\r\nb\n"+fake+"\n"+fake), fakeNeedles())
	if len(got) != 2 || got[0].line != 3 || got[1].line != 4 {
		t.Errorf("got %+v, want findings on lines 3 and 4", got)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitAdd(t *testing.T, dir string, names ...string) {
	t.Helper()
	args := append([]string{"-C", dir, "add"}, names...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
}

func TestRun_ReportsFileLineAndKindNeverTheValue(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	write(t, dir, "a/b.txt", "one\nid "+fake+"\n")
	gitAdd(t, dir, "a/b.txt")
	var out, errb bytes.Buffer
	if code := run(dir, fakeNeedles(), &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if want := "::error file=a/b.txt,line=2::a/b.txt contains the test id\n"; out.String() != want {
		t.Errorf("stdout %q, want %q", out.String(), want)
	}
	if strings.Contains(out.String()+errb.String(), fake) {
		t.Error("output contains the value")
	}
}

func TestRun_ScansOnlyTrackedRegularFiles(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	write(t, dir, "clean.txt", "nothing\n")
	write(t, dir, "untracked.txt", fake)
	if err := os.Symlink("untracked.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	gitAdd(t, dir, "clean.txt", "link")
	var out, errb bytes.Buffer
	if code := run(dir, fakeNeedles(), &out, &errb); code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
	if want := "prodidgate: no production id in 1 tracked files\n"; out.String() != want {
		t.Errorf("stdout %q, want %q", out.String(), want)
	}
}

func TestRun_UnreadableTrackedFileIsAnError(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	write(t, dir, "ok.txt", "x") // a read file, so the empty-tree floor cannot answer for the error
	write(t, dir, "gone.txt", "x")
	gitAdd(t, dir, "ok.txt", "gone.txt")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run(dir, fakeNeedles(), &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "gone.txt") {
		t.Errorf("stderr %q does not name the file", errb.String())
	}
}

func TestRun_UnlistableOrUnopenableTreeIsAnError(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := run(t.TempDir(), fakeNeedles(), &out, &errb); code != 2 {
		t.Errorf("not a git repo: exit %d, want 2", code)
	}
	if os.Geteuid() == 0 {
		return
	}
	dir := gitRepo(t)
	write(t, dir, "ok.txt", "x") // a read file, so the empty-tree floor cannot answer for the error
	write(t, dir, "locked.txt", "x")
	gitAdd(t, dir, "ok.txt", "locked.txt")
	if err := os.Chmod(filepath.Join(dir, "locked.txt"), 0); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := run(dir, fakeNeedles(), &out, &errb); code != 2 || !strings.Contains(errb.String(), "locked.txt") {
		t.Errorf("unreadable file: exit %d, stderr %q, want 2 naming the file", code, errb.String())
	}
}

func selfNeedles() []needle {
	return []needle{
		{"GCP prod project number", 12, sum("111111111111")},
		{"HubSpot portal id", 9, sum("222222222")},
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSelfcheck_MatchesAndMismatches(t *testing.T) {
	t.Parallel()
	provider := "projects/111111111111/locations/global/workloadIdentityPools/p/providers/q"
	var out, errb bytes.Buffer
	if code := selfcheck(env(map[string]string{"GCP_WIF_PROVIDER": provider, "HUBSPOT_PORTAL_ID": "222222222"}), selfNeedles(), &out, &errb); code != 0 {
		t.Fatalf("matching: exit %d, stderr %q", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	code := selfcheck(env(map[string]string{"GCP_WIF_PROVIDER": provider, "HUBSPOT_PORTAL_ID": "333333333"}), selfNeedles(), &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "HubSpot portal id") || !strings.Contains(errb.String(), "HUBSPOT_PORTAL_ID") {
		t.Errorf("mismatch: exit %d, stderr %q", code, errb.String())
	}
	if all := out.String() + errb.String(); strings.Contains(all, "333333333") || strings.Contains(all, "111111111111") {
		t.Error("output contains a value")
	}
}

func TestSelfcheck_BadProviderAndEmptyVariables(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	code := selfcheck(env(map[string]string{"GCP_WIF_PROVIDER": "nope"}), selfNeedles(), &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "GCP_WIF_PROVIDER") {
		t.Errorf("bad provider: exit %d, stderr %q", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := selfcheck(env(nil), selfNeedles(), &out, &errb); code != 0 {
		t.Fatalf("empty: exit %d", code)
	}
	if n := strings.Count(out.String(), "self-check skipped"); n != 2 {
		t.Errorf("got %d skip notices, want 2: %q", n, out.String())
	}
	out.Reset()
	if code := selfcheck(env(map[string]string{"GCP_WIF_PROVIDER": "  ", "HUBSPOT_PORTAL_ID": "\t"}), selfNeedles(), &out, &errb); code != 0 || strings.Count(out.String(), "self-check skipped") != 2 {
		t.Errorf("blank variables: exit %d, stdout %q, want 0 and two skip notices", code, out.String())
	}
}

func TestPinned_TwoNeedlesOfTheRightShape(t *testing.T) {
	t.Parallel()
	if len(pinned) != 2 {
		t.Fatalf("got %d needles, want 2", len(pinned))
	}
	want := []struct {
		kind   string
		digits int
	}{{"GCP prod project number", 12}, {"HubSpot portal id", 9}}
	hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for i, w := range want {
		n := pinned[i]
		if n.kind != w.kind || n.digits != w.digits || !hex.MatchString(n.digest) {
			t.Errorf("needle %d is {%q %d}, want {%q %d} with a 64-char hex digest", i, n.kind, n.digits, w.kind, w.digits)
		}
	}
	if pinned[0].digest == pinned[1].digest {
		t.Error("the two digests are equal")
	}
}

func TestRun_ReadingNoFilesIsAnError(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := run(gitRepo(t), fakeNeedles(), &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "read 0 tracked files") || out.Len() != 0 {
		t.Errorf("stdout %q stderr %q, want the cause on stderr only", out.String(), errb.String())
	}
}
