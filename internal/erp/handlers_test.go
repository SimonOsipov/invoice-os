package erp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

const adminMsg = "only an admin can manage ERP connections"

type stubs struct {
	gateErr, listErr, connectErr, disconnectErr error
	created                                     bool
	conns                                       []Connection
	gateCalls, connectCalls, disconnectCalls    int
	gotEntity, gotConn                          string
	consentURL                                  string
	gotInput                                    ConnectInput

	mappingErr, saveMappingErr, lookupsErr, saveLookupsErr         error
	mappingView                                                    MappingView
	lookupsView                                                    LookupsView
	mappingCalls, saveMappingCalls, lookupsCalls, saveLookupsCalls int
	gotSystem, gotExternalKey                                      string
	gotFields                                                      []FieldChange
	gotLookups                                                     []Lookup

	overviewErr, marksErr error
	overview              Overview
	marks                 []DocumentMark
	gotInvoice            string
}

func (s *stubs) mux(log *slog.Logger) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/entities/{id}/erp-connections", ListHandler(
		func(_ context.Context, e string) ([]Connection, error) { s.gotEntity = e; return s.conns, s.listErr },
		func() []string { return nil }, log))
	m.HandleFunc("POST /v1/entities/{id}/erp-connections", ConnectHandler(
		func(context.Context) error { s.gateCalls++; return s.gateErr },
		func(_ context.Context, e string, in ConnectInput) (Connection, string, bool, error) {
			s.connectCalls++
			s.gotEntity, s.gotInput = e, in
			return Connection{ID: "c1"}, s.consentURL, s.created, s.connectErr
		}, log))
	m.HandleFunc("POST /v1/entities/{id}/erp-connections/{connection}/disconnect", DisconnectHandler(
		func(_ context.Context, e, c string) (Connection, error) {
			s.disconnectCalls++
			s.gotEntity, s.gotConn = e, c
			return Connection{ID: c, Status: "disconnected"}, s.disconnectErr
		}, log))
	gate := func(context.Context) error { s.gateCalls++; return s.gateErr }
	m.HandleFunc("GET /v1/entities/{id}/erp-mappings/{system}", MappingHandler(
		func(_ context.Context, e, sys string) (MappingView, error) {
			s.mappingCalls++
			s.gotEntity, s.gotSystem = e, sys
			return s.mappingView, s.mappingErr
		}, log))
	m.HandleFunc("PUT /v1/entities/{id}/erp-mappings/{system}", SaveMappingHandler(gate,
		func(_ context.Context, e, sys string, f []FieldChange) (MappingView, error) {
			s.saveMappingCalls++
			s.gotEntity, s.gotSystem, s.gotFields = e, sys, f
			return s.mappingView, s.saveMappingErr
		}, log))
	m.HandleFunc("GET /v1/entities/{id}/erp-lookups/{system}", LookupsHandler(
		func(_ context.Context, e, sys, k string) (LookupsView, error) {
			s.lookupsCalls++
			s.gotEntity, s.gotSystem, s.gotExternalKey = e, sys, k
			return s.lookupsView, s.lookupsErr
		}, log))
	m.HandleFunc("PUT /v1/entities/{id}/erp-lookups/{system}", SaveLookupsHandler(gate,
		func(_ context.Context, e, sys, k string, l []Lookup) (LookupsView, error) {
			s.saveLookupsCalls++
			s.gotEntity, s.gotSystem, s.gotExternalKey, s.gotLookups = e, sys, k, l
			return s.lookupsView, s.saveLookupsErr
		}, log))
	m.HandleFunc("GET /v1/entities/{id}/erp-connections/{connection}/overview", OverviewHandler(
		func(_ context.Context, e, c string) (Overview, error) {
			s.gotEntity, s.gotConn = e, c
			return s.overview, s.overviewErr
		}, log))
	m.HandleFunc("GET /v1/entities/{id}/invoices/{invoice}/erp-marks", MarksHandler(
		func(_ context.Context, e, inv string) ([]DocumentMark, error) {
			s.gotEntity, s.gotInvoice = e, inv
			return s.marks, s.marksErr
		}, log))
	return m
}

func do(m http.Handler, method, path, body string, authed bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if authed {
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: "u"}))
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}

