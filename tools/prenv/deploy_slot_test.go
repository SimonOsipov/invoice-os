// deploy_slot_test.go runs scripts/ci/deploy-slot.sh under a fake gh, sleep and date.
// Rules: INFRA-08 story, Design "The slot rule"; decisions D-03..D-21.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Constants copied from the story's "The slot rule"; each is a script constant of the same name.
const (
	slotBase     = 1790000000 // fake `date +%s` before any sleep
	slotSelfID   = 200        // RUN_ID of every run under test, unless a test says otherwise
	slotPoll     = "60"       // POLL_SECONDS: every sleep argument
	slotDeadline = 2400       // DEADLINE_SECONDS
	slotMaxAge   = 10800      // MAX_AGE_SECONDS (3 h)
	slotMaxHold  = 3600       // MAX_HOLD_SECONDS (60 min)
	slotGrace    = 60         // NO_SLOT_GRACE_SECONDS

	jobSlot    = "Deploy slot"                 // Design 2.3: exact job names the script matches
	jobRelease = "Release deploy slot"         // Design 2.3
	jobChanges = "Detect E2E-relevant changes" // Design 2.3, D-05
)

// slotShim is gh, date and sleep on PATH. gh answers by URL: the runs list from runs.seq (one
// line per call, then runs.default), a jobs read from jobs-<id>.seq (then jobs-<id>.default).
// Line EXIT1 fails with "gh: HTTP 502"; FAIL:<msg> fails with <msg>; both add a second stderr line.
// date answers `+%s` with slotBase + the sleeps in sleep.log + clock.jump per read after the first.
type slotShim struct{ dir string }

func newSlotShim(t *testing.T) slotShim {
	t.Helper()
	dir := t.TempDir()
	gh := `#!/bin/sh
dir='` + dir + `'
sep=""
for a in "$@"; do printf '%s%s' "$sep" "$a"; sep="$(printf '\t')"; done >> "$dir/argv.log"
printf '\n' >> "$dir/argv.log"
if [ "$(wc -l < "$dir/argv.log" | tr -d ' ')" -gt 2000 ]; then echo "gh stub: runaway script" >&2; exit 97; fi
case "$*" in
  *actions/workflows/dev-env.yml/runs*) key=runs ;;
  *actions/runs/*/jobs*) key=jobs-$(printf '%s' "$*" | sed -n 's#.*actions/runs/\([0-9][0-9]*\)/jobs.*#\1#p') ;;
  *) echo "gh stub: unrouted call: $*" >&2; exit 98 ;;
esac
if [ -s "$dir/$key.seq" ]; then
  line=$(head -n 1 "$dir/$key.seq"); tail -n +2 "$dir/$key.seq" > "$dir/$key.seq.tmp"; mv "$dir/$key.seq.tmp" "$dir/$key.seq"
elif [ -f "$dir/$key.default" ]; then line=$(cat "$dir/$key.default")
else line="FAIL:gh stub: no answer for $key"; fi
case "$line" in
  EXIT1) fail='gh: HTTP 502' ;;
  FAIL:*) fail=${line#FAIL:} ;;
  *) printf '%s\n' "$line"; exit 0 ;;
esac
printf '%s\ntry again later\n' "$fail" >&2
exit 1
`
	date := `#!/bin/sh
dir='` + dir + `'
if [ "$1" != "+%s" ]; then exec /bin/date "$@"; fi
n=$(cat "$dir/date.n" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$dir/date.n"
slept=0
if [ -f "$dir/sleep.log" ]; then for a in $(cat "$dir/sleep.log"); do slept=$((slept+a)); done; fi
jump=0
if [ -f "$dir/clock.jump" ]; then jump=$(cat "$dir/clock.jump"); fi
echo $(( ` + strconv.Itoa(slotBase) + ` + slept + jump * (n - 1) ))
`
	for name, body := range map[string]string{"gh": gh, "date": date} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSleepStub(t, dir)
	return slotShim{dir: dir}
}

func (s slotShim) put(t *testing.T, name string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, name), strings.Join(lines, "\n")+"\n")
}

func (s slotShim) runs(t *testing.T, bodies ...string)  { t.Helper(); s.put(t, "runs.seq", bodies...) }
func (s slotShim) runsAlways(t *testing.T, body string) { t.Helper(); s.put(t, "runs.default", body) }

