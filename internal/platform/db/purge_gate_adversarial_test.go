// Adversarial cover for the three properties of the health-gate that
// purge_gate_test.go's scanners cannot see: that the field is read out of the
// body whose build was matched, that the comment carrying AC-6's rationale is
// still there, and that a value outside PurgeOutcome's domain fails closed.
package db_test

import (
	"os"
	"strings"
	"testing"
)

// devEnvRaw returns dev-env.yml with its comments INTACT. devEnvExecutable
// strips them, which is right for the executable checks and wrong for the one
// requirement that is about a comment.
func devEnvRaw(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../.github/workflows/dev-env.yml")
	if err != nil {
		t.Fatalf("read .github/workflows/dev-env.yml: %v", err)
	}
	return string(b)
}

// lineIndex returns the 0-based index of the first line containing fragment, or
// -1.
func lineIndex(lines []string, fragment string) int {
	for i, line := range lines {
		if strings.Contains(line, fragment) {
			return i
		}
	}
	return -1
}

// sameBodyFaults returns every reason the demo_purge read in yaml is not taken
// from the body whose build was already matched. Empty means it is.
func sameBodyFaults(yaml string) []string {
	lines := strings.Split(yaml, "\n")
	build := lineIndex(lines, `.build // empty`)
	purge := lineIndex(lines, `.demo_purge // empty`)
	brk := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "break" {
			brk = i
			break
		}
	}

	var faults []string
	if purge < 0 {
		return append(faults, "the workflow never reads .demo_purge out of any body")
	}
	if build < 0 || brk < 0 {
		return append(faults, "the workflow carries no .build read or no break to place the demo_purge read between")
	}
	if purge < build || purge > brk {
		faults = append(faults, "the demo_purge read sits outside the block that matched .build against EXPECTED_BUILD, so it can observe a different container mid-rollout")
	}
	if !strings.Contains(lines[purge], `"$body"`) {
		faults = append(faults, "the demo_purge read does not parse $body, so it is not reading the response whose build was verified")
	}
	if strings.Contains(lines[purge], "curl") {
		faults = append(faults, "the demo_purge read issues its own curl, which can land on a different container while a rolling deploy is still in progress")
	}
	return faults
}

// TestPurgeGateReadsTheSameBodyItMatchedTheBuildAgainst: a second fetch can hit
// the OLD container mid-rollout and report a demo_purge the commit under test
// never produced. That is why the build field exists at all; the reset read is
// worded to say so and the purge read must hold to it too.
func TestPurgeGateReadsTheSameBodyItMatchedTheBuildAgainst(t *testing.T) {
	yaml := devEnvExecutable(t)
	if !strings.Contains(yaml, "healthz") {
		t.Fatalf("the comment-stripped dev-env.yml mentions healthz nowhere; the checks below would pass having examined nothing")
	}

	for _, fault := range sameBodyFaults(yaml) {
		t.Errorf("dev-env.yml's health-gate: %s", fault)
	}

	t.Run("control needle", func(t *testing.T) {
		const sameBody = `
            seen=$(printf '%s' "$body" | jq -r '.build // empty')
            if [ "$seen" = "$EXPECTED_BUILD" ]; then
              purge=$(printf '%s' "$body" | jq -r '.demo_purge // empty')
              break
            fi
`
		if faults := sameBodyFaults(sameBody); len(faults) != 0 {
			t.Fatalf("the scanner reports %v against a fixture that DOES read one body — it cannot recognise the shape it demands", faults)
		}

		refetched := strings.Replace(sameBody,
			`purge=$(printf '%s' "$body" | jq -r '.demo_purge // empty')`,
			`purge=$(curl -fsS "$GATEWAY_URL/healthz" | jq -r '.demo_purge // empty')`, 1)
		if faults := sameBodyFaults(refetched); len(faults) == 0 {
			t.Fatal("the scanner reports a fixture that re-fetches with its own curl clean — a clean report against the real workflow means nothing")
		}

		outside := strings.Replace(sameBody,
			`              purge=$(printf '%s' "$body" | jq -r '.demo_purge // empty')
`, "", 1) + `
          purge=$(printf '%s' "$body" | jq -r '.demo_purge // empty')
`
		if faults := sameBodyFaults(outside); len(faults) == 0 {
			t.Fatal("the scanner reports a fixture whose read sits after the break clean — it cannot find a planted violation")
		}
	})
}

// purgeBarrierComment returns the contiguous comment blocks immediately above each
// top-level block of the purge gate in the RAW job. Empty when there are none.
func purgeBarrierComment(rawJob string) string {
	lines := strings.Split(rawJob, "\n")
	var parts []string
	for _, b := range gateBlocks(rawJob, "purge", "want_purge") {
		at := b[0].line
		first := at
		for first > 0 && strings.HasPrefix(strings.TrimSpace(lines[first-1]), "#") {
			first--
		}
		if first < at {
			parts = append(parts, strings.Join(lines[first:at], "\n"))
		}
	}
	return strings.Join(parts, "\n")
}

