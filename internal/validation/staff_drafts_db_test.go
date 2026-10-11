// Store.OpenDraft, AddDraftRule, EditDraftRule, RemoveDraftRule and PublishDraft against the dev DB.
// Every test calls removeDeskVersionsOnCleanup first; staff_audit_log is append-only, so rows are
// found by a fresh actor uuid. No t.Parallel().
package validation

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const draftNewKey = "e2e-x"

func mustValid(t *testing.T, key, typ, message string, params string) validated {
	t.Helper()
	yes := true
	v, err := validateDraftRule(key, draftRuleRequest{
		Type: typ, Target: "invoice.x", Params: []byte(params), Severity: "warning", Message: message, Enabled: &yes,
	})
	if err != nil {
		t.Fatalf("fixture rule %s: %v", key, err)
	}
	return v
}

func requiredRule(t *testing.T, key, message string) validated {
	return mustValid(t, key, "required", message, "")
}

func draftSetup(t *testing.T) (super, app *pgxpool.Pool, store *Store, actor string, ctx context.Context) {
	t.Helper()
	super, app = dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor = seedRulesStaffRow(t, super)
	return super, app, NewStore(app), actor, staffCtx(t, actor)
}

func isSealed(t *testing.T, super *pgxpool.Pool, id uuid.UUID) bool {
	t.Helper()
	var sealed bool
	if err := super.QueryRow(context.Background(), `SELECT sealed FROM rule_set_versions WHERE id = $1`, id).Scan(&sealed); err != nil {
		t.Fatalf("read sealed of %s: %v", id, err)
	}
	return sealed
}

func TestStaffDrafts_OpenThenOpenAgain(t *testing.T) {
	super, _, store, _, ctx := draftSetup(t)
	_, inForce := versionInForce(t, super)
	got, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatalf("OpenDraft: %v", err)
	}
	if got.FromVersion != inForce || got.Version <= inForce || got.RuleSetVersionID == uuid.Nil {
		t.Errorf("got %+v, want from_version %d and a newer version", got, inForce)
	}
	if _, err := store.OpenDraft(ctx); !errors.Is(err, ErrDraftExists) {
		t.Errorf("second open err = %v, want ErrDraftExists", err)
	}
	if n := unsealedCount(t, super); n != 1 {
		t.Errorf("%d drafts, want 1", n)
	}
}

func TestStaffDrafts_AddEditRemove(t *testing.T) {
	super, _, store, _, ctx := draftSetup(t)
	draft, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	added, err := store.AddDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "first"))
	if err != nil || added.RuleSetVersionID != draft.RuleSetVersionID || added.RuleSetVersion != draft.Version {
		t.Fatalf("AddDraftRule = %+v, %v", added, err)
	}
	if _, err := store.AddDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "second")); !errors.Is(err, ErrRuleInDraft) {
		t.Fatalf("add again err = %v, want ErrRuleInDraft", err)
	}
	if msg, _ := ruleMessage(t, super, draft.RuleSetVersionID.String(), draftNewKey); msg != "first" {
		t.Errorf("message after the refused add = %q, want first", msg)
	}
	edited, err := store.EditDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "third"))
	if err != nil || !edited.Changed {
		t.Fatalf("EditDraftRule = %+v, %v, want changed", edited, err)
	}
	if msg, _ := ruleMessage(t, super, draft.RuleSetVersionID.String(), draftNewKey); msg != "third" {
		t.Errorf("message after edit = %q, want third", msg)
	}
	if _, err := store.EditDraftRule(ctx, "e2e-absent", requiredRule(t, "e2e-absent", "m")); !errors.Is(err, ErrRuleNotInDraft) {
		t.Fatalf("edit absent err = %v, want ErrRuleNotInDraft", err)
	}
	if _, found := ruleMessage(t, super, draft.RuleSetVersionID.String(), "e2e-absent"); found {
		t.Error("an edit of an absent key inserted a row")
	}
	if _, err := store.RemoveDraftRule(ctx, draftNewKey); err != nil {
		t.Fatalf("RemoveDraftRule: %v", err)
	}
	v := draft.Version
	got, err := store.RulesOfVersion(ctx, &v)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.Rules {
		if r.Key == draftNewKey {
			t.Error("removed rule still listed")
		}
	}
	if _, err := store.RemoveDraftRule(ctx, draftNewKey); !errors.Is(err, ErrRuleNotInDraft) {
		t.Errorf("remove again err = %v, want ErrRuleNotInDraft", err)
	}
}