func errText(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var b map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return b["error"]
}

const (
	listPath       = "/v1/entities/E/erp-connections"
	disconnectPath = "/v1/entities/E/erp-connections/C/disconnect"
	mappingPath    = "/v1/entities/E/erp-mappings/fake"
	lookupsPath    = "/v1/entities/E/erp-lookups/fake?external_key=co-1"
	overviewPath   = "/v1/entities/E/erp-connections/C/overview"
	marksPath      = "/v1/entities/E/invoices/I/erp-marks"
)

func TestHandlers_StatusForEveryStoreError(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		msg    string
	}{
		{db.ErrNoTenant, 401, "unauthorized"},
		{db.ErrNotActiveMember, 403, db.NotActiveMemberMessage},
		{db.ErrWorkspaceLocked, 403, db.WorkspaceLockedMessage},
		{ErrNotPermitted, 403, adminMsg},
		{ErrUnknownSystem, 400, "unknown system"},
		{ErrNoSuchRecord, 404, "not found"},
		{ErrAlreadyConnected, 409, "already connected"},
		{ErrAlreadyDisconnected, 409, "already disconnected"},
		{fmt.Errorf("wrapped: %w", ErrUnsupported), 501, "this system cannot connect yet"},
		{ErrKeylessPending, 409, "another connection of this system is already waiting for consent"},
		{fmt.Errorf("wrapped: %w", ErrNoSuchRecord), 404, "not found"},
		{invalidMapping("line_quantity", "a line field reads one []"), 400, "line_quantity: a line field reads one []"},
		{invalidLookup("external_key: required"), 400, "external_key: required"},
	} {
		var logs bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logs, nil))
		s := &stubs{connectErr: c.err, disconnectErr: c.err, listErr: c.err, mappingErr: c.err, saveMappingErr: c.err, lookupsErr: c.err, saveLookupsErr: c.err, overviewErr: c.err, marksErr: c.err}
		for name, w := range map[string]*httptest.ResponseRecorder{
			"connect":    do(s.mux(log), "POST", listPath, `{"system":"fake"}`, true),
			"disconnect": do(s.mux(nil), "POST", disconnectPath, "", true),
			"list":       do(s.mux(nil), "GET", listPath, "", true),
			"mapping":    do(s.mux(nil), "GET", mappingPath, "", true),
			"saveMap":    do(s.mux(nil), "PUT", mappingPath, "{}", true),
			"lookups":    do(s.mux(nil), "GET", lookupsPath, "", true),
			"saveLook":   do(s.mux(nil), "PUT", lookupsPath, "{}", true),
			"overview":   do(s.mux(nil), "GET", overviewPath, "", true),
			"marks":      do(s.mux(nil), "GET", marksPath, "", true),
		} {
			if w.Code != c.status || errText(t, w) != c.msg {
				t.Errorf("%v on %s: got %d %q, want %d %q", c.err, name, w.Code, errText(t, w), c.status, c.msg)
			}
		}
		if c.status != 500 && logs.Len() != 0 {
			t.Errorf("%v logged an error line: %s", c.err, logs.String())
		}
	}
}

func TestHandlers_UnknownErrorIs500WithoutLeaking(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	s := &stubs{connectErr: errors.New("pq: secret detail")}
	w := do(s.mux(log), "POST", listPath, `{"system":"fake","connection_id":"k"}`, true)
	if w.Code != 500 || errText(t, w) != "internal server error" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret detail") {
		t.Error("response leaks the error")
	}
	if !strings.Contains(logs.String(), "connect erp") {
		t.Errorf("log lacks the operation name: %s", logs.String())
	}
	boom := errors.New("pq: secret detail")
	s = &stubs{mappingErr: boom, saveMappingErr: boom, lookupsErr: boom, saveLookupsErr: boom}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"mapping":  do(s.mux(log), "GET", mappingPath, "", true),
		"saveMap":  do(s.mux(log), "PUT", mappingPath, "{}", true),
		"lookups":  do(s.mux(log), "GET", lookupsPath, "", true),
		"saveLook": do(s.mux(log), "PUT", lookupsPath, "{}", true),
	} {
		if w.Code != 500 || errText(t, w) != "internal server error" || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("%s: got %d %q", name, w.Code, w.Body.String())
		}
	}
}

