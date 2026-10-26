package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// companyFreeKinds: job kinds that run for no company, each with its reason.
// Lives here because the real bundle (contactWorkers, workerBundle) is built in package main.
var companyFreeKinds = map[string]string{
	"contact_deliver":      "args hold a contact email, destination and version; contacts are global",
	"demo_deal":            "args hold a demo requester's email, name and company text; no tenant",
	"blog_subscriber_sync": "args hold a subscriber email and version; newsletter tables are global",
	"registrant_intake":    "args hold a registrant's user id and workspace name; registrant_handoffs is global",
	"person_erase":         "args hold a user id and a generation; person_erasures is global",
	"person_erase_by_id":   "args hold a user id only; it touches no tenant table",
}

// registeredJobArgs maps kind to args type for every worker in b.
// ceiling: reads River's unexported workersMap, which has no public enumeration; a rename fails here.
func registeredJobArgs(t *testing.T, b *river.Workers) map[string]reflect.Type {
	t.Helper()
	m := reflect.ValueOf(b).Elem().FieldByName("workersMap")
	if !m.IsValid() || m.Kind() != reflect.Map || m.Len() == 0 {
		t.Fatal("river.Workers.workersMap is missing or empty: the guard cannot read the registry")
	}
	out := map[string]reflect.Type{}
	for _, k := range m.MapKeys() {
		out[k.String()] = m.MapIndex(k).FieldByName("jobArgs").Elem().Type()
	}
	return out
}

// fillIDs sets every settable string and uuid.UUID leaf to id, descending through struct fields.
// Pointers stay nil, as encoding/json leaves a nil embedded pointer out.
func fillIDs(v reflect.Value, id uuid.UUID) {
	switch {
	case v.Type() == reflect.TypeOf(uuid.UUID{}):
		if v.CanSet() {
			v.Set(reflect.ValueOf(id))
		}
	case v.Kind() == reflect.String:
		if v.CanSet() {
			v.SetString(id.String())
		}
	case v.Kind() == reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillIDs(v.Field(i), id)
		}
	}
}

// carriesTenantID encodes a filled args value and reads the top-level tenant_id as tenantOf does.
func carriesTenantID(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	id := uuid.MustParse("6f1c2a3e-8b4d-4c5e-9a7f-0123456789ab")
	v := reflect.New(t).Elem()
	fillIDs(v, id)
	raw, err := json.Marshal(v.Interface())
	if err != nil {
		return false
	}
	var a struct {
		TenantID string `json:"tenant_id"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return false
	}
	got, err := uuid.Parse(a.TenantID)
	return err == nil && got == id
}

func tenantTagProblems(kinds map[string]reflect.Type, free map[string]string) []string {
	var problems []string
	for _, kind := range slices.Sorted(maps.Keys(kinds)) {
		if _, ok := free[kind]; !ok && !carriesTenantID(kinds[kind]) {
			problems = append(problems, fmt.Sprintf("job kind %s has no tenant_id: add tenant_id to its args, or add it to the company-free job kinds with a reason", kind))
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(free)) {
		if _, ok := kinds[kind]; !ok {
			problems = append(problems, fmt.Sprintf("company-free job kind %s is not a registered job kind: remove it from the list", kind))
		} else if strings.TrimSpace(free[kind]) == "" {
			problems = append(problems, fmt.Sprintf("company-free job kind %s has no reason", kind))
		}
	}
	return problems
}

// realRegistry builds the bundle through the production constructors, every optional worker on.
func realRegistry(t *testing.T) map[string]reflect.Type {
	t.Helper()
	cw := contactWorkers(nil, notifications.ModeFake, notifications.FakeHubSpot{}, notifications.FakeResend{},
		notifications.NewsletterWiring{Enabled: true}, slog.Default())
	if cw == nil {
		t.Fatal("contactWorkers returned nil in fake mode")
	}
	v := reflect.ValueOf(cw).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Fatalf("contactWorkerSet.%s is nil: the guard would not see its job kind", v.Type().Field(i).Name)
		}
	}
	b := workerBundle(&submission.SubmitWorker{}, &submission.PollWorker{}, &extraction.ExtractWorker{}, &notifications.RegistrantWorker{}, cw, &notifications.EraseWorker{}, &notifications.EraseByIDWorker{})
	kinds := registeredJobArgs(t, b)
	for _, k := range []string{"submission_submit", "submission_poll", "extraction_extract"} {
		if _, ok := kinds[k]; !ok {
			t.Fatalf("registry lacks %s: the guard read the wrong bundle", k)
		}
	}
	return kinds
}

func TestJobTenantGuard_RealBundleIsClean(t *testing.T) {
	if problems := tenantTagProblems(realRegistry(t), companyFreeKinds); len(problems) != 0 {
		t.Fatalf("job tenant guard:\n%s", strings.Join(problems, "\n"))
	}
}

func TestJobTenantGuard_RealRegistryFailsWhenAFreeKindIsUnlisted(t *testing.T) {
	free := maps.Clone(companyFreeKinds)
	delete(free, "demo_deal")
	want := "job kind demo_deal has no tenant_id: add tenant_id to its args, or add it to the company-free job kinds with a reason"
	if got := tenantTagProblems(realRegistry(t), free); !slices.Equal(got, []string{want}) {
		t.Fatalf("problems = %q, want exactly %q", got, want)
	}
}
