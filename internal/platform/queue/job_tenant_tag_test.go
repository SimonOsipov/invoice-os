package queue_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

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

// argsOf builds the zero args value of w's Work method (so unexported types are reachable),
// fills every string and uuid field with id, and returns it with its JSON.
// A tenant_id anywhere that json flattens then carries id, so the tag test follows the real encoding.
func argsOf(t *testing.T, w any, id uuid.UUID) (river.JobArgs, []byte) {
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
	fillIDs(v, id)
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
			id := uuid.New()
			tenant := id.String()
			ja, raw := argsOf(t, c.w, id)
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
			ja, raw := argsOf(t, c.w, uuid.New())
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
