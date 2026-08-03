// bucket_adversarial_test.go closes four holes found by MUTATING the DOC-01-02
// implementation. Each mutation was applied to the real source, `go test
// -count=1 ./tools/prenv/...` stayed GREEN, and the mutation was reverted.
//
//	M-doublespace  DSN_VAR_PREFIXES="DATABASE  DOCUMENT" (two spaces)
//	               -> suite green. jq -R 'split(" ")' yields an EMPTY prefix,
//	               startswith("") is universally true, and the filter ships the
//	               whole rendered map -- S2S_TOKEN included. strings.Fields in
//	               shellDSNVarPrefixes collapses the run and cannot see it.
//	M-ungated      drop assert_bucket_isolation from ensure_bucket's early
//	               return -> suite green. That return is the NORMAL path
//	               (Railway auto-provisions into a fork), so the isolation
//	               assertion would never run.
//	M-post         swap the credential probe's graphql_try for graphql_post
//	               -> suite green. bucketS3Credentials ERRORS for a pair with no
//	               instance, so reconcile-fork would hard-exit on every fresh
//	               fork.
//	M-blocklist    `if req.Kind != KindDSN` instead of `== KindOpaque`
//	               -> suite green. Equivalent for two kinds; fail-OPEN the day a
//	               third arrives.
//
// No test here skips, and none restates behaviour an existing test pins.
package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// T4-1. The shell's prefix filter must not be universal.
//
// shellDSNVarPrefixes parses the assignment with strings.Fields, which is not
// what jq does. The two disagree on exactly the input that matters: a run of
// spaces yields an empty prefix in jq and none in Go, and an empty prefix turns
// the filter that exists to NARROW the credential-dense map into a pass-through.
//
// KILLS: M-doublespace, and any prefix broad enough to ship an unrelated
// variable.
func TestShellPrefixFilterIsNotUniversal(t *testing.T) {
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	m := dsnVarPrefixAssignment.FindStringSubmatch(string(content))
	if m == nil {
		t.Fatalf("could not find a DSN_VAR_PREFIX* assignment in %s", path)
	}

	rhs := strings.TrimSpace(m[2])
	if !strings.HasPrefix(rhs, `"`) || !strings.HasSuffix(rhs, `"`) || len(rhs) < 2 {
		t.Fatalf("the prefix assignment is %s, not a double-quoted string. cmd_assert_db_dsns splits it with `jq -R 'split(\" \")'`; a different bash spelling means different split semantics and this test must be re-derived against them rather than left passing.", rhs)
	}

	// The shell's own split: every space, including repeated ones.
	prefixes := strings.Split(rhs[1:len(rhs)-1], " ")
	for _, p := range prefixes {
		if p == "" {
			t.Errorf("the prefix assignment %s yields an EMPTY prefix under `jq -R 'split(\" \")'` (%q). startswith(\"\") is true for every key, so cmd_assert_db_dsns ships the whole rendered variable map -- the most credential-dense object the script holds -- instead of narrowing it.", rhs, prefixes)
		}
	}

	// An over-broad prefix widens the same leak without emptying anything.
	for _, control := range []string{"S2S_TOKEN", "SENTRY_DSN", "PORT", "RAILWAY_ENVIRONMENT_NAME"} {
		for _, p := range prefixes {
			if p != "" && strings.HasPrefix(control, p) {
				t.Errorf("prefix %q matches %q, which no severity-table row names. The filter would ship it to prenv's stdin for nothing.", p, control)
			}
		}
	}
}

// shellFunctionBody returns the lines between `name() {` and its column-0 `}`.
func shellFunctionBody(t *testing.T, name string) []string {
	t.Helper()
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	lines := strings.Split(string(content), "\n")
	start := -1
	for i, line := range lines {
		if line != name+"() {" {
			continue
		}
		if start >= 0 {
			t.Fatalf("%s() is defined twice in %s (lines %d and %d)", name, path, start+1, i+1)
		}
		start = i
	}
	if start < 0 {
		t.Fatalf("no %s() definition in %s. If it was renamed, update this test deliberately -- do not let the assertion go silently inert.", name, path)
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "}" {
			return lines[start+1 : i]
		}
	}
	t.Fatalf("%s() has no closing brace at column 0", name)
	return nil
}