func TestStaffDrafts_NoDraftIsNotFound(t *testing.T) {
	_, _, store, _, ctx := draftSetup(t)
	eng := NewDefaultEngine()
	for name, err := range map[string]error{
		"add": func() error {
			_, e := store.AddDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "m"))
			return e
		}(),
		"edit": func() error {
			_, e := store.EditDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "m"))
			return e
		}(),
		"remove": func() error { _, e := store.RemoveDraftRule(ctx, draftNewKey); return e }(),
		"publish": func() error {
			_, e := store.PublishDraft(ctx, eng, time.Now().UTC().AddDate(0, 0, 1))
			return e
		}(),
	} {
		if !errors.Is(err, ErrNoDraft) {
			t.Errorf("%s err = %v, want ErrNoDraft", name, err)
		}
		if status, msg := draftsError(err); status != 404 || msg != "no draft" {
			t.Errorf("%s maps to %d %q, want 404 no draft", name, status, msg)
		}
	}
}

func TestStaffDrafts_PublishSealsAndDates(t *testing.T) {
	super, _, store, _, ctx := draftSetup(t)
	inForceBefore, _ := versionInForce(t, super)
	draft, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	far, _ := time.Parse("2006-01-02", deskFarDate)
	pub, err := store.PublishDraft(ctx, NewDefaultEngine(), far)
	if err != nil {
		t.Fatalf("PublishDraft: %v", err)
	}
	if pub.RuleSetVersionID != draft.RuleSetVersionID || pub.EffectiveFrom != deskFarDate || pub.RuleCount < 1 {
		t.Errorf("got %+v", pub)
	}
	list, err := store.Versions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, v := range list.Versions {
		if v.RuleSetVersionID == draft.RuleSetVersionID {
			found = true
			if v.State != "scheduled" || v.EffectiveFrom == nil || *v.EffectiveFrom != deskFarDate {
				t.Errorf("version row = %+v, want scheduled from %s", v, deskFarDate)
			}
		}
	}
	if !found {
		t.Error("published version missing from the list")
	}
	if id, _ := versionInForce(t, super); id != inForceBefore {
		t.Errorf("version in force changed from %s to %s", inForceBefore, id)
	}
}