func (s slotShim) jobs(t *testing.T, id int, bodies ...string) {
	t.Helper()
	s.put(t, fmt.Sprintf("jobs-%d.seq", id), bodies...)
}

func (s slotShim) jobsAlways(t *testing.T, id int, body string) {
	t.Helper()
	s.put(t, fmt.Sprintf("jobs-%d.default", id), body)
}

func (s slotShim) jump(t *testing.T, seconds int) {
	t.Helper()
	s.put(t, "clock.jump", strconv.Itoa(seconds))
}

func slotEnv(runID int, event string) string {
	return fmt.Sprintf("export REPO=o/r GH_TOKEN=fake RUN_ID=%d EVENT_NAME=%s\n", runID, event)
}

func slotScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "scripts", "ci", "deploy-slot.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("scripts/ci/deploy-slot.sh does not exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("scripts/ci/deploy-slot.sh is not executable (mode %v)", info.Mode())
	}
	return path
}

// runEnv returns stdout and stderr in one stream, in order.
func (s slotShim) runEnv(t *testing.T, env string) (out string, code int) {
	t.Helper()
	script := slotScript(t)
	stdout, stderr, code := runBashScript(t, "exec 2>&1\nexport PATH='"+s.dir+"':\"$PATH\"\n"+env+"bash '"+script+"'\n")
	return stdout + stderr, code
}

func (s slotShim) run(t *testing.T) (string, int) {
	t.Helper()
	return s.runEnv(t, slotEnv(slotSelfID, "pull_request"))
}

func (s slotShim) argv(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "argv.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func (s slotShim) listCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, l := range s.argv(t) {
		if strings.Contains(l, "actions/workflows/dev-env.yml/runs") {
			out = append(out, l)
		}
	}
	return out
}

// jobsCalls counts jobs reads per run id.
func (s slotShim) jobsCalls(t *testing.T) map[int]int {
	t.Helper()
	re := regexp.MustCompile(`actions/runs/(\d+)/jobs`)
	got := map[int]int{}
	for _, l := range s.argv(t) {
		if m := re.FindStringSubmatch(l); m != nil {
			id, _ := strconv.Atoi(m[1])
			got[id]++
		}
	}
	return got
}

func (s slotShim) wantSleeps(t *testing.T, n int) {
	t.Helper()
	want := make([]string, n)
	for i := range want {
		want[i] = slotPoll
	}
	got := railwayShim{dir: s.dir}.sleeps(t)
	if len(got) == 0 && n == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sleeps = %v, want %d x %s", got, n, slotPoll)
	}
}

func (s slotShim) wantCalls(t *testing.T, want int) {
	t.Helper()
	if n := len(s.argv(t)); n != want {
		t.Errorf("gh calls = %d, want %d", n, want)
	}
}

func slotTS(agoSeconds int) string {
	return time.Unix(int64(slotBase-agoSeconds), 0).UTC().Format("2006-01-02T15:04:05Z")
}

// slotRun is one workflow run in the runs list. Zero values: attempt 1, in_progress,
// pull_request, branch feature/x, started 600 s ago, no PR.
type slotRun struct {
	id, attempt, pr, startedAgo int
	status, event, branch       string
}

func (r slotRun) json() string {
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	attempt, started := r.attempt, r.startedAgo
	if attempt == 0 {
		attempt = 1
	}
	if started == 0 {
		started = 600
	}
	prs := "[]"
	if r.pr != 0 {
		prs = fmt.Sprintf(`[{"number":%d}]`, r.pr)
	}
	return fmt.Sprintf(`{"id":%d,"run_attempt":%d,"status":"%s","event":"%s","head_branch":"%s","run_started_at":"%s","pull_requests":%s}`,
		r.id, attempt, def(r.status, "in_progress"), def(r.event, "pull_request"), def(r.branch, "feature/x"), slotTS(started), prs)
}

// slotList is a runs-list body: the run under test plus the given runs, newest id first.
func slotList(runs ...slotRun) string { return slotListFor(slotSelfID, runs...) }

func slotListFor(self int, runs ...slotRun) string {
	all := append([]slotRun{{id: self}}, runs...)
	sort.Slice(all, func(i, j int) bool { return all[i].id > all[j].id })
	parts := make([]string, len(all))
	for i, r := range all {
		parts[i] = r.json()
	}
	return fmt.Sprintf(`{"total_count":%d,"workflow_runs":[%s]}`, len(all), strings.Join(parts, ","))
}

