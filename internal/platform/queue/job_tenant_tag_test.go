package queue_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// argsOf builds the zero args value of w's Work method (so unexported types are reachable),
// sets its tenant_id field to tenant when non-empty, and returns it with its JSON.
// With fillStrings, every string field holds a UUID, so a tenant_id field added to a company-free type shows.
func argsOf(t *testing.T, w any, tenant string, fillStrings bool) (river.JobArgs, []byte) {
	t.Helper()
	m, ok := reflect.TypeOf(w).MethodByName("Work")
	if !ok || m.Type.NumIn() != 3 {
		t.Fatalf("%T has no Work(ctx, *river.Job[T]) method", w)
	}
	job := m.Type.In(2)
	if job.Kind() != reflect.Pointer || job.Elem().Kind() != reflect.Struct {
		t.Fatalf("%T Work arg 2 is %s, want *river.Job[T]", w, job)
	}
	argsField, ok := job.Elem().FieldByName("Args")
	if !ok {
		t.Fatalf("%s has no Args field", job)
	}
	v := reflect.New(argsField.Type).Elem()
	if fillStrings {
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).Kind() == reflect.String {
				v.Field(i).SetString(uuid.NewString())
			}
		}
	}
	if tenant != "" {
		set := false
		for i := 0; i < v.NumField(); i++ {
			if strings.Split(argsField.Type.Field(i).Tag.Get("json"), ",")[0] != "tenant_id" {
				continue
			}
			switch f := v.Field(i); f.Type() {
			case reflect.TypeOf(""):
				f.SetString(tenant)
			case reflect.TypeOf(uuid.UUID{}):
				f.Set(reflect.ValueOf(uuid.MustParse(tenant)))
			default:
				t.Fatalf("%s tenant_id has type %s", argsField.Type, f.Type())
			}
			set = true
		}
		if !set {
			t.Fatalf("%s has no tenant_id field", argsField.Type)
		}
	}
	ja, ok := v.Interface().(river.JobArgs)
	if !ok {
		t.Fatalf("%s is not river.JobArgs", argsField.Type)
	}
	raw, err := json.Marshal(ja)
	if err != nil {
		t.Fatalf("marshal %s: %v", argsField.Type, err)
	}
	return ja, raw
}

func TestJobTenantTag_CompanyKindsTagTheirTenant(t *testing.T) {
	cases := []struct {
		kind string
		w    any
	}{
		{"extraction_extract", &extraction.ExtractWorker{}}, // extractArgs.Kind()
		{"submission_submit", &submission.SubmitWorker{}},   // SubmitArgs.Kind()
		{"submission_poll", &submission.PollWorker{}},       // PollArgs.Kind()
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			tenant := uuid.NewString()
			ja, raw := argsOf(t, c.w, tenant, false)
			if ja.Kind() != c.kind {
				t.Fatalf("Kind() = %q, want %q", ja.Kind(), c.kind)
			}
			_, rec, want := sentrytest.Boot(t, "submission")
			queue.ReportFinalError(ja.Kind(), raw, errors.New("adapter: 503"))
			e := rec.One(t, want)
			if e.Tags["tenant_id"] != tenant || e.Tags["job_kind"] != c.kind {
				t.Errorf("tags = %v, want tenant_id %s and job_kind %s", e.Tags, tenant, c.kind)
			}
		})
	}
}

func TestJobTenantTag_CompanyFreeKindsCarryNoTenantTag(t *testing.T) {
	cases := []struct {
		kind string
		w    any
	}{
		{"contact_deliver", &notifications.DeliverWorker{}},       // DeliverArgs.Kind()
		{"demo_deal", &notifications.DemoDealWorker{}},            // DemoDealArgs.Kind()
		{"blog_subscriber_sync", &notifications.BlogSyncWorker{}}, // BlogSyncArgs.Kind()
		{"registrant_intake", &notifications.RegistrantWorker{}},  // RegistrantArgs.Kind()
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			ja, raw := argsOf(t, c.w, "", true)
			if ja.Kind() != c.kind {
				t.Fatalf("Kind() = %q, want %q", ja.Kind(), c.kind)
			}
			_, rec, want := sentrytest.Boot(t, "submission")
			queue.ReportFinalError(ja.Kind(), raw, errors.New("adapter: 503"))
			e := rec.One(t, want)
			if e.Tags["job_kind"] != c.kind {
				t.Errorf("job_kind = %q, want %q", e.Tags["job_kind"], c.kind)
			}
			if _, has := e.Tags["tenant_id"]; has {
				t.Errorf("tenant_id tag = %q, want none", e.Tags["tenant_id"])
			}
		})
	}
}
