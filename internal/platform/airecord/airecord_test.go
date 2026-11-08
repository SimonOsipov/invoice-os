package airecord

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

type spy struct{ got []Call }

func (s *spy) sink(c Call) { s.got = append(s.got, c) }

func tenantCtx(tenant string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: "u1", TenantID: tenant})
}

func newLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func wantOneWarn(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("want exactly one log line, got %q", buf.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	if m["level"] != "WARN" || m["msg"] != "ai call not recorded: no tenant" ||
		m["purpose"] != "document" || m["client"] != "ai" {
		t.Errorf("unexpected log line: %v", m)
	}
	if _, has := m["tenant_id"]; has {
		t.Errorf("log line carries tenant_id: %v", m)
	}
}

func TestRecord_ACallThatSentNothingIsNotRecorded(t *testing.T) {
	var s spy
	logger, buf := newLogger()
	Record(tenantCtx("T1"), s.sink, logger, Call{Client: "ai", Purpose: "document", Attempts: 0, Outcome: "off"})
	if len(s.got) != 0 || buf.Len() != 0 {
		t.Errorf("sink calls=%d log=%q, want none", len(s.got), buf.String())
	}
	Record(context.Background(), s.sink, logger, Call{Client: "ai", Purpose: "document", Attempts: 0, Outcome: "off"})
	if len(s.got) != 0 || buf.Len() != 0 {
		t.Errorf("no identity: sink calls=%d log=%q, want none", len(s.got), buf.String())
	}
}

func TestRecord_ARefusedRequestWithNoAttemptIsNotRecorded(t *testing.T) {
	var s spy
	Record(tenantCtx("T1"), s.sink, nil, Call{Client: "jev", Purpose: "value_check", Attempts: 0, Outcome: "skipped_refused"})
	if len(s.got) != 0 {
		t.Errorf("sink called %d times", len(s.got))
	}
}

func TestRecord_HandsTheSinkTheCallersTenant(t *testing.T) {
	var s spy
	in := Call{Client: "ai", Purpose: "document", Model: "m", InputTokens: 10, OutputTokens: 20,
		Cost: 0.0012, LatencyMS: 345, Attempts: 2, Outcome: "ok"}
	Record(tenantCtx("T1"), s.sink, nil, in)
	if len(s.got) != 1 {
		t.Fatalf("sink called %d times, want 1", len(s.got))
	}
	want := in
	want.TenantID = "T1"
	if s.got[0] != want {
		t.Errorf("got %+v, want %+v", s.got[0], want)
	}
}

func TestCall_HasExactlyTheDesignedFields(t *testing.T) {
	typ := reflect.TypeOf(Call{})
	var got []string
	for i := range typ.NumField() {
		got = append(got, typ.Field(i).Name)
	}
	want := []string{"TenantID", "Client", "Purpose", "Model", "InputTokens", "OutputTokens", "Cost", "LatencyMS", "Attempts", "Outcome", "PublicCheck"}
	if !slices.Equal(got, want) {
		t.Errorf("fields %v, want %v", got, want)
	}
}

func TestRecord_AFakeCallWithOneAttemptIsRecorded(t *testing.T) {
	var s spy
	Record(tenantCtx("T1"), s.sink, nil, Call{Client: "jev", Purpose: "mapping_check", Attempts: 1, Outcome: "fake"})
	if len(s.got) != 1 {
		t.Errorf("sink called %d times, want 1", len(s.got))
	}
}

func TestRecord_EmptyTenantWarnsOnceAndSkips(t *testing.T) {
	var s spy
	logger, buf := newLogger()
	Record(tenantCtx(""), s.sink, logger, Call{Client: "ai", Purpose: "document", Attempts: 1, Outcome: "ok"})
	if len(s.got) != 0 {
		t.Errorf("sink called %d times", len(s.got))
	}
	wantOneWarn(t, buf)
}

func TestRecord_ATenantlessCallerIsNotATenant(t *testing.T) {
	var s spy
	logger, buf := newLogger()
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: "u1", TenantID: "T1"})
	Record(ctx, s.sink, logger, Call{Client: "ai", Purpose: "document", Attempts: 1, Outcome: "ok"})
	if len(s.got) != 0 {
		t.Errorf("sink called %d times", len(s.got))
	}
	wantOneWarn(t, buf)
}

func TestRecord_NilSinkIsANoOp(t *testing.T) {
	logger, buf := newLogger()
	Record(context.Background(), nil, logger, Call{Client: "ai", Purpose: "document", Attempts: 1})
	if buf.Len() != 0 {
		t.Errorf("log = %q, want empty", buf.String())
	}
}

func TestRecord_NilLoggerWithNoTenantDoesNotPanic(t *testing.T) {
	var s spy
	Record(context.Background(), s.sink, nil, Call{Client: "ai", Purpose: "document", Attempts: 1})
	if len(s.got) != 0 {
		t.Errorf("sink called %d times", len(s.got))
	}
}

func TestRecord_NegativeVendorUsageReachesTheSinkAsZero(t *testing.T) {
	var s spy
	Record(tenantCtx("T1"), s.sink, nil, Call{Client: "ai", Purpose: "document", Model: "m",
		InputTokens: -1, OutputTokens: -5, Cost: -0.001, LatencyMS: 7, Attempts: 1, Outcome: "ok"})
	if len(s.got) != 1 {
		t.Fatalf("sink called %d times, want 1", len(s.got))
	}
	want := Call{TenantID: "T1", Client: "ai", Purpose: "document", Model: "m", LatencyMS: 7, Attempts: 1, Outcome: "ok"}
	if s.got[0] != want {
		t.Errorf("got %+v, want %+v", s.got[0], want)
	}
}

