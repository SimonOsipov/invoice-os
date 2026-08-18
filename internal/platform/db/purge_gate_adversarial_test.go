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

// purgeBarrierComment returns the contiguous comment block immediately above the
// purge assertion in the RAW workflow. Empty when there is none.
func purgeBarrierComment(raw string) string {
	lines := strings.Split(raw, "\n")
	at := lineIndex(lines, `if [ "$purge" != "true" ]`)
	if at < 0 {
		return ""
	}
	first := at
	for first > 0 && strings.HasPrefix(strings.TrimSpace(lines[first-1]), "#") {
		first--
	}
	return strings.Join(lines[first:at], "\n")
}

// TestPurgeGateCommentExplainsWhyItIsNotDirectional (AC-6): db_reset is asserted
// in both directions and demo_purge is not, three lines apart. Without the
// reason written down the next reader reads that as an oversight, moves the
// purge assertion into the IS_PR branch to match, and silently disarms the
// production half. devEnvExecutable strips comments, so no other test in this
// package can see this.
func TestPurgeGateCommentExplainsWhyItIsNotDirectional(t *testing.T) {
	comment := purgeBarrierComment(devEnvRaw(t))
	if comment == "" {
		t.Fatal("the purge assertion in dev-env.yml carries no comment above it at all")
	}

	want := []string{"BootstrapEnabled", "db_reset", "IS_PR"}
	if missing := missingFragments(comment, want); len(missing) != 0 {
		t.Errorf("the comment above dev-env.yml's purge assertion never mentions %v, so it does not say why the assertion is not directional the way db_reset's is:\n%s", missing, comment)
	}

	t.Run("control needle", func(t *testing.T) {
		const explains = `
          # NOT directional the way db_reset above is, and deliberately outside
          # that if/elif: the purge's only gate is BootstrapEnabled, true on the
          # persistent environment too. Nesting it in the IS_PR branch disarms
          # the production half.
          if [ "$purge" != "true" ]; then
`
		if got := purgeBarrierComment(explains); got == "" {
			t.Fatal("the extractor found no comment above a fixture that carries four lines of one")
		} else if missing := missingFragments(got, want); len(missing) != 0 {
			t.Fatalf("the scanner calls %v missing from a fixture that carries all of them", missing)
		}

		const explainsNothing = `
          # The purge barrier.
          if [ "$purge" != "true" ]; then
`
		if got := missingFragments(purgeBarrierComment(explainsNothing), want); len(got) != len(want) {
			t.Fatalf("the scanner found only %d of %d fragment(s) missing from a bare comment — a clean report from it would prove nothing", len(got), len(want))
		}
	})
}

// purgeConditionBlock returns the health-gate's `if`-on-$purge block as runnable
// shell, whatever condition it is written with. Deliberately looser than
// purgeAssertionBlock, which pins one exact condition: a gate loosened to some
// other test must still be EXECUTED here, not merely reported missing.
func purgeConditionBlock(yaml string) string {
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "if [") && strings.Contains(trimmed, "$purge") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	var block []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		block = append(block, trimmed)
		if trimmed == "fi" {
			return strings.Join(block, "\n")
		}
	}
	return ""
}

// TestPurgeGateFailsOnAValueOutsideThePurgeOutcomeDomain: the gate must fail
// closed, not open. PurgeOutcome has three values today, but /healthz relays a
// plain string and the field survives a gateway of any age — a truthy-looking
// value that is not exactly "true" must still be a red deploy.
func TestPurgeGateFailsOnAValueOutsideThePurgeOutcomeDomain(t *testing.T) {
	offDomain := []string{"TRUE", "True", "1", "yes", "ok", "true ", " true", "skipped", "null"}
	if len(offDomain) == 0 {
		t.Fatal("no values to try — the loop below would assert nothing")
	}

	t.Run("control needle", func(t *testing.T) {
		const inverted = "if [ \"$purge\" = \"true\" ]; then\nexit 1\nfi"
		for _, v := range offDomain {
			if got := runGateBlock(t, inverted, v); got != 0 {
				t.Fatalf("the runner reports exit %d for purge=%q against a block that exits 0 for everything but \"true\" — it is not observing the block it was given", got, v)
			}
		}
	})

	block := purgeConditionBlock(devEnvExecutable(t))
	if block == "" {
		t.Fatal("dev-env.yml's health-gate carries no complete `if [ ... $purge ... ]; then ... fi` block to run")
	}

	for _, v := range offDomain {
		if got := runGateBlock(t, block, v); got != 1 {
			t.Errorf("demo_purge=%q exits %d, want 1 — anything that is not exactly \"true\" is not a purge that ran", v, got)
		}
	}
}