func jobsBody(jobs ...string) string {
	return fmt.Sprintf(`{"total_count":%d,"jobs":[%s]}`, len(jobs), strings.Join(jobs, ","))
}

func jobDone(name, conclusion string, agoSeconds int) string {
	return fmt.Sprintf(`{"name":"%s","status":"completed","conclusion":"%s","completed_at":"%s"}`, name, conclusion, slotTS(agoSeconds))
}

func jobOpen(name, status string) string {
	return fmt.Sprintf(`{"name":"%s","status":"%s","conclusion":null,"completed_at":null}`, name, status)
}

// holderJobs: slot passed 300 s ago, no release job yet.
func holderJobs() string {
	return jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "success", 300))
}

// pinnedHolderJobs: slot passed "in the future", so a jumping clock never expires the holder.
func pinnedHolderJobs() string {
	return jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "success", -100000))
}

func holdersOf(t *testing.T, s slotShim, ids ...int) {
	t.Helper()
	for _, id := range ids {
		s.jobsAlways(t, id, holderJobs())
	}
}

var slotPollRe = regexp.MustCompile(`(?m)^Deploy slot: poll ([0-9]+): .*$`)

func pollLine(t *testing.T, out string, k int) string {
	t.Helper()
	for _, m := range slotPollRe.FindAllStringSubmatch(out, -1) {
		if m[1] == strconv.Itoa(k) {
			return m[0]
		}
	}
	t.Fatalf("no `Deploy slot: poll %d: ` line in %q", k, out)
	return ""
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

func slotErrorLines(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "::error::") {
			got = append(got, l)
		}
	}
	return got
}

func wantContains(t *testing.T, what, got string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(got, sub) {
			t.Errorf("%s lacks %q: %q", what, sub, got)
		}
	}
}

func wantExit(t *testing.T, code, want int, out string) {
	t.Helper()
	if code != want {
		t.Errorf("exit %d, want %d; output = %q", code, want, out)
	}
}

func TestDeploySlot_NonPREventPassesWithoutCalls(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"push", "workflow_dispatch"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			s := newSlotShim(t)
			out, code := s.runEnv(t, slotEnv(slotSelfID, event))

			wantExit(t, code, 0, out)
			wantContains(t, "output", out, "Deploy slot: a "+event+" run takes a slot without waiting.")
			s.wantCalls(t, 0)
			s.wantSleeps(t, 0)
		})
	}
}

func TestDeploySlot_NoOtherRunsPassesOnFirstPoll(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList())
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "output", out, "0 of 2 held", "taken after 0 s")
	s.wantCalls(t, 1)
	s.wantSleeps(t, 0)
}

func TestDeploySlot_OneHolderPasses(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList(slotRun{id: 101, pr: 7}))
	holdersOf(t, s, 101)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "1 of 2 held by run 101 (PR #7)")
	s.wantSleeps(t, 0)
	// Design 2.3: the jobs read asks for the latest attempt only.
	for _, l := range s.argv(t) {
		if strings.Contains(l, "actions/runs/101/jobs") {
			wantContains(t, "jobs call", l, "filter=latest")
		}
	}
	if n := s.jobsCalls(t)[101]; n != 1 {
		t.Errorf("jobs calls for run 101 = %d, want 1", n)
	}
}

func TestDeploySlot_TwoHoldersWaitThenPass(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	two := slotList(slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12})
	s.runs(t, two, two)
	holdersOf(t, s, 101)
	s.jobs(t, 102, holderJobs(), jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "success", 300), jobDone(jobRelease, "success", 5)))
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "2 of 2 held")
	s.wantSleeps(t, 1)
	if got := lastLine(out); got != "Deploy slot: taken after 60 s." {
		t.Errorf("last line = %q, want %q", got, "Deploy slot: taken after 60 s.")
	}
}

