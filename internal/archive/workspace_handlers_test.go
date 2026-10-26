package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	exportNotPermittedMsg = "only an admin can download the workspace export"
	exportRunningMsg      = "an export of this workspace is already running"
	exportBusyMsg         = "another workspace export is running; try again in a few minutes"
)

type workspaceSpy struct {
	calls int
	fn    func(w io.Writer, onStart func(string)) error
}

func (s *workspaceSpy) Assemble(_ context.Context, w io.Writer, onStart func(string)) error {
	s.calls++
	if s.fn == nil {
		return nil
	}
	return s.fn(w, onStart)
}

func workspaceRequest(withIdentity bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/workspace/export", nil)
	if withIdentity {
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: uuid.NewString(), TenantID: testTenantID}))
	}
	return r
}

// serveWorkspace runs the handler and reports whether it aborted the connection.
func serveWorkspace(h http.Handler, r *http.Request) (rec *httptest.ResponseRecorder, aborted bool) {
	rec = httptest.NewRecorder()
	defer func() {
		if v := recover(); v != nil {
			if v != http.ErrAbortHandler {
				panic(v)
			}
			aborted = true
		}
	}()
	h.ServeHTTP(rec, r)
	return rec, false
}

func TestWorkspaceExportHandler_MapsEveryOutcome(t *testing.T) {
	const name = "ASComply_workspace_Okafor_20261026.zip"

	t.Run("no identity", func(t *testing.T) {
		spy := &workspaceSpy{}
		rec, aborted := serveWorkspace(WorkspaceExportHandler(spy.Assemble, slog.Default()), workspaceRequest(false))
		if rec.Code != http.StatusUnauthorized || decodeErrorBody(t, rec.Body) != "unauthorized" {
			t.Errorf("answer = %d %q, want 401 unauthorized", rec.Code, rec.Body.String())
		}
		if spy.calls != 0 || aborted {
			t.Errorf("store called %d times, aborted = %v, want 0 and false", spy.calls, aborted)
		}
	})

	for _, tc := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"not permitted", ErrExportNotPermitted, http.StatusForbidden, exportNotPermittedMsg},
		{"not permitted wrapped", fmt.Errorf("tx: %w", ErrExportNotPermitted), http.StatusForbidden, exportNotPermittedMsg},
		{"not an active member", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage},
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{"already running", ErrExportRunning, http.StatusConflict, exportRunningMsg},
		{"raw error", errors.New("pq: secret internals"), http.StatusInternalServerError, "internal server error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &workspaceSpy{fn: func(io.Writer, func(string)) error { return tc.err }}
			rec, aborted := serveWorkspace(WorkspaceExportHandler(spy.Assemble, slog.Default()), workspaceRequest(true))
			if aborted {
				t.Fatal("a refusal before the first byte aborted the connection")
			}
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d", rec.Code, tc.code)
			}
			if got := decodeErrorBody(t, rec.Body); got != tc.body {
				t.Errorf("error = %q, want %q", got, tc.body)
			}
			if rec.Header().Get("Content-Disposition") != "" || strings.Contains(rec.Header().Get("Content-Type"), "zip") {
				t.Errorf("error answer carries download headers: %v", rec.Header())
			}
		})
	}

	t.Run("success", func(t *testing.T) {
		spy := &workspaceSpy{fn: func(w io.Writer, onStart func(string)) error {
			onStart(name)
			_, err := w.Write([]byte("PK"))
			return err
		}}
		rec, aborted := serveWorkspace(WorkspaceExportHandler(spy.Assemble, slog.Default()), workspaceRequest(true))
		if aborted || rec.Code != http.StatusOK {
			t.Fatalf("status = %d aborted = %v, want 200 and false", rec.Code, aborted)
		}
		h := rec.Header()
		if got := h.Get("Content-Type"); got != "application/zip" {
			t.Errorf("Content-Type = %q, want application/zip", got)
		}
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
		}
		if got, want := h.Get("Content-Disposition"), "attachment; filename="+name; got != want {
			t.Errorf("Content-Disposition = %q, want %q", got, want)
		}
		if _, ok := h["Content-Length"]; ok {
			t.Errorf("Content-Length = %q, want none", h.Get("Content-Length"))
		}
		if rec.Body.String() != "PK" {
			t.Errorf("body = %q, want PK", rec.Body.String())
		}
	})

	t.Run("error after the first write", func(t *testing.T) {
		spy := &workspaceSpy{fn: func(w io.Writer, onStart func(string)) error {
			onStart(name)
			if _, err := w.Write([]byte("PK-partial")); err != nil {
				return err
			}
			return errors.New("bucket gone")
		}}
		var logs bytes.Buffer
		rec, aborted := serveWorkspace(WorkspaceExportHandler(spy.Assemble, slog.New(slog.NewTextHandler(&logs, nil))), workspaceRequest(true))
		if !aborted {
			t.Error("the handler returned normally after a mid-stream error, want panic(http.ErrAbortHandler)")
		}
		if rec.Code != http.StatusOK || rec.Body.String() != "PK-partial" {
			t.Errorf("answer = %d %q, want 200 with only the written bytes", rec.Code, rec.Body.String())
		}
		if n := strings.Count(logs.String(), "archive: workspace export aborted mid-stream"); n != 1 {
			t.Errorf("abort log lines = %d, want 1 (%s)", n, logs.String())
		}
	})
}