func TestHandlers_NoIdentityIs401(t *testing.T) {
	s := &stubs{}
	m := s.mux(nil)
	for _, c := range [][3]string{{"GET", listPath, ""}, {"POST", listPath, "{}"}, {"POST", disconnectPath, ""},
		{"GET", mappingPath, ""}, {"PUT", mappingPath, "{}"}, {"GET", lookupsPath, ""}, {"PUT", lookupsPath, "{}"},
		{"GET", overviewPath, ""}, {"GET", marksPath, ""}} {
		if w := do(m, c[0], c[1], c[2], false); w.Code != 401 {
			t.Errorf("%s %s = %d, want 401", c[0], c[1], w.Code)
		}
	}
	if s.gateCalls+s.connectCalls+s.disconnectCalls+s.mappingCalls+s.saveMappingCalls+s.lookupsCalls+s.saveLookupsCalls != 0 {
		t.Error("store called without an identity")
	}
}

func TestHandlers_NonAdminIs403WhateverTheBody(t *testing.T) {
	big := `{"system":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+10) + `"}`
	for _, body := range []string{`{"system":"sage"}`, `{"system":"fake","connection_id":"x"}`, `{}`, "not json", big} {
		s := &stubs{gateErr: ErrNotPermitted, disconnectErr: ErrNotPermitted}
		w := do(s.mux(nil), "POST", listPath, body, true)
		if w.Code != 403 || errText(t, w) != adminMsg {
			t.Errorf("connect %.20q: %d %s", body, w.Code, w.Body.String())
		}
		if s.connectCalls != 0 {
			t.Errorf("connect stub called for %.20q", body)
		}
		for _, gerr := range []error{ErrNotPermitted, db.ErrNotActiveMember} {
			for _, p := range []string{mappingPath, lookupsPath} {
				s := &stubs{gateErr: gerr}
				w := do(s.mux(nil), "PUT", p, body, true)
				if w.Code != 403 || s.saveMappingCalls+s.saveLookupsCalls != 0 {
					t.Errorf("PUT %s %v %.20q: %d %s", p, gerr, body, w.Code, w.Body.String())
				}
			}
		}
	}
	s := &stubs{disconnectErr: ErrNotPermitted}
	if w := do(s.mux(nil), "POST", disconnectPath, "", true); w.Code != 403 || errText(t, w) != adminMsg {
		t.Errorf("disconnect: %d %s", w.Code, w.Body.String())
	}
	s = &stubs{gateErr: db.ErrNotActiveMember}
	if w := do(s.mux(nil), "POST", listPath, "{}", true); w.Code != 403 || errText(t, w) != db.NotActiveMemberMessage {
		t.Errorf("inactive: %d %s", w.Code, w.Body.String())
	}
}

func TestHandlers_ListCallsNoGate(t *testing.T) {
	s := &stubs{conns: []Connection{{ID: "a"}, {ID: "b"}}}
	w := do(s.mux(nil), "GET", listPath, "", true)
	var got listResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || len(got.Connections) != 2 || s.gateCalls != 0 {
		t.Fatalf("got %d %s gate=%d", w.Code, w.Body.String(), s.gateCalls)
	}
	s = &stubs{gateErr: ErrNotPermitted}
	for _, p := range []string{mappingPath, lookupsPath} {
		if w := do(s.mux(nil), "GET", p, "", true); w.Code != 200 || s.gateCalls != 0 {
			t.Errorf("GET %s: %d gate=%d", p, w.Code, s.gateCalls)
		}
	}
	s = &stubs{listErr: ErrNoSuchRecord}
	known := do(s.mux(nil), "GET", listPath, "", true)
	unknown := do(s.mux(nil), "GET", "/v1/entities/nope/erp-connections", "", true)
	if known.Code != 404 || errText(t, known) != "not found" || known.Body.String() != unknown.Body.String() {
		t.Errorf("404 bodies differ: %q vs %q", known.Body.String(), unknown.Body.String())
	}
	s = &stubs{listErr: db.ErrNotActiveMember}
	if w := do(s.mux(nil), "GET", listPath, "", true); w.Code != 403 || errText(t, w) != db.NotActiveMemberMessage {
		t.Errorf("inactive list: %d %s", w.Code, w.Body.String())
	}
}