// Other run 150 (older) or 250 (newer) is tested against a fixed holder 151: 2 of 2 blocks, 1 of 2 passes.
func TestDeploySlot_HolderRule(t *testing.T) {
	t.Parallel()
	slotDone := func(c string) string { return jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, c, 300)) }
	withRelease := func(rel string) string {
		return jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "success", 300), rel)
	}
	cases := []struct {
		name   string
		id     int
		jobs   string
		holder bool
	}{
		{"slot success, release absent", 150, holderJobs(), true},
		{"slot success, release in_progress", 150, withRelease(jobOpen(jobRelease, "in_progress")), true},
		{"slot success, release queued", 150, withRelease(jobOpen(jobRelease, "queued")), true},
		{"release completed success", 150, withRelease(jobDone(jobRelease, "success", 5)), false},
		{"release completed cancelled", 150, withRelease(jobDone(jobRelease, "cancelled", 5)), false},
		{"slot in_progress on a newer id", 250, jobsBody(jobDone(jobChanges, "success", 40), jobOpen(jobSlot, "in_progress")), false},
		{"slot skipped", 150, slotDone("skipped"), false},
		{"slot failure", 150, slotDone("failure"), false},
		{"slot cancelled", 150, slotDone("cancelled"), false},
		{"no slot job listed", 150, jobsBody(jobDone(jobChanges, "success", 10)), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newSlotShim(t)
			s.runs(t, slotList(slotRun{id: c.id, pr: 5}, slotRun{id: 151, pr: 6}), slotList())
			s.jobsAlways(t, c.id, c.jobs)
			holdersOf(t, s, 151)
			out, code := s.run(t)

			line := pollLine(t, out, 1)
			wantExit(t, code, 0, out)
			wantContains(t, "poll 1", line, "run 151 (PR #6)")
			if strings.Contains(line, "older waiting") || strings.Contains(line, "expired") {
				t.Errorf("poll 1 reports a waiter or an expired holder: %q", line)
			}
			if c.holder {
				wantContains(t, "poll 1", line, "2 of 2 held", fmt.Sprintf("run %d (PR #5)", c.id))
				s.wantSleeps(t, 1)
			} else {
				wantContains(t, "poll 1", line, "1 of 2 held")
				if strings.Contains(line, fmt.Sprintf("run %d", c.id)) {
					t.Errorf("poll 1 names run %d, which is not a holder: %q", c.id, line)
				}
				s.wantSleeps(t, 0)
			}
		})
	}
}

func TestDeploySlot_OlderWaiterCountsNewerDoesNot(t *testing.T) {
	t.Parallel()
	waiting := jobsBody(jobDone(jobChanges, "success", 40), jobOpen(jobSlot, "in_progress"))
	t.Run("older run 199 counts", func(t *testing.T) {
		t.Parallel()
		s := newSlotShim(t)
		s.runs(t, slotList(slotRun{id: 150, pr: 5}, slotRun{id: 199, pr: 6}), slotList())
		holdersOf(t, s, 150)
		s.jobsAlways(t, 199, waiting)
		out, code := s.run(t)

		wantExit(t, code, 0, out)
		wantContains(t, "poll 1", pollLine(t, out, 1), "1 older waiting: run 199")
		s.wantSleeps(t, 1)
	})
	t.Run("newer run 201 does not", func(t *testing.T) {
		t.Parallel()
		s := newSlotShim(t)
		s.runs(t, slotList(slotRun{id: 150, pr: 5}, slotRun{id: 201, pr: 6}), slotList())
		holdersOf(t, s, 150)
		s.jobsAlways(t, 201, waiting)
		out, code := s.run(t)

		wantExit(t, code, 0, out)
		if strings.Contains(pollLine(t, out, 1), "older waiting") {
			t.Errorf("a newer waiter was counted: %q", out)
		}
		s.wantSleeps(t, 0)
	})
}

func TestDeploySlot_TwoOlderWaitersBlock(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotListFor(300, slotRun{id: 298}, slotRun{id: 299}), slotListFor(300))
	s.jobsAlways(t, 298, jobsBody(jobDone(jobChanges, "success", 40), jobOpen(jobSlot, "queued")))
	s.jobsAlways(t, 299, jobsBody(jobDone(jobChanges, "success", 40), jobOpen(jobSlot, "in_progress")))
	out, code := s.runEnv(t, slotEnv(300, "pull_request"))

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "0 of 2 held; 2 older waiting")
	s.wantSleeps(t, 1)
}

func TestDeploySlot_PushRunCountsAsHolder(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList(slotRun{id: 150, pr: 11}, slotRun{id: 250, event: "push", branch: "main"}), slotList())
	holdersOf(t, s, 150, 250)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "2 of 2 held", "run 250 (push main)")
	s.wantSleeps(t, 1)
}

