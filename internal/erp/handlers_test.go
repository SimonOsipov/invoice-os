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
}

func (s *stubs) mux(log *slog.Logger) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/entities/{id}/erp-connections", ListHandler(
		func(context.Context, string) ([]Connection, error) { return s.conns, s.listErr },
		func() []string { return nil }, log))
	m.HandleFunc("POST /v1/entities/{id}/erp-connections", ConnectHandler(
		func(context.Context) error { s.gateCalls++; return s.gateErr },
		func(_ context.Context, e string, _ ConnectInput) (Connection, bool, error) {
			s.connectCalls++
			s.gotEntity = e
			return Connection{ID: "c1"}, s.created, s.connectErr
		}, log))
	m.HandleFunc("POST /v1/entities/{id}/erp-connections/{connection}/disconnect", DisconnectHandler(
		func(_ context.Context, e, c string) (Connection, error) {
			s.disconnectCalls++
			s.gotEntity, s.gotConn = e, c
			return Connection{ID: c, Status: "disconnected"}, s.disconnectErr
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
		{ErrUnknownExternalKey, 400, "unknown external_key"},
		{ErrNotFound, 404, "not found"},
		{ErrAlreadyConnected, 409, "already connected"},
		{ErrAlreadyDisconnected, 409, "already disconnected"},
		{fmt.Errorf("wrapped: %w", ErrNotFound), 404, "not found"},
	} {
		s := &stubs{connectErr: c.err, disconnectErr: c.err, listErr: c.err}
		for name, w := range map[string]*httptest.ResponseRecorder{
			"connect":    do(s.mux(nil), "POST", listPath, `{"system":"fake"}`, true),
			"disconnect": do(s.mux(nil), "POST", disconnectPath, "", true),
			"list":       do(s.mux(nil), "GET", listPath, "", true),
		} {
			if w.Code != c.status || errText(t, w) != c.msg {
				t.Errorf("%v on %s: got %d %q, want %d %q", c.err, name, w.Code, errText(t, w), c.status, c.msg)
			}
		}
	}
}

func TestHandlers_UnknownErrorIs500WithoutLeaking(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	s := &stubs{connectErr: errors.New("pq: secret detail")}
	w := do(s.mux(log), "POST", listPath, `{"system":"fake","external_key":"k"}`, true)
	if w.Code != 500 || errText(t, w) != "internal server error" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret detail") {
		t.Error("response leaks the error")
	}
	if !strings.Contains(logs.String(), "connect erp") {
		t.Errorf("log lacks the operation name: %s", logs.String())
	}
}

func TestHandlers_NoIdentityIs401(t *testing.T) {
	s := &stubs{}
	m := s.mux(nil)
	for _, c := range [][3]string{{"GET", listPath, ""}, {"POST", listPath, "{}"}, {"POST", disconnectPath, ""}} {
		if w := do(m, c[0], c[1], c[2], false); w.Code != 401 {
			t.Errorf("%s %s = %d, want 401", c[0], c[1], w.Code)
		}
	}
	if s.gateCalls+s.connectCalls+s.disconnectCalls != 0 {
		t.Error("store called without an identity")
	}
}

func TestHandlers_NonAdminIs403WhateverTheBody(t *testing.T) {
	big := `{"system":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+10) + `"}`
	for _, body := range []string{`{"system":"sage"}`, `{"system":"fake","external_key":"x"}`, `{}`, "not json", big} {
		s := &stubs{gateErr: ErrNotPermitted, disconnectErr: ErrNotPermitted}
		w := do(s.mux(nil), "POST", listPath, body, true)
		if w.Code != 403 || errText(t, w) != adminMsg {
			t.Errorf("connect %.20q: %d %s", body, w.Code, w.Body.String())
		}
		if s.connectCalls != 0 {
			t.Errorf("connect stub called for %.20q", body)
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
	s = &stubs{listErr: ErrNotFound}
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

func TestHandlers_PathValuesReachTheStore(t *testing.T) {
	s := &stubs{}
	do(s.mux(nil), "POST", disconnectPath, "", true)
	if s.gotEntity != "E" || s.gotConn != "C" {
		t.Errorf("got %q %q", s.gotEntity, s.gotConn)
	}
}