// statements drops blank and comment lines.
func statements(body []string) []string {
	var out []string
	for _, line := range body {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// T4-2. Every successful return out of ensure_bucket must be gated by the
// fork-isolation assertion.
//
// Railway auto-provisions a bucket instance into a duplicated environment, so
// the "already present, no mutation" return is the normal path and the create
// path is the exception. An isolation assertion reachable only via create is
// therefore unreachable in practice, while looking like coverage: the PR
// environment would write its test documents into live evidence.
//
// KILLS: M-ungated, and a fall-through that returns the last command's status.
func TestEnsureBucketIsolationGatesEverySuccessfulReturn(t *testing.T) {
	stmts := statements(shellFunctionBody(t, "ensure_bucket"))

	returns := 0
	for i, s := range stmts {
		if s != "return 0" {
			continue
		}
		returns++
		if i == 0 || !strings.HasPrefix(stmts[i-1], "assert_bucket_isolation ") {
			prev := "<nothing>"
			if i > 0 {
				prev = stmts[i-1]
			}
			t.Errorf("ensure_bucket returns 0 after %q, not after assert_bucket_isolation. Every successful return must compare the fork's bucketName with the source environment's first; an ungated return lets a PR environment that SHARES the production bucket pass.", prev)
		}
	}
	if returns == 0 {
		t.Fatalf("ensure_bucket has no `return 0` -- either it was rewritten, or this test is asserting over the wrong function body")
	}

	last := stmts[len(stmts)-1]
	if last != "exit 1" && last != "return 1" {
		t.Errorf("ensure_bucket ends with %q. Falling off the end returns the last command's status, which is an UNGATED success whenever that status is 0.", last)
	}
}

// T4-3. The per-environment probe must be read with graphql_try.
//
// bucketS3Credentials returns a GraphQL error, not [], for a (bucket,
// environment) pair with no instance. graphql_post exits 1 on any .errors, so a
// copy of ensure_postgres_volume's first read hard-exits reconcile-fork on every
// fresh fork -- before any bucket could be created.
//
// KILLS: M-post.
func TestBucketCredentialProbeNeverUsesGraphqlPost(t *testing.T) {
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	calls := 0
	for _, line := range strings.Split(string(content), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "#") || !strings.Contains(s, "bucket_credentials_body ") {
			continue
		}
		calls++
		if !strings.Contains(s, "graphql_try") {
			t.Errorf("the bucket credential probe is invoked without graphql_try: %q. A probe failure is UNKNOWN, not fatal and not 'absent' -- graphql_post would exit 1 on the Not-Authorized answer a fork with no instance yet returns.", s)
		}
	}
	if calls < 2 {
		t.Fatalf("found %d bucket_credentials_body call site(s), want the probe, the confirm poll and the isolation read -- this test is asserting over the wrong text", calls)
	}
}

// T4-4. cmd_reconcile_fork must call ensure_bucket, after ensure_postgres_running.
//
// A deleted call site makes every guard above inert. The ordering also gives an
// auto-inherited instance the Postgres block's wait to materialise, which is why
// it is not moved earlier for fail-fast.
func TestReconcileForkCallsEnsureBucketAfterPostgres(t *testing.T) {
	stmts := statements(shellFunctionBody(t, "cmd_reconcile_fork"))

	bucket, postgres := -1, -1
	for i, s := range stmts {
		switch {
		case strings.HasPrefix(s, "ensure_bucket "):
			bucket = i
		case strings.HasPrefix(s, "ensure_postgres_running "):
			postgres = i
		}
	}
	if bucket < 0 {
		t.Fatalf("cmd_reconcile_fork does not call ensure_bucket. Without the call site every PR environment forks with ${{source-documents.*}} references that resolve to nothing.")
	}
	if postgres < 0 {
		t.Fatalf("cmd_reconcile_fork does not call ensure_postgres_running -- the ordering assertion below would be vacuous")
	}
	if bucket < postgres {
		t.Errorf("ensure_bucket is called before ensure_postgres_running (positions %d and %d)", bucket, postgres)
	}
}

// T4-5. A Kind with no dispatch arm must get the strict path.
//
// `if req.Kind == KindOpaque` and `if req.Kind != KindDSN` are the same for two
// kinds. They differ on the third: the first leaves an undispatched Kind fully
// checked, the second silently drops it to unset/empty/unrendered only. Same
// fail-safe reason KindDSN is iota-0.
//
// KILLS: M-blocklist.
func TestCheckDSNs_UnknownKindGetsTheStrictPath(t *testing.T) {
	saved := DSNRequirements
	t.Cleanup(func() { DSNRequirements = saved })

	m := dsnMap{"invoice": {"DOCUMENT_BUCKET": sentinelBucket}}

	// Oracle: the same value under KindOpaque is clean, so the assertion below
	// discriminates the dispatch rather than the value.
	DSNRequirements = []DSNRequirement{{"invoice", "DOCUMENT_BUCKET", IfPresent, KindOpaque}}
	if got := CheckDSNs(m); len(got) != 0 {
		t.Fatalf("oracle: KindOpaque reports %v for an opaque token", offenderStrings(got))
	}

	DSNRequirements = []DSNRequirement{{"invoice", "DOCUMENT_BUCKET", IfPresent, Kind(99)}}
	want := []Offender{{"invoice", "DOCUMENT_BUCKET", DefectNoPassword}}
	if got := CheckDSNs(m); !reflect.DeepEqual(got, want) {
		t.Errorf("a Kind with no dispatch arm reports %v, want %v: an unrecognised Kind must fall to the full DSN checking, not to the lax path.", offenderStrings(got), offenderStrings(want))
	}
}