func TestDeploySlot_ListsRunsWithoutStatusFilter(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList())
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	calls := s.listCalls(t)
	if len(calls) == 0 {
		t.Fatalf("no runs-list call in %q", s.argv(t))
	}
	for _, c := range calls {
		if strings.Contains(c, "status=") {
			t.Errorf("the runs request filters by status: %q", c)
		}
		wantContains(t, "list call", c, "per_page=100") // D-06: the 100 newest runs cover the 3 h window
	}
}

func TestDeploySlot_CountsEveryNotCompletedRunStatus(t *testing.T) {
	t.Parallel()
	waiting := jobsBody(jobDone(jobChanges, "success", 40), jobOpen(jobSlot, "in_progress"))
	for _, status := range []string{"queued", "waiting", "pending", "requested", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			s := newSlotShim(t)
			s.runs(t, slotList(slotRun{id: 99, status: status}, slotRun{id: 150, pr: 5}), slotList())
			holdersOf(t, s, 150)
			s.jobsAlways(t, 99, waiting)
			out, code := s.run(t)

			wantExit(t, code, 0, out)
			wantContains(t, "poll 1", pollLine(t, out, 1), "1 older waiting")
			s.wantSleeps(t, 1)
		})
	}
	t.Run("completed", func(t *testing.T) {
		t.Parallel()
		s := newSlotShim(t)
		s.runs(t, slotList(slotRun{id: 99, status: "completed"}, slotRun{id: 150, pr: 5}))
		holdersOf(t, s, 150)
		s.jobsAlways(t, 99, waiting)
		out, code := s.run(t)

		wantExit(t, code, 0, out)
		wantContains(t, "poll 1", pollLine(t, out, 1), "1 of 2 held by run 150")
		if n := s.jobsCalls(t)[99]; n != 0 {
			t.Errorf("a completed run was fetched %d times", n)
		}
		s.wantSleeps(t, 0)
	})
}

func TestDeploySlot_SkipsOwnRunAndStaleRuns(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList(slotRun{id: 55, startedAgo: 4 * 3600}))
	s.jobsAlways(t, slotSelfID, jobsBody(jobDone(jobChanges, "success", 40), jobOpen(jobSlot, "in_progress")))
	holdersOf(t, s, 55)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "0 of 2 held")
	s.wantCalls(t, 1)
	if calls := s.jobsCalls(t); len(calls) != 0 {
		t.Errorf("jobs reads = %v, want none for the own run or the 4 h old run", calls)
	}
}

func TestDeploySlot_StaleCutoffBoundary(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList(
		slotRun{id: 150, pr: 5, startedAgo: slotMaxAge - 60},         // 2 h 59 min: inside MAX_AGE_SECONDS
		slotRun{id: 151, pr: 6, startedAgo: slotMaxAge + 60},         // 3 h 01 min: outside
		slotRun{id: 152, pr: 7, startedAgo: slotMaxAge}), slotList()) // exactly 3 h: still inside
	holdersOf(t, s, 150, 151, 152)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "2 of 2 held", "run 150 (PR #5)", "run 152 (PR #7)")
	calls := s.jobsCalls(t)
	if calls[150] != 1 || calls[152] != 1 {
		t.Errorf("jobs calls = %v, want run 150 and run 152 fetched once", calls)
	}
	if calls[151] != 0 {
		t.Errorf("the 3 h 01 min old run 151 was fetched %d times", calls[151])
	}
}

func TestDeploySlot_OneLinePerPollAndSixtySecondSleeps(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	two := slotList(slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12})
	s.runs(t, two, two, two, slotList())
	holdersOf(t, s, 101, 102)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	lines := slotPollRe.FindAllStringSubmatch(out, -1)
	if len(lines) != 4 {
		t.Fatalf("poll lines = %d, want 4: %q", len(lines), out)
	}
	for i, m := range lines {
		if m[1] != strconv.Itoa(i+1) {
			t.Errorf("poll line %d is numbered %s", i+1, m[1])
		}
		if i < 3 {
			wantContains(t, fmt.Sprintf("poll %d", i+1), m[0], "2 of 2", "run 101 (PR #11)", "run 102 (PR #12)")
		}
	}
	s.wantSleeps(t, 3)
	if got := lastLine(out); got != "Deploy slot: taken after 180 s." {
		t.Errorf("last line = %q, want %q", got, "Deploy slot: taken after 180 s.")
	}
}

func TestDeploySlot_LabelFallsBackToEventAndBranch(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList(slotRun{id: 150, branch: "feature/x"}))
	holdersOf(t, s, 150)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "run 150 (pull_request feature/x)")
}