func TestWorkspaceExportHandler_AMidStreamErrorAbortsTheConnection(t *testing.T) {
	rec := &sentrytest.Recorder{}
	client, err := sentry.NewClient(sentry.ClientOptions{Dsn: "https://public@example.com/1", Transport: rec})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	sentry.CurrentHub().BindClient(client)
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

	failing := &workspaceSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart("x.zip")
		if _, err := w.Write(bytes.Repeat([]byte("z"), 10<<10)); err != nil {
			return err
		}
		return errors.New("bucket gone")
	}}
	clean := &workspaceSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart("x.zip")
		_, err := w.Write(bytes.Repeat([]byte("z"), 10<<10))
		return err
	}}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	withIdentity := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: uuid.NewString(), TenantID: testTenantID})))
		})
	}

	srv := httptest.NewServer(withIdentity(WorkspaceExportHandler(failing.Assemble, log)))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v, want a 200 whose body then breaks", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("reading the body returned %v after %d bytes, want io.ErrUnexpectedEOF", err, len(body))
	}
	if n := len(rec.Events()); n != 1 {
		t.Errorf("Sentry events after the failed export = %d, want 1", n)
	}
	if !strings.Contains(logs.String(), "archive: workspace export aborted mid-stream") {
		t.Errorf("log = %q, want archive: workspace export aborted mid-stream", logs.String())
	}

	ok := httptest.NewServer(withIdentity(WorkspaceExportHandler(clean.Assemble, log)))
	defer ok.Close()
	res2, err := http.Get(ok.URL)
	if err != nil {
		t.Fatalf("GET clean: %v", err)
	}
	defer res2.Body.Close()
	body2, err := io.ReadAll(res2.Body)
	if err != nil || len(body2) != 10<<10 {
		t.Errorf("clean export read %d bytes, err %v, want 10240 and nil", len(body2), err)
	}
	if n := len(rec.Events()); n != 1 {
		t.Errorf("Sentry events after the clean export = %d, want still 1", n)
	}
}

// deadlineRecorder records the write deadline a handler asks for.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	return nil
}

func TestWorkspaceExportHandler_ExtendsTheWriteDeadlineToOneHour(t *testing.T) {
	t.Run("asks for one hour from now", func(t *testing.T) {
		spy := &workspaceSpy{}
		rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		before := time.Now()
		WorkspaceExportHandler(spy.Assemble, slog.Default()).ServeHTTP(rec, workspaceRequest(true))
		if spy.calls != 1 {
			t.Fatalf("store called %d times, want 1", spy.calls)
		}
		if got := rec.deadline.Sub(before); got < 59*time.Minute || got > 61*time.Minute {
			t.Errorf("write deadline is %v from now, want about one hour", got)
		}
	})

	// A server write timeout far under the stream's length cuts the stream unless the handler extends it.
	slow := func(w io.Writer, onStart func(string)) error {
		time.Sleep(600 * time.Millisecond)
		onStart("x.zip")
		_, err := w.Write([]byte("PK"))
		return err
	}
	serve := func(h http.Handler) (*http.Response, error) {
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: uuid.NewString(), TenantID: testTenantID})))
		}))
		srv.Config.WriteTimeout = 200 * time.Millisecond
		srv.Start()
		t.Cleanup(srv.Close)
		return http.Get(srv.URL)
	}

	t.Run("a stream longer than the server write timeout arrives", func(t *testing.T) {
		spy := &workspaceSpy{fn: slow}
		res, err := serve(WorkspaceExportHandler(spy.Assemble, slog.Default()))
		if err != nil {
			t.Fatalf("GET: %v, want the stream to arrive", err)
		}
		defer res.Body.Close()
		if body, err := io.ReadAll(res.Body); err != nil || string(body) != "PK" {
			t.Errorf("body = %q, err %v, want PK and nil", body, err)
		}
	})

	t.Run("control: the same stream without the extension is cut", func(t *testing.T) {
		res, err := serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = slow(w, func(string) {})
		}))
		if err == nil {
			defer res.Body.Close()
			body, rerr := io.ReadAll(res.Body)
			if rerr == nil && string(body) == "PK" {
				t.Fatal("the stream arrived without the extension, so the server write timeout is not cutting and the test above proves nothing")
			}
		}
	})
}