func TestHandlers_ConnectRejectsBadBodies(t *testing.T) {
	big := `{"system":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+10) + `"}`
	for body, want := range map[string]struct {
		status int
		msg    string
	}{"not json": {400, "invalid request body"}, big: {413, "request body exceeds the size limit"}} {
		s := &stubs{}
		w := do(s.mux(nil), "POST", listPath, body, true)
		if w.Code != want.status || errText(t, w) != want.msg || s.gateCalls != 1 || s.connectCalls != 0 {
			t.Errorf("%.12q: %d %s gate=%d connect=%d", body, w.Code, w.Body.String(), s.gateCalls, s.connectCalls)
		}
	}
}

func TestHandlers_SavesRejectBadBodies(t *testing.T) {
	big := `{"fields":[{"target":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+10) + `"}]}`
	for _, p := range []string{mappingPath, lookupsPath} {
		for body, want := range map[string]struct {
			status int
			msg    string
		}{"not json": {400, "invalid request body"}, big: {413, "request body exceeds the size limit"}} {
			s := &stubs{}
			w := do(s.mux(nil), "PUT", p, body, true)
			if w.Code != want.status || errText(t, w) != want.msg || s.gateCalls != 1 || s.saveMappingCalls+s.saveLookupsCalls != 0 {
				t.Errorf("%s %.12q: %d %s gate=%d", p, body, w.Code, w.Body.String(), s.gateCalls)
			}
		}
	}
}

func keys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func TestHandlers_ResponseShapes(t *testing.T) {
	s := &stubs{}
	w := do(s.mux(nil), "GET", listPath, "", true)
	if got := w.Body.String(); !strings.Contains(got, `"connections":[]`) || !strings.Contains(got, `"systems":[]`) {
		t.Errorf("empty list = %s, want empty arrays", got)
	}
	if !slices.Equal(keys(t, w.Body.Bytes()), []string{"connections", "systems"}) {
		t.Errorf("list keys = %v", keys(t, w.Body.Bytes()))
	}
	now := time.Now()
	s.conns = []Connection{{ID: "a", LastSyncAt: &now}}
	var l struct {
		Connections []json.RawMessage `json:"connections"`
	}
	_ = json.Unmarshal(do(s.mux(nil), "GET", listPath, "", true).Body.Bytes(), &l)
	want := []string{"created_at", "entity_id", "external_key", "id", "last_sync_at", "status", "system", "updated_at"}
	if got := keys(t, l.Connections[0]); !slices.Equal(got, want) {
		t.Errorf("connection keys = %v, want %v", got, want)
	}
	s.created = true
	if w := do(s.mux(nil), "POST", listPath, `{"system":"fake"}`, true); w.Code != 201 {
		t.Errorf("created = %d", w.Code)
	}
	s.created = false
	if w := do(s.mux(nil), "POST", listPath, `{"system":"fake"}`, true); w.Code != 200 {
		t.Errorf("re-authorised = %d", w.Code)
	}
	if w := do(s.mux(nil), "POST", disconnectPath, "", true); w.Code != 200 {
		t.Errorf("disconnect = %d", w.Code)
	}
}