func TestDeploySlot_NeverCallsRailway(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	for _, name := range []string{"railway", "curl"} {
		stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + filepath.Join(s.dir, name+".log") + "'\n"
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	two := slotList(slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12})
	s.runs(t, two, slotList())
	holdersOf(t, s, 101, 102)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	s.wantSleeps(t, 1) // the wait ran, so a railway or curl call would have been logged
	for _, name := range []string{"railway", "curl"} {
		if _, err := os.Stat(filepath.Join(s.dir, name+".log")); !os.IsNotExist(err) {
			t.Errorf("the script invoked %s (stat err = %v)", name, err)
		}
	}
}

func TestDeploySlot_TimesOutNamingHolders(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runsAlways(t, slotList(slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12}))
	holdersOf(t, s, 101, 102)
	out, code := s.run(t)

	wantExit(t, code, 1, out)
	errs := slotErrorLines(out)
	if len(errs) != 1 {
		t.Fatalf("::error:: lines = %d, want 1: %q", len(errs), out)
	}
	wantContains(t, "::error:: line", errs[0], "no free slot after 40 polls (2340 s)", "run 101 (PR #11)", "run 102 (PR #12)")
	if n := len(s.listCalls(t)); n != 40 {
		t.Errorf("runs-list calls = %d, want 40", n)
	}
	s.wantSleeps(t, 39)
}

func TestDeploySlot_SlowCallsEndTheWaitByWallClock(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runsAlways(t, slotList(slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12}))
	s.jobsAlways(t, 101, pinnedHolderJobs())
	s.jobsAlways(t, 102, pinnedHolderJobs())
	s.jump(t, 600)
	out, code := s.run(t)

	wantExit(t, code, 1, out)
	errs := slotErrorLines(out)
	if len(errs) != 1 {
		t.Fatalf("::error:: lines = %d, want 1: %q", len(errs), out)
	}
	m := regexp.MustCompile(`no free slot after (\d+) polls \((\d+) s\)`).FindStringSubmatch(errs[0])
	if m == nil {
		t.Fatalf("::error:: line lacks `no free slot after <k> polls (<s> s)`: %q", errs[0])
	}
	polls, _ := strconv.Atoi(m[1])
	elapsed, _ := strconv.Atoi(m[2])
	if polls >= 10 {
		t.Errorf("polls = %d, want fewer than 10", polls)
	}
	if elapsed < slotDeadline-60 {
		t.Errorf("elapsed = %d s, want at least %d", elapsed, slotDeadline-60)
	}
}

func TestDeploySlot_ListReadFailureKeepsWaiting(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, "EXIT1", slotList())
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	line := pollLine(t, out, 1)
	wantContains(t, "poll 1", line, "could not read the runs: gh: HTTP 502")
	if strings.Contains(out, "try again later") {
		t.Errorf("output carries gh's second stderr line: %q", out)
	}
	s.wantSleeps(t, 1)
}

func TestDeploySlot_RateLimitIsNamed(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, "FAIL:gh: API rate limit exceeded (HTTP 403)", slotList())
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "API rate limit exceeded (HTTP 403)")
	s.wantSleeps(t, 1)
}

func TestDeploySlot_JobsReadFailureKeepsWaiting(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	one := slotList(slotRun{id: 101, pr: 11})
	s.runs(t, one, one)
	s.jobs(t, 101, "EXIT1", holderJobs())
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "could not read run 101: gh: HTTP 502")
	if strings.Contains(out, "taken after 0 s") {
		t.Errorf("passed on poll 1 with an unreadable run: %q", out)
	}
	wantContains(t, "poll 2", pollLine(t, out, 2), "1 of 2 held by run 101")
	s.wantSleeps(t, 1)
}

func TestDeploySlot_PersistentJobsFailureBlocksToTheDeadline(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runsAlways(t, slotList(slotRun{id: 77}))
	s.jobsAlways(t, 77, "EXIT1")
	out, code := s.run(t)

	wantExit(t, code, 1, out)
	errs := slotErrorLines(out)
	if len(errs) != 1 {
		t.Fatalf("::error:: lines = %d, want 1: %q", len(errs), out)
	}
	wantContains(t, "::error:: line", errs[0], "unreadable: run 77")
	if n := len(s.listCalls(t)); n != 40 {
		t.Errorf("runs-list calls = %d, want 40", n)
	}
}