func TestWorkspaceExportHandler_BusyIs503WithRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		code  int
		msg   string
		retry string
	}{
		{"instance busy", ErrExportBusy, http.StatusServiceUnavailable, exportBusyMsg, "60"},
		{"instance busy wrapped", fmt.Errorf("slot: %w", ErrExportBusy), http.StatusServiceUnavailable, exportBusyMsg, "60"},
		{"workspace running", ErrExportRunning, http.StatusConflict, exportRunningMsg, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &workspaceSpy{fn: func(io.Writer, func(string)) error { return tc.err }}
			rec, _ := serveWorkspace(WorkspaceExportHandler(spy.Assemble, slog.Default()), workspaceRequest(true))
			if rec.Code != tc.code || decodeErrorBody(t, rec.Body) != tc.msg {
				t.Errorf("answer = %d, want %d %q", rec.Code, tc.code, tc.msg)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.retry {
				t.Errorf("Retry-After = %q, want %q", got, tc.retry)
			}
		})
	}
}

func TestWorkspaceExportPreflightHandler_Statuses(t *testing.T) {
	t.Run("no identity", func(t *testing.T) {
		calls := 0
		h := WorkspaceExportPreflightHandler(func(context.Context) error { calls++; return nil }, slog.Default())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, workspaceRequest(false))
		if rec.Code != http.StatusUnauthorized || decodeErrorBody(t, rec.Body) != "unauthorized" || calls != 0 {
			t.Errorf("answer = %d %q, check called %d times, want 401 unauthorized and 0", rec.Code, rec.Body.String(), calls)
		}
	})
	for _, tc := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"free", nil, http.StatusNoContent, ""},
		{"running", ErrExportRunning, http.StatusConflict, exportRunningMsg},
		{"not permitted", ErrExportNotPermitted, http.StatusForbidden, exportNotPermittedMsg},
		{"not an active member", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := WorkspaceExportPreflightHandler(func(context.Context) error { calls++; return tc.err }, slog.Default())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, workspaceRequest(true))
			if calls != 1 || rec.Code != tc.code {
				t.Fatalf("check called %d times, status = %d, want 1 and %d", calls, rec.Code, tc.code)
			}
			if tc.err == nil {
				if rec.Body.Len() != 0 {
					t.Errorf("204 body = %q, want empty", rec.Body.String())
				}
				return
			}
			if got := decodeErrorBody(t, rec.Body); got != tc.body {
				t.Errorf("error = %q, want %q", got, tc.body)
			}
		})
	}
}

func TestCompanyDir_AnAlikeSlugGetsTheID(t *testing.T) {
	id1, id2, id3 := uuid.NewString(), uuid.NewString(), uuid.NewString()

	first := companyDir("Okafor & Co", id1, map[string]bool{})
	if first != "okafor-co" {
		t.Fatalf("companyDir(Okafor & Co) = %q, want okafor-co", first)
	}
	if got, want := companyDir("Okafor  Co", id2, map[string]bool{first: true}), "okafor-co-"+id2; got != want {
		t.Errorf("an alike slug = %q, want %q", got, want)
	}
	if got := companyDir("Okafor Holdings", id2, map[string]bool{first: true}); got != "okafor-holdings" {
		t.Errorf("a different slug beside a taken one = %q, want okafor-holdings", got)
	}
	if got := companyDir("!!!", id3, map[string]bool{}); got != id3 {
		t.Errorf("a name with no alphanumerics = %q, want the id %q", got, id3)
	}
}

func TestDocumentExt_IsTheLowerCasedAlphanumericExtension(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, tc := range []struct {
		name string
		in   *string
		want string
	}{
		{"upper case", s("Scan.PDF"), ".pdf"},
		{"last dot wins", s("a.tar.gz"), ".gz"},
		{"nil", nil, ""},
		{"no extension", s("noext"), ""},
		{"space in the extension", s("x.p d f"), ""},
		{"eleven characters", s("x.abcdefghijk"), ""},
		{"ten characters", s("x.abcdefghij"), ".abcdefghij"},
		{"trailing dot", s("x."), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := documentExt(tc.in); got != tc.want {
				t.Errorf("documentExt(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