// TestPurgeGateCommentExplainsWhyItIsDirectional: db_reset and demo_purge are
// both asserted per target now, for different reasons. The comment must say why
// production expects "false", or the next reader restores the old all-targets
// "true". devEnvExecutable strips comments, so no other test can see this.
func TestPurgeGateCommentExplainsWhyItIsDirectional(t *testing.T) {
	job := strings.Join(healthGateJob(devEnvRaw(t)), "\n")
	if !strings.Contains(job, `.build // empty`) {
		t.Fatal("the raw dev-env.yml has no health-gate job that reads .build; the comment scan would examine nothing")
	}
	comment := purgeBarrierComment(job)
	if comment == "" {
		t.Fatal("the purge gate in dev-env.yml's health-gate carries no comment above it at all")
	}

	want := []string{"ENVIRONMENT", "provisionableEnvironment", "set-fork-environment"}
	if missing := missingFragments(comment, want); len(missing) != 0 {
		t.Errorf("the comment above dev-env.yml's purge gate never mentions %v, so it does not say why a fork expects \"true\" and production expects \"false\":\n%s", missing, comment)
	}

	t.Run("control needle", func(t *testing.T) {
		const explains = `
          # Directional: set-fork-environment sets a fork's gateway ENVIRONMENT to
          # development, so its purge runs. Production reads ENVIRONMENT=production,
          # which db.provisionableEnvironment refuses, so its purge never runs.
          if [ "$IS_PR" = "true" ]; then
            want_purge=true
          else
            want_purge=false
          fi
          if [ "$purge" != "$want_purge" ]; then
            exit 1
          fi
`
		if got := purgeBarrierComment(explains); got == "" {
			t.Fatal("the extractor found no comment above a fixture that carries three lines of one")
		} else if missing := missingFragments(got, want); len(missing) != 0 {
			t.Fatalf("the scanner calls %v missing from a fixture that carries all of them", missing)
		}

		const explainsNothing = `
          # The purge barrier.
          if [ "$purge" != "$want_purge" ]; then
            exit 1
          fi
`
		if got := missingFragments(purgeBarrierComment(explainsNothing), want); len(got) != len(want) {
			t.Fatalf("the scanner found only %d of %d fragment(s) missing from a bare comment — a clean report from it would prove nothing", len(got), len(want))
		}
	})
}

// TestPurgeGateFailsOnAValueOutsideThePurgeOutcomeDomain: the gate must fail
// closed, not open, on both targets. /healthz relays a plain string and the field
// survives a gateway of any age, so a value that only looks like the expected one
// must still be a red deploy. Each target's own expected value must still pass,
// or a gate that fails everything would clear this test.
func TestPurgeGateFailsOnAValueOutsideThePurgeOutcomeDomain(t *testing.T) {
	offDomain := []string{"TRUE", "True", "1", "yes", "ok", "true ", " true", "skipped", "null", "FALSE", "False", "0", "no", "false ", " false"}
	targets := []struct{ isPR, passes string }{{"true", "true"}, {"false", "false"}}
	if len(offDomain) == 0 || len(targets) == 0 {
		t.Fatal("no values to try — the loop below would assert nothing")
	}

	t.Run("control needle", func(t *testing.T) {
		const inverted = "if [ \"$purge\" = \"true\" ]; then\nexit 1\nfi"
		for _, tg := range targets {
			for _, v := range offDomain {
				if got := runGateBlock(t, inverted, tg.isPR, v); got != 0 {
					t.Fatalf("the runner reports exit %d for IS_PR=%s purge=%q against a block that exits 0 for everything but \"true\" — it is not observing the block it was given", got, tg.isPR, v)
				}
			}
		}
	})

	block := gateScript(devEnvHealthGate(t), "purge", "want_purge")
	if block == "" {
		t.Fatal(`dev-env.yml's health-gate carries no if-block on "$purge" to run`)
	}

	for _, tg := range targets {
		if got := runGateBlock(t, block, tg.isPR, tg.passes); got != 0 {
			t.Errorf("IS_PR=%s demo_purge=%q exits %d, want 0 — the gate refuses the one outcome this target expects", tg.isPR, tg.passes, got)
		}
		for _, v := range offDomain {
			if got := runGateBlock(t, block, tg.isPR, v); got != 1 {
				t.Errorf("IS_PR=%s demo_purge=%q exits %d, want 1 — only exactly %q passes on this target", tg.isPR, v, got, tg.passes)
			}
		}
	}
}
