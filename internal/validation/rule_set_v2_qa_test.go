// task-111 / M4-04-01, QA Stage (Mode B) -- adversarial/edge coverage ADDED on
// top of the Stage-1 RED specs (rule_set_v2_test.go) and the Stage-3 fixture
// fixes, per this stage's mandate to extend coverage the AC-derived specs did
// not include. Does NOT modify rule_set_v2_test.go, seed_test.go,
// schema_test.go, or any other existing file -- new coverage lives here so
// each stage's file stays exactly what it authored.
//
// Three gaps this file closes, none covered by the existing RS-V2-* suite:
//
//  1. TestRuleSetV2_JSONQuotedVersionPinNotPresent -- the QA Debate Log's F7
//     finding: task-111 §b's detection command is blind to a JSON-quoted
//     `"rule_set_version": <n>` literal (the closing quote sits between
//     `version` and the operator, defeating the command's
//     `[Vv]ersion[[:space:]]*(:|...)` pattern). The three golden fixtures hit
//     this exactly and were fixed via normalization (golden_test.go's
//     normalizeRuleSetVersion), but the command itself was DELIBERATELY left
//     unpatched (task-111's disclosed judgment call -- treating the command as
//     a reviewed artifact not to be re-touched under execution pressure). That
//     leaves the blind spot live for any FUTURE JSON-shaped fixture. This test
//     is the safety net: a second, independent, JSON-shaped detection pass,
//     asserting zero hits repo-wide.
//
//  2. TestRuleSetV2_ActiveContentFingerprintUnmutated -- none of RS-V2-03/04/07
//     detect a mutation to an EXISTING v2 rule's content (only the rule KEYS
//     are pinned by name, and only the 2 line-item rules' params are pinned
//     byte-for-byte). A future migration that "republished" by editing e.g.
//     vat-standard-rate's params directly on the already-active v2 row --
//     precisely the M3-04 immutability violation this whole story exists to
//     revert -- would pass every existing RS-V2-* test untouched. This test
//     hashes the full content (key/type/target/params/severity/when/message/
//     scope; enabled deliberately excluded -- see its doc comment) of every
//     rule under the active version and pins the digest.
//
//  3. TestRuleSetV2_NestedFlipsRestoreToOriginalActive -- RS-V2-10 proves a
//     SINGLE nested seedVersion(active) restores the pre-existing active row
//     by id. This test nests the same restore-by-id pattern TWO levels deep
//     (simulateActiveVersion inside simulateActiveVersion) to prove the LIFO
//     restore-by-id discipline survives compounding, not just one level --
//     the shape a future test author is most likely to reach for once a
//     second DB-backed fixture needs its own "swap the active version"
//     scratch space.
package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// 1. The F7 JSON-quote blind spot -- a second, independent detection pass.
// ---------------------------------------------------------------------

// TestRuleSetV2_JSONQuotedVersionPinNotPresent (QA-added adversarial):
// task-111 §b's detection command requires `[Vv]ersion` to be followed by
// (optional whitespace then) an operator. In JSON the KEY's closing quote
// sits between `version` and the `:` -- `"rule_set_version": 1` -- so that
// command returns 0 hits on exactly this shape. Independently reproduced: it
// misses `internal/validation/testdata/golden/*.json`'s PRE-fix content
// (`"rule_set_version": 1`, verified via `git show eef02dc:...` against this
// same pattern), which were genuine Category-A hits (produced by loadActive,
// a real DB read of the seeded rule-set).
//
// The golden fixtures were fixed by NORMALIZING the field out (a placeholder,
// not a swapped literal -- golden_test.go's normalizeRuleSetVersion), which
// is immune to this blind spot going forward. But the detection COMMAND
// itself was deliberately left unpatched (task-111's disclosed judgment
// call: a sixth ad-hoc regex revision under execution pressure is exactly
// how defects 1-4 were born). That leaves the blind spot live for any FUTURE
// JSON-shaped fixture this repo grows. This test is the tripwire for that
// case: a second, JSON-shaped detection pass (quoted key, colon, digits),
// run independently of and in addition to the story's own command (which
// this test does NOT modify or replace).
func TestRuleSetV2_JSONQuotedVersionPinNotPresent(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("bash", "-c",
		`grep -rnE '"[Rr]ule_?[Ss]et_?[Vv]ersion"[[:space:]]*:[[:space:]]*[0-9]+|"ruleSetVersion"[[:space:]]*:[[:space:]]*[0-9]+' . --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=vendor --exclude-dir=playwright-report`)
	cmd.Dir = root
	out, runErr := cmd.Output()
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			t.Fatalf("run the JSON-quoted detection pass: %v", runErr)
		}
		// grep exits 1 on no matches -- the desired outcome, not a Go-level error.
	}

	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		return // zero hits -- exactly what a healthy repo looks like for this pattern.
	}

	// THIS file's own doc comment above necessarily reproduces the pattern as
	// prose (illustrating the exact JSON shape this test guards against) --
	// excluded by name, the same way rule_set_v2_test.go's
	// TestRuleSetV2_DetectionCommandBaseline excludes itself for the identical
	// reason. Not part of the reviewed repo content this test polices.
	const selfFile = "internal/validation/rule_set_v2_qa_test.go"
	for _, line := range strings.Split(trimmed, "\n") {
		file, _, ok := strings.Cut(line, ":")
		if ok && strings.TrimPrefix(file, "./") == selfFile {
			continue
		}
		t.Errorf("JSON-quoted rule_set_version literal found: %q -- this is the F7 blind spot "+
			"(QA Debate Log, task-111): a future publish will silently pin this file's version "+
			"unless it uses a discovered/normalized value instead of a literal", line)
	}
}