func TestDeploySlot_NonJSONBodyKeepsWaiting(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"<html>", "[]", `{"message":"Bad credentials"}`} {
		t.Run("runs list "+body, func(t *testing.T) {
			t.Parallel()
			s := newSlotShim(t)
			s.runs(t, body, slotList())
			out, code := s.run(t)

			wantExit(t, code, 0, out)
			wantContains(t, "poll 1", pollLine(t, out, 1), "could not read the runs")
			s.wantSleeps(t, 1)
		})
	}
	for _, body := range []string{"<html>", "[]", `{"message":"Not Found"}`} {
		t.Run("jobs read "+body, func(t *testing.T) {
			t.Parallel()
			s := newSlotShim(t)
			one := slotList(slotRun{id: 101, pr: 11})
			s.runs(t, one, one)
			s.jobs(t, 101, body, holderJobs())
			out, code := s.run(t)

			wantExit(t, code, 0, out)
			wantContains(t, "poll 1", pollLine(t, out, 1), "could not read run 101")
			s.wantSleeps(t, 1)
		})
	}
}

func TestDeploySlot_UsageErrors(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"REPO", "RUN_ID", "EVENT_NAME"} {
		for _, how := range []string{"empty", "unset"} {
			t.Run(name+" "+how, func(t *testing.T) {
				t.Parallel()
				s := newSlotShim(t)
				s.runs(t, slotList())
				env := "unset REPO RUN_ID EVENT_NAME\n" + slotEnv(slotSelfID, "pull_request")
				if how == "empty" {
					env += "export " + name + "=''\n"
				} else {
					env += "unset " + name + "\n"
				}
				out, code := s.runEnv(t, env)

				wantExit(t, code, 2, out)
				wantContains(t, "output", out, "::error::")
				if !strings.Contains(strings.ToLower(out), "usage") {
					t.Errorf("output lacks a usage line: %q", out)
				}
				s.wantCalls(t, 0)
			})
		}
	}
}

func TestDeploySlot_SettledRunNotRefetched(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	p1 := slotList(slotRun{id: 50}, slotRun{id: 51}, slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12})
	p2 := slotList(slotRun{id: 50}, slotRun{id: 51}, slotRun{id: 101, pr: 11})
	s.runs(t, p1, p2)
	s.jobsAlways(t, 50, jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "success", 300), jobDone(jobRelease, "success", 5)))
	s.jobsAlways(t, 51, jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "skipped", 300)))
	holdersOf(t, s, 101, 102)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "2 of 2 held")
	wantContains(t, "poll 2", pollLine(t, out, 2), "1 of 2 held")
	calls := s.jobsCalls(t)
	if calls[50] != 1 || calls[51] != 1 {
		t.Errorf("jobs calls = %v, want runs 50 and 51 fetched once each", calls)
	}
	if calls[101] != 2 {
		t.Errorf("jobs calls for holder 101 = %d, want 2 (a holder is read every poll)", calls[101])
	}
	s.wantSleeps(t, 1)
}

func TestDeploySlot_RunWithoutSlotJobSettlesAfterGrace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		changesAgo   int
		wantFetchesN int
	}{
		{"changes completed 61 s ago settles at poll 1", slotGrace + 1, 1},
		{"changes completed exactly 60 s ago settles at poll 1", slotGrace, 1},
		{"changes completed 30 s ago settles at poll 2", slotGrace / 2, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newSlotShim(t)
			both := slotList(slotRun{id: 40}, slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12})
			s.runs(t, both, both, slotList(slotRun{id: 40}, slotRun{id: 101, pr: 11}))
			s.jobsAlways(t, 40, jobsBody(jobDone(jobChanges, "success", c.changesAgo)))
			holdersOf(t, s, 101, 102)
			out, code := s.run(t)

			wantExit(t, code, 0, out)
			s.wantSleeps(t, 2)
			if n := s.jobsCalls(t)[40]; n != c.wantFetchesN {
				t.Errorf("run 40 fetched %d times, want %d", n, c.wantFetchesN)
			}
			wantContains(t, "poll 3", pollLine(t, out, 3), "1 of 2 held")
		})
	}
}