func TestHandlers_MappingAndLookupShapes(t *testing.T) {
	path := "invoice.number"
	s := &stubs{
		mappingView: MappingView{System: "fake", Fields: []FieldView{
			{Target: "a", Label: "A", SourcePath: &path, Origin: "client"},
			{Target: "b", Label: "B", Origin: "none"},
		}},
		lookupsView: LookupsView{System: "fake", ExternalKey: "co-1"},
	}
	w := do(s.mux(nil), "GET", mappingPath, "", true)
	if got := keys(t, w.Body.Bytes()); w.Code != 200 || !slices.Equal(got, []string{"fields", "system"}) {
		t.Errorf("mapping: %d keys %v", w.Code, got)
	}
	var m struct {
		Fields []json.RawMessage `json:"fields"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if got := keys(t, m.Fields[1]); !slices.Equal(got, []string{"default_path", "label", "line", "origin", "source_path", "target"}) {
		t.Errorf("field keys = %v", got)
	}
	if !strings.Contains(w.Body.String(), `"source_path":null`) || !strings.Contains(w.Body.String(), `"default_path":null`) {
		t.Errorf("absent paths not null: %s", w.Body.String())
	}
	w = do(s.mux(nil), "GET", lookupsPath, "", true)
	if got := keys(t, w.Body.Bytes()); !slices.Equal(got, []string{"external_key", "lookups", "system"}) || !strings.Contains(w.Body.String(), `"lookups":[]`) {
		t.Errorf("lookups: keys %v body %s", got, w.Body.String())
	}
	s.lookupsView.Lookups = []Lookup{{Kind: "item", ERPKey: "k"}}
	w = do(s.mux(nil), "GET", lookupsPath, "", true)
	var l struct {
		Lookups []json.RawMessage `json:"lookups"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &l)
	want := []string{"erp_key", "exemption_code", "hsn_code", "kind", "lga", "service_code", "state", "tax_category", "tin"}
	if got := keys(t, l.Lookups[0]); !slices.Equal(got, want) || !strings.Contains(string(l.Lookups[0]), `"tin":null`) {
		t.Errorf("entry keys = %v", got)
	}
}

func TestHandlers_PathValuesReachTheStore(t *testing.T) {
	s := &stubs{}
	do(s.mux(nil), "POST", disconnectPath, "", true)
	if s.gotEntity != "E" || s.gotConn != "C" {
		t.Errorf("got %q %q", s.gotEntity, s.gotConn)
	}
	s.gotEntity = ""
	do(s.mux(nil), "GET", listPath, "", true)
	if s.gotEntity != "E" {
		t.Errorf("list got entity %q, want E", s.gotEntity)
	}
	s = &stubs{mappingView: MappingView{System: "fake"}, lookupsView: LookupsView{System: "fake", ExternalKey: "co-1"}}
	w := do(s.mux(nil), "PUT", mappingPath, `{"fields":[{"target":"a","source_path":"x.y"},{"target":"b","source_path":null}]}`, true)
	if w.Code != 200 || s.gotEntity != "E" || s.gotSystem != "fake" || len(s.gotFields) != 2 ||
		s.gotFields[0].SourcePath == nil || *s.gotFields[0].SourcePath != "x.y" || s.gotFields[1].SourcePath != nil {
		t.Errorf("save mapping: %d %+v", w.Code, s.gotFields)
	}
	s.gotEntity = ""
	w = do(s.mux(nil), "PUT", lookupsPath, `{"lookups":[{"kind":"item","erp_key":"k"}]}`, true)
	if w.Code != 200 || s.gotEntity != "E" || s.gotSystem != "fake" || s.gotExternalKey != "co-1" || len(s.gotLookups) != 1 || s.gotLookups[0].ERPKey != "k" {
		t.Errorf("save lookups: %d %+v key %q", w.Code, s.gotLookups, s.gotExternalKey)
	}
	s.gotEntity, s.gotSystem, s.gotExternalKey = "", "", ""
	do(s.mux(nil), "GET", lookupsPath, "", true)
	if s.gotEntity != "E" || s.gotSystem != "fake" || s.gotExternalKey != "co-1" {
		t.Errorf("get lookups got %q %q %q", s.gotEntity, s.gotSystem, s.gotExternalKey)
	}
	s.gotSystem = ""
	do(s.mux(nil), "GET", mappingPath, "", true)
	if s.gotEntity != "E" || s.gotSystem != "fake" {
		t.Errorf("get mapping got %q %q", s.gotEntity, s.gotSystem)
	}
}