// ---------------------------------------------------------------------
// 2. Full-content fingerprint of the active version -- catches an in-place
//    mutation to ANY existing rule, not just a key added/removed.
// ---------------------------------------------------------------------

// activeRuleContentFingerprint reads every rule under the active version,
// ordered by key for determinism, and hashes their CONTENT columns --
// key/type/target/params/severity/when/message/scope. `enabled` is
// deliberately EXCLUDED: it is a runtime toggle (the kill-switch,
// [v2-ships-as-authored]'s own framing draws this exact line), not content,
// and TestSeed_KillSwitch legitimately flips it mid-suite (restored via its
// own cleanup before any later test runs). Hashing it in would make this
// test flicker on kill-switch ordering rather than guard content.
func activeRuleContentFingerprint(t *testing.T) string {
	t.Helper()
	_, app := dbTestPools(t)
	ctx := context.Background()

	rows, err := app.Query(ctx,
		`SELECT r.key, r.type, r.target, r.params, r.severity, COALESCE(r."when", ''), r.message, r.scope
		   FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.is_active
		  ORDER BY r.key`)
	if err != nil {
		t.Fatalf("query active version's rule content: %v", err)
	}
	defer rows.Close()

	h := sha256.New()
	n := 0
	for rows.Next() {
		var key, typ, target, severity, when, message, scope string
		var params json.RawMessage
		if err := rows.Scan(&key, &typ, &target, &params, &severity, &when, &message, &scope); err != nil {
			t.Fatalf("scan active rule row: %v", err)
		}
		h.Write([]byte(key))
		h.Write([]byte{0})
		h.Write([]byte(typ))
		h.Write([]byte{0})
		h.Write([]byte(target))
		h.Write([]byte{0})
		h.Write(params)
		h.Write([]byte{0})
		h.Write([]byte(severity))
		h.Write([]byte{0})
		h.Write([]byte(when))
		h.Write([]byte{0})
		h.Write([]byte(message))
		h.Write([]byte{0})
		h.Write([]byte(scope))
		h.Write([]byte{'\n'})
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate active rule rows: %v", err)
	}
	if n == 0 {
		t.Fatal("0 rules under the active version -- nothing to fingerprint")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestRuleSetV2_ActiveContentFingerprintUnmutated (QA-added adversarial):
// pins a SHA-256 digest of every active-version rule's full content
// (key/type/target/params/severity/when/message/scope, sorted by key).
// RS-V2-03/04 only pin the active version's KEY SET (19 names); RS-V2-07
// only pins the 2 line-item rules' params byte-for-byte. Neither would
// notice a future migration that "republished" v3 by editing an EXISTING
// v2 rule's params/severity/message directly, in place -- exactly the M3-04
// immutability violation task-111 exists to revert (the line_rules.sql
// mutation of v1). A full-content hash closes that gap: any single-byte
// change to any rule's content changes the digest.
//
// If this test ever legitimately reds because content intentionally changed
// (a deliberate v3 publish that revises v2... no -- v2 must stay immutable
// too, by the same M3-04 guarantee this story restores. A red here should be
// read as "something mutated a rule that must never change" first, and a
// digest update only after confirming the change is NOT a content mutation
// of an already-published version).
func TestRuleSetV2_ActiveContentFingerprintUnmutated(t *testing.T) {
	const wantFingerprint = "321883213ed3a56cc0b9cf6c89a3279af086165e9fdfbdb4cff2ff82fad73790"

	got := activeRuleContentFingerprint(t)
	if got != wantFingerprint {
		t.Errorf("active rule-set content fingerprint = %s, want %s -- an active rule's content "+
			"(key/type/target/params/severity/when/message/scope) changed. If this is NOT an "+
			"in-place mutation of an already-published version's content (the exact defect "+
			"task-111 reverts), update wantFingerprint deliberately, with the diff reviewed -- "+
			"never silently", got, wantFingerprint)
	}
}

// ---------------------------------------------------------------------
// 3. Nested restore-by-id, two levels deep.
// ---------------------------------------------------------------------

// TestRuleSetV2_NestedFlipsRestoreToOriginalActive (QA-added adversarial):
// RS-V2-10 proves ONE nested seedVersion(active) restores the pre-existing
// active row by id (rule_set_v2_test.go's TestRuleSetV2_
// SeedVersionRestoresPreviousActiveByID). This test nests the SAME
// restore-by-id primitive (simulateActiveVersion) two levels deep -- the
// shape a future test author reaches for the moment a second DB-backed
// fixture needs its own "swap the active version" scratch space inside an
// already-swapped one -- and proves the one-active invariant still unwinds
// to the ORIGINAL row (not the intermediate one) once both levels' cleanups
// have run.
func TestRuleSetV2_NestedFlipsRestoreToOriginalActive(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	originalID, originalVersion := activeVersionRow(t, app)

	t.Run("level_one", func(t *testing.T) {
		level1ID, _ := simulateActiveVersion(t, super)

		var midActiveID string
		if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&midActiveID); err != nil {
			t.Fatalf("read active id at nesting level one: %v", err)
		}
		if midActiveID != level1ID {
			t.Fatalf("active id at nesting level one = %s, want %s (the level-one fixture itself)", midActiveID, level1ID)
		}

		t.Run("level_two", func(t *testing.T) {
			level2ID, _ := simulateActiveVersion(t, super)

			var innerActiveID string
			if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&innerActiveID); err != nil {
				t.Fatalf("read active id at nesting level two: %v", err)
			}
			if innerActiveID != level2ID {
				t.Fatalf("active id at nesting level two = %s, want %s (the level-two fixture itself)", innerActiveID, level2ID)
			}
		})

		// level_two's t.Cleanup has now run (subtests complete, and their
		// Cleanups fire, before the parent subtest's own body continues past
		// t.Run). The one-active slot must be back to level1ID, not
		// originalID -- restore-by-id must unwind ONE level at a time, not
		// jump straight to the outermost original.
		var afterLevelTwoID string
		if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&afterLevelTwoID); err != nil {
			t.Fatalf("read active id after level-two cleanup: %v", err)
		}
		if afterLevelTwoID != level1ID {
			t.Errorf("active id after level-two's cleanup = %s, want %s (level one's fixture -- "+
				"restore-by-id must unwind exactly one level, not skip straight to the original)",
				afterLevelTwoID, level1ID)
		}
	})

	// level_one's t.Cleanup has now run. The one-active slot must be back to
	// the ORIGINAL row this test started with.
	var finalActiveID string
	var finalActiveVersion int
	if err := super.QueryRow(ctx, `SELECT id, version FROM rule_set_versions WHERE is_active`).Scan(&finalActiveID, &finalActiveVersion); err != nil {
		t.Fatalf("read active id after both levels' cleanup: %v", err)
	}
	if finalActiveID != originalID {
		t.Errorf("active id after both nested fixtures' cleanup = %s, want %s (version=%d, the "+
			"row active before this test ran) -- two levels of restore-by-id must compose back to "+
			"the original, not leak an intermediate row as active", finalActiveID, originalID, originalVersion)
	}
}