func TestStaffDrafts_PublishRefusals(t *testing.T) {
	super, _, store, _, ctx := draftSetup(t)
	eng := NewDefaultEngine()
	draft, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().AddDate(0, 0, 5)
	stillDraft := func(step string) {
		t.Helper()
		if isSealed(t, super, draft.RuleSetVersionID) {
			t.Fatalf("%s: the draft was sealed", step)
		}
	}

	if _, err := store.PublishDraft(ctx, eng, time.Now().UTC().AddDate(0, 0, -1)); !errors.Is(err, ErrStartDateInPast) {
		t.Errorf("past date err = %v, want ErrStartDateInPast", err)
	}
	stillDraft("past date")

	if _, err := store.AddDraftRule(ctx, "e2e-fault", mustValid(t, "e2e-fault", "cel", "m", `{"expr":"invoice.missing.field == 1"}`)); err != nil {
		t.Fatal(err)
	}
	_, err = store.PublishDraft(ctx, eng, future)
	if !errors.Is(err, ErrDraftInvalid) || !strings.Contains(err.Error(), "the draft does not evaluate: ") || !strings.Contains(err.Error(), "e2e-fault") {
		t.Errorf("cel fault err = %v, want ErrDraftInvalid naming e2e-fault", err)
	}
	stillDraft("cel fault")
	if _, err := store.RemoveDraftRule(ctx, "e2e-fault"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.AddDraftRule(ctx, "e2e-list", mustValid(t, "e2e-list", "enum", "m", `{"list":"t-never-synced"}`)); err != nil {
		t.Fatal(err)
	}
	_, err = store.PublishDraft(ctx, eng, future)
	if !errors.Is(err, ErrDraftInvalid) || !strings.Contains(err.Error(), "t-never-synced") {
		t.Errorf("missing list err = %v, want ErrDraftInvalid naming the list", err)
	}
	stillDraft("missing list")
	if _, err := store.RemoveDraftRule(ctx, "e2e-list"); err != nil {
		t.Fatal(err)
	}

	for _, k := range ruleKeysOf(t, super, draft.RuleSetVersionID.String()) {
		if _, err := store.RemoveDraftRule(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.PublishDraft(ctx, eng, future)
	if !errors.Is(err, ErrDraftInvalid) || !strings.HasSuffix(err.Error(), "the draft has no rules") {
		t.Errorf("empty draft err = %v, want ErrDraftInvalid the draft has no rules", err)
	}
	stillDraft("empty draft")
}

func TestStaffDrafts_EveryWriteIsAudited(t *testing.T) {
	super, _, store, actor, ctx := draftSetup(t)
	draft, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.AddDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "a")))
	must(store.EditDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "b")))
	if r, err := store.EditDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "b")); err != nil || r.Changed {
		t.Fatalf("same edit = %+v, %v, want unchanged", r, err)
	}
	if _, err := store.AddDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "c")); !errors.Is(err, ErrRuleInDraft) {
		t.Fatalf("add existing err = %v", err)
	}
	if _, err := store.RemoveDraftRule(ctx, "e2e-absent"); !errors.Is(err, ErrRuleNotInDraft) {
		t.Fatalf("remove absent err = %v", err)
	}
	must(store.RemoveDraftRule(ctx, draftNewKey))
	far, _ := time.Parse("2006-01-02", deskFarDate)
	must(store.PublishDraft(ctx, NewDefaultEngine(), far))

	rows := auditRowsOf(t, super, actor)
	wantEvents := []string{"draft_opened", "rule_added", "rule_changed", "rule_removed", "published"}
	wantKeys := [][]string{
		{"from_version", "version"},
		{"key", "rule", "version"},
		{"key", "rule", "version"},
		{"key", "version"},
		{"effective_from", "rule_count", "version"},
	}
	if len(rows) != len(wantEvents) {
		t.Fatalf("%d audit rows, want %d: %+v", len(rows), len(wantEvents), rows)
	}
	for i, r := range rows {
		if r.Event != "validation.rule_set."+wantEvents[i] || r.Version != draft.RuleSetVersionID.String() {
			t.Errorf("row %d = %s on %s, want %s on %s", i, r.Event, r.Version, wantEvents[i], draft.RuleSetVersionID)
		}
		var keys []string
		for k := range r.Payload {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, wantKeys[i]) {
			t.Errorf("row %d payload keys = %v, want %v", i, keys, wantKeys[i])
		}
	}
	rule, _ := rows[2].Payload["rule"].(map[string]any)
	if rule["message"] != "b" {
		t.Errorf("rule_changed payload message = %v, want b", rule["message"])
	}
	if rows[4].Payload["effective_from"] != deskFarDate {
		t.Errorf("published payload = %v", rows[4].Payload)
	}
}

func TestStaffDrafts_AuditFailureRollsBack(t *testing.T) {
	super, _, store, actor, ctx := draftSetup(t)
	orig := recordStaff
	t.Cleanup(func() { recordStaff = orig })
	boom := errors.New("audit down")
	recordStaff = func(context.Context, pgx.Tx, uuid.UUID, uuid.UUID, string, any) error { return boom }

	if _, err := store.OpenDraft(ctx); !errors.Is(err, boom) {
		t.Fatalf("open err = %v, want the audit error", err)
	}
	if n := unsealedCount(t, super); n != 0 {
		t.Fatalf("%d drafts after a failed audit, want 0", n)
	}

	recordStaff = orig
	draft, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordStaff = func(context.Context, pgx.Tx, uuid.UUID, uuid.UUID, string, any) error { return boom }
	if _, err := store.AddDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "m")); !errors.Is(err, boom) {
		t.Fatalf("add err = %v, want the audit error", err)
	}
	if _, found := ruleMessage(t, super, draft.RuleSetVersionID.String(), draftNewKey); found {
		t.Error("the rule survived a failed audit write")
	}
	far, _ := time.Parse("2006-01-02", deskFarDate)
	if _, err := store.PublishDraft(ctx, NewDefaultEngine(), far); !errors.Is(err, boom) {
		t.Fatalf("publish err = %v, want the audit error", err)
	}
	if isSealed(t, super, draft.RuleSetVersionID) {
		t.Error("the draft was sealed despite a failed audit write")
	}
	if n := len(auditRowsOf(t, super, actor)); n != 1 {
		t.Errorf("%d audit rows, want only the draft_opened row", n)
	}
}