func TestDeploySlot_NewAttemptIsNotSettled(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t,
		slotList(slotRun{id: 60, attempt: 1}, slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12}),
		slotList(slotRun{id: 60, attempt: 2, pr: 13}, slotRun{id: 101, pr: 11}),
		slotList())
	s.jobs(t, 60,
		jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "success", 300), jobDone(jobRelease, "success", 5)),
		holderJobs())
	holdersOf(t, s, 101, 102)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	if strings.Contains(pollLine(t, out, 1), "run 60") {
		t.Errorf("poll 1 counts the released attempt 1 of run 60: %q", out)
	}
	wantContains(t, "poll 2", pollLine(t, out, 2), "2 of 2 held", "run 60 (PR #13)")
	if n := s.jobsCalls(t)[60]; n != 2 {
		t.Errorf("run 60 fetched %d times, want 2 (attempt 2 is a new key)", n)
	}
}

func TestDeploySlot_StuckHolderExpires(t *testing.T) {
	t.Parallel()
	stuck := func(agoSeconds int) string {
		return jobsBody(jobDone(jobChanges, "success", agoSeconds+100), jobDone(jobSlot, "success", agoSeconds))
	}
	for _, ago := range []int{slotMaxHold, slotMaxHold + 1} {
		t.Run(fmt.Sprintf("%d s ago expires", ago), func(t *testing.T) {
			t.Parallel()
			s := newSlotShim(t)
			s.runs(t, slotList(slotRun{id: 150, pr: 5}, slotRun{id: 151, pr: 6}))
			s.jobsAlways(t, 150, stuck(ago))
			holdersOf(t, s, 151)
			out, code := s.run(t)

			wantExit(t, code, 0, out)
			wantContains(t, "poll 1", pollLine(t, out, 1), "1 of 2 held", "expired: run 150 (PR #5)")
			s.wantSleeps(t, 0)
		})
	}
	t.Run("59 min 59 s ago still holds", func(t *testing.T) {
		t.Parallel()
		s := newSlotShim(t)
		s.runs(t, slotList(slotRun{id: 150, pr: 5}, slotRun{id: 151, pr: 6}), slotList())
		s.jobsAlways(t, 150, stuck(slotMaxHold-1))
		holdersOf(t, s, 151)
		out, code := s.run(t)

		wantExit(t, code, 0, out)
		line := pollLine(t, out, 1)
		wantContains(t, "poll 1", line, "2 of 2 held", "run 150 (PR #5)")
		if strings.Contains(line, "expired") {
			t.Errorf("a 59 min 59 s holder is listed as expired: %q", line)
		}
		s.wantSleeps(t, 1)
	})
}

// Settled keys are matched whole: settled run 150 must not hide run 50.
func TestDeploySlot_SettledKeyDoesNotMatchAnIDSuffix(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	all := slotList(slotRun{id: 50, pr: 5}, slotRun{id: 150}, slotRun{id: 151, pr: 6})
	s.runs(t, all, all, slotList())
	s.jobsAlways(t, 150, jobsBody(jobDone(jobChanges, "success", 400), jobDone(jobSlot, "success", 300), jobDone(jobRelease, "success", 5)))
	holdersOf(t, s, 50, 151)
	out, code := s.run(t)

	wantExit(t, code, 0, out)
	wantContains(t, "poll 1", pollLine(t, out, 1), "2 of 2 held", "run 50 (PR #5)")
	wantContains(t, "poll 2", pollLine(t, out, 2), "2 of 2 held", "run 50 (PR #5)")
	if n := s.jobsCalls(t)[150]; n != 1 {
		t.Errorf("settled run 150 fetched %d times, want 1", n)
	}
	s.wantSleeps(t, 2)
}

// The deadline names the holders of the last poll that had a verdict, not "none" after a read failure.
func TestDeploySlot_DeadlineNamesHoldersOfTheLastVerdictPoll(t *testing.T) {
	t.Parallel()
	s := newSlotShim(t)
	s.runs(t, slotList(slotRun{id: 101, pr: 11}, slotRun{id: 102, pr: 12}))
	s.runsAlways(t, "EXIT1")
	holdersOf(t, s, 101, 102)
	out, code := s.run(t)

	wantExit(t, code, 1, out)
	errs := slotErrorLines(out)
	if len(errs) != 1 {
		t.Fatalf("::error:: lines = %d, want 1: %q", len(errs), out)
	}
	wantContains(t, "::error:: line", errs[0], "run 101 (PR #11)", "run 102 (PR #12)")
	wantContains(t, "poll 2", pollLine(t, out, 2), "could not read the runs")
}