func TestMarksHandler_AnswersSnakeCaseAndEmptyArray(t *testing.T) {
	for name, none := range map[string][]DocumentMark{"nil": nil, "empty": {}} {
		s := &stubs{marks: none}
		w := do(s.mux(nil), "GET", marksPath, "", true)
		if got := strings.TrimSpace(w.Body.String()); w.Code != 200 || got != `{"marks":[]}` {
			t.Errorf("%s marks: %d %s, want 200 {\"marks\":[]}", name, w.Code, got)
		}
		if s.gotEntity != "E" || s.gotInvoice != "I" {
			t.Errorf("%s marks: the store got %q %q, want E and I", name, s.gotEntity, s.gotInvoice)
		}
	}

	changed := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	s := &stubs{marks: []DocumentMark{{ConnectionID: "c1", System: "fake", SourceDocID: "d1", State: "linked", ChangedAfterClearanceAt: &changed}}}
	w := do(s.mux(nil), "GET", marksPath, "", true)
	var body struct {
		Marks []json.RawMessage `json:"marks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || len(body.Marks) != 1 {
		t.Fatalf("one mark: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	want := []string{"changed_after_clearance_at", "connection_id", "deleted_in_erp_at", "differs_from_erp", "source_doc_id", "state", "system"}
	if got := keys(t, body.Marks[0]); !slices.Equal(got, want) {
		t.Errorf("mark keys = %v, want %v", got, want)
	}
	if got := string(body.Marks[0]); !strings.Contains(got, `"changed_after_clearance_at":"2026-11-20T09:30:00Z"`) || !strings.Contains(got, `"deleted_in_erp_at":null`) {
		t.Errorf("mark = %s, want the time as RFC 3339 and the unset time null", got)
	}
}

func TestOverviewHandler_NotFoundIs404(t *testing.T) {
	s := &stubs{overviewErr: ErrNoSuchRecord}
	w := do(s.mux(nil), "GET", overviewPath, "", true)
	if got := strings.TrimSpace(w.Body.String()); w.Code != 404 || got != `{"error":"not found"}` {
		t.Errorf("not found: %d %s, want 404 {\"error\":\"not found\"}", w.Code, got)
	}

	// The control: an answer is 200 with the contract's top-level and activity keys.
	s = &stubs{}
	w = do(s.mux(nil), "GET", overviewPath, "", true)
	if got := keys(t, w.Body.Bytes()); w.Code != 200 || !slices.Equal(got, []string{"activity", "connection", "held", "writebacks"}) {
		t.Fatalf("overview: %d keys %v, want 200 and connection, activity, writebacks, held", w.Code, got)
	}
	if s.gotEntity != "E" || s.gotConn != "C" {
		t.Errorf("the store got %q %q, want E and C", s.gotEntity, s.gotConn)
	}
	var body struct {
		Activity json.RawMessage `json:"activity"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if got := keys(t, body.Activity); !slices.Equal(got, []string{"counts", "documents", "since"}) {
		t.Errorf("activity keys = %v, want counts, documents, since", got)
	}
}

func TestHandlers_ConnectAnswersConsentURL(t *testing.T) {
	const consent = "https://gw.example/erp/fake/consent?state=s"
	want := []string{"consent_url", "created_at", "entity_id", "external_key", "id", "last_sync_at", "status", "system", "updated_at"}
	for _, c := range []struct {
		created bool
		status  int
	}{{true, 201}, {false, 200}} {
		s := &stubs{created: c.created, consentURL: consent}
		w := do(s.mux(nil), "POST", listPath, `{"system":"fake","connection_id":"X"}`, true)
		if w.Code != c.status {
			t.Errorf("created=%v: status %d, want %d", c.created, w.Code, c.status)
		}
		if got := keys(t, w.Body.Bytes()); !slices.Equal(got, want) {
			t.Errorf("created=%v: body keys = %v, want every Connection field and consent_url %v", c.created, got, want)
		}
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body["consent_url"] != consent || body["id"] != "c1" {
			t.Errorf("created=%v: body = %v, want consent_url %q on connection c1", c.created, body, consent)
		}
		if s.gotInput != (ConnectInput{System: "fake", ConnectionID: "X"}) {
			t.Errorf("created=%v: the store got %+v, want the system and connection_id of the body", c.created, s.gotInput)
		}
	}
	// A refusal answers no consent URL.
	s := &stubs{connectErr: ErrAlreadyConnected, consentURL: consent}
	w := do(s.mux(nil), "POST", listPath, `{"system":"fake","connection_id":"X"}`, true)
	if got := keys(t, w.Body.Bytes()); w.Code != 409 || !slices.Equal(got, []string{"error"}) {
		t.Errorf("already connected: %d with keys %v, want 409 and only error", w.Code, got)
	}
}