func TestStaffDrafts_PublishHoldsTheDraftLock(t *testing.T) {
	super, _, store, _, ctx := draftSetup(t)
	draft, err := store.OpenDraft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := super.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(context.Background(), `SELECT pg_advisory_xact_lock(hashtext('rule_set_versions:draft'))`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		far, _ := time.Parse("2006-01-02", deskFarDate)
		_, err := store.PublishDraft(ctx, NewDefaultEngine(), far)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("publish finished while the lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := holder.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("publish after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("publish did not finish after the lock was released")
	}
	if !isSealed(t, super, draft.RuleSetVersionID) {
		t.Error("draft not sealed")
	}
}

func TestStaffDrafts_NonUUIDSubjectIs403(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	ctx := staffCtx(t, "not-a-uuid")
	store := NewStore(app)
	count := func() (versions, audits int) {
		t.Helper()
		if err := super.QueryRow(context.Background(),
			`SELECT (SELECT count(*) FROM rule_set_versions), (SELECT count(*) FROM staff_audit_log)`).Scan(&versions, &audits); err != nil {
			t.Fatal(err)
		}
		return
	}
	v0, a0 := count()
	far, _ := time.Parse("2006-01-02", deskFarDate)
	for name, err := range map[string]error{
		"open": func() error { _, e := store.OpenDraft(ctx); return e }(),
		"add": func() error {
			_, e := store.AddDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "m"))
			return e
		}(),
		"edit": func() error {
			_, e := store.EditDraftRule(ctx, draftNewKey, requiredRule(t, draftNewKey, "m"))
			return e
		}(),
		"remove":  func() error { _, e := store.RemoveDraftRule(ctx, draftNewKey); return e }(),
		"publish": func() error { _, e := store.PublishDraft(ctx, NewDefaultEngine(), far); return e }(),
	} {
		if !errors.Is(err, db.ErrNotStaff) {
			t.Errorf("%s err = %v, want db.ErrNotStaff", name, err)
		}
	}
	if v1, a1 := count(); v1 != v0 || a1 != a0 {
		t.Errorf("rows changed: versions %d to %d, audit %d to %d", v0, v1, a0, a1)
	}
}

func TestStaffDrafts_NonRulesActorIsNotStaff(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := uuid.NewString()
	if _, err := super.Exec(context.Background(), `INSERT INTO staff_members (user_id, rules_role) VALUES ($1, false)`, actor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM staff_members WHERE user_id = $1`, actor) })
	if _, err := NewStore(app).OpenDraft(staffCtx(t, actor)); !errors.Is(err, db.ErrNotStaff) {
		t.Fatalf("err = %v, want db.ErrNotStaff (42501 mapped)", err)
	}
	if n := unsealedCount(t, super); n != 0 {
		t.Errorf("%d drafts, want 0", n)
	}
}

// Every rule v4 ships must pass the desk's own checks, so a draft copied from v4 is editable.
func TestStaffDrafts_EveryV4RuleValidates(t *testing.T) {
	_, app, store, _, ctx := draftSetup(t)
	_ = app
	four := 4
	got, err := store.RulesOfVersion(ctx, &four)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) == 0 {
		t.Fatal("v4 has no rules")
	}
	for _, r := range got.Rules {
		if _, err := validateDraftRule(r.Key, draftRuleRequest{
			Type: r.Type, Target: r.Target, Params: r.Params, Severity: r.Severity, When: r.When, Message: r.Message, Enabled: &r.Enabled,
		}); err != nil {
			t.Errorf("v4 rule %s: %v", r.Key, err)
		}
	}
}