func TestRecord_ZeroAndTinyUsagePassUnchanged(t *testing.T) {
	var s spy
	Record(tenantCtx("T1"), s.sink, nil, Call{Client: "ai", Purpose: "document",
		InputTokens: 0, OutputTokens: 1, Cost: 1e-9, Attempts: 1})
	if len(s.got) != 1 {
		t.Fatalf("sink called %d times, want 1", len(s.got))
	}
	if g := s.got[0]; g.InputTokens != 0 || g.OutputTokens != 1 || g.Cost != 1e-9 {
		t.Errorf("got %+v", g)
	}
}

func okCall() Call {
	return Call{Client: "ai", Purpose: "document", Model: "m", LatencyMS: 7, Attempts: 1, Outcome: "ok"}
}

func TestRecord_AMarkedTenantlessCallReachesTheSinkAsAPublicCheck(t *testing.T) {
	var s spy
	logger, buf := newLogger()
	Record(WithPublicCheck(context.Background()), s.sink, logger, okCall())
	if len(s.got) != 1 {
		t.Fatalf("sink called %d times, want 1 (log: %q)", len(s.got), buf.String())
	}
	want := okCall()
	want.PublicCheck = true
	if s.got[0] != want {
		t.Errorf("got %+v, want %+v (empty TenantID, PublicCheck true)", s.got[0], want)
	}
	if buf.Len() != 0 {
		t.Errorf("a marked call logged %q, want nothing", buf.String())
	}
}

func TestRecord_AnUnmarkedTenantlessCallIsStillDropped(t *testing.T) {
	var s spy
	logger, buf := newLogger()
	// The positive half: the same call under a mark is recorded, so the drop below is the mark's absence.
	Record(WithPublicCheck(context.Background()), s.sink, nil, okCall())
	if len(s.got) != 1 {
		t.Fatalf("marked call: sink called %d times, want 1", len(s.got))
	}
	s.got = nil
	Record(context.Background(), s.sink, logger, okCall())
	if len(s.got) != 0 {
		t.Errorf("unmarked call: sink called %d times, want 0", len(s.got))
	}
	wantOneWarn(t, buf)
}

func TestRecord_ATenantIdentityWinsOverTheMark(t *testing.T) {
	var s spy
	// The positive half: the mark alone makes a public check.
	Record(WithPublicCheck(context.Background()), s.sink, nil, okCall())
	if len(s.got) != 1 || !s.got[0].PublicCheck {
		t.Fatalf("marked call: got %+v, want one PublicCheck call", s.got)
	}
	s.got = nil
	Record(WithPublicCheck(tenantCtx("T1")), s.sink, nil, okCall())
	if len(s.got) != 1 {
		t.Fatalf("sink called %d times, want 1", len(s.got))
	}
	want := okCall()
	want.TenantID = "T1"
	if s.got[0] != want {
		t.Errorf("got %+v, want %+v (tenant T1, PublicCheck false)", s.got[0], want)
	}
}

func TestRecord_AMarkedCallThatSentNothingIsNotRecorded(t *testing.T) {
	var s spy
	logger, buf := newLogger()
	// The positive half: the same marked context records a call that sent something.
	Record(WithPublicCheck(context.Background()), s.sink, logger, okCall())
	if len(s.got) != 1 {
		t.Fatalf("marked call with an attempt: sink called %d times, want 1", len(s.got))
	}
	s.got = nil
	c := okCall()
	c.Attempts = 0
	c.Outcome = "off"
	Record(WithPublicCheck(context.Background()), s.sink, logger, c)
	if len(s.got) != 0 || buf.Len() != 0 {
		t.Errorf("marked call with no attempt: sink calls=%d log=%q, want none", len(s.got), buf.String())
	}
}

func TestRecord_AMarkedCallClampsNegativeUsage(t *testing.T) {
	var s spy
	c := okCall()
	c.InputTokens, c.OutputTokens, c.Cost = -5, -2, -1
	Record(WithPublicCheck(context.Background()), s.sink, nil, c)
	if len(s.got) != 1 {
		t.Fatalf("sink called %d times, want 1", len(s.got))
	}
	if g := s.got[0]; g.InputTokens != 0 || g.OutputTokens != 0 || g.Cost != 0 || !g.PublicCheck {
		t.Errorf("got %+v, want usage clamped to 0 on a PublicCheck call", g)
	}
}

// A tenantless caller (WithTenantlessCaller) carries a tenant id that is not a tenant binding;
// under a mark the call is a public check and the id never reaches the row.
func TestRecord_AMarkedCallFromATenantlessCallerIsAPublicCheckWithNoTenant(t *testing.T) {
	var s spy
	logger, buf := newLogger()
	base := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: "u1", TenantID: "T1"})
	Record(base, s.sink, logger, okCall())
	if len(s.got) != 0 {
		t.Fatalf("unmarked tenantless caller: sink called %d times, want 0", len(s.got))
	}
	wantOneWarn(t, buf)

	Record(WithPublicCheck(base), s.sink, nil, okCall())
	if len(s.got) != 1 {
		t.Fatalf("marked tenantless caller: sink called %d times, want 1", len(s.got))
	}
	if g := s.got[0]; g.TenantID != "" || !g.PublicCheck {
		t.Errorf("got %+v, want TenantID empty and PublicCheck true", g)
	}
}
