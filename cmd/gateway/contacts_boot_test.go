package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const bootLandingOrigin = "https://landing.example"

type bootIntake struct {
	path   string
	header http.Header
	body   map[string]any
}

type bootBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *bootBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *bootBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The booted binary is the only observer of main's sink wiring: a nil or mis-aimed sink passes every handler test.
func TestGatewayBinary_HandsOffThroughTheMainWiring(t *testing.T) {
	migrationURL := os.Getenv("DATABASE_MIGRATION_URL")
	if migrationURL == "" {
		t.Skip("gateway boot test skipped: set DATABASE_MIGRATION_URL (or run under the rls job's env)")
	}
	const token = "gw-boot-hand-off-token"
	const verifyID, signInID, registerID = "7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f", "0c9e8d7b-6a5f-4e3d-8c2b-1a0f9e8d7c6b", "5a4b3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d"

	intake := make(chan bootIntake, 8)
	notifications := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		intake <- bootIntake{r.URL.Path, r.Header.Clone(), m}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(notifications.Close)

	session := func(id string) string {
		return `{"access_token":"at","refresh_token":"rt","user":{"id":"` + id + `","email":"` + id + `@corp.example","user_metadata":{` +
			`"registration":{"workspace_name":"Quillworks Ltd","display_name":"Zelda Quill"},` +
			`"marketing_consent":{"text":"I agree.","at":"2026-09-24T10:00:00Z"}}}}`
	}
	var verifyMu sync.Mutex
	var verifyBodies []string
	goTrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/verify":
			verifyMu.Lock()
			verifyBodies = append(verifyBodies, string(b))
			verifyMu.Unlock()
			_, _ = io.WriteString(w, session(verifyID))
		case "/token":
			_, _ = io.WriteString(w, session(signInID))
		case "/signup":
			_, _ = io.WriteString(w, session(registerID))
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(goTrue.Close)

	bin := filepath.Join(t.TempDir(), "gateway")
	buildCtx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(buildCtx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	env := []string{
		"HOME=" + os.Getenv("HOME"), "PORT=" + strconv.Itoa(port), "GATEWAY_TOKEN=" + token,
		"DATABASE_MIGRATION_URL=" + migrationURL,
		"AUTH_ISSUER=http://issuer.invalid", "AUTH_JWKS_URL=http://issuer.invalid/jwks",
		"AUTH_SITE_URL=http://site.invalid", "AUTH_REGISTER_MIN_RESPONSE=10ms",
		"CORS_ALLOWED_ORIGINS=" + bootLandingOrigin,
	}
	for _, svc := range append(append([]string{}, routedServices...), probedServices...) {
		u := goTrue.URL
		if svc == "notifications" {
			u = notifications.URL
		}
		env = append(env, strings.ToUpper(svc)+"_URL="+u)
	}
	out := &bootBuf{}
	cmd := exec.Command(bin)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	for deadline := time.Now().Add(60 * time.Second); ; {
		select {
		case err := <-done:
			t.Fatalf("gateway exited before /healthz answered: %v\n%s", err, out)
		default:
		}
		if resp, err := http.Get(base + "/healthz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("/healthz never answered 200\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}

	next := func(t *testing.T) bootIntake {
		t.Helper()
		select {
		case in := <-intake:
			return in
		case <-time.After(10 * time.Second):
			t.Fatalf("notifications received nothing within 10s\n%s", out)
			return bootIntake{}
		}
	}
	requireIntake := func(t *testing.T, in bootIntake, wantID string) {
		t.Helper()
		if in.path != "/internal/contacts/registrants" {
			t.Errorf("hand-off path = %q, want /internal/contacts/registrants", in.path)
		}
		if got := in.header.Get("X-Gateway-Token"); got != token {
			t.Errorf("X-Gateway-Token = %q, want the gateway token", got)
		}
		if in.body["user_id"] != wantID || in.body["email"] != wantID+"@corp.example" {
			t.Errorf("hand-off body = %v, want the user %s", in.body, wantID)
		}
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Register runs first against an autoconfirming GoTrue: a hand-off it made would reach the intake before verify's.
	t.Run("register hands off nothing", func(t *testing.T) {
		body := `{"email":"r@corp.example","password":"pw-long-enough","marketing_consent_text":"I agree."}`
		resp, err := client.Post(base+"/auth/register", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("register answered %d, want 202\n%s", resp.StatusCode, out)
		}
	})
	t.Run("verify", func(t *testing.T) {
		verifyCalls := func() []string {
			verifyMu.Lock()
			defer verifyMu.Unlock()
			return slices.Clone(verifyBodies)
		}
		link := base + "/auth/verify?token=tok&type=signup"
		var page string
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req, _ := http.NewRequest(method, link, nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s of the link answered %d, want 200\n%s", method, resp.StatusCode, out)
			}
			if method == http.MethodGet {
				page = string(b)
			}
		}
		if got := verifyCalls(); len(got) != 0 {
			t.Fatalf("opening the link reached GoTrue /verify %d times: %v", len(got), got)
		}

		action, values := pageForm(t, link, page)
		resp, err := client.PostForm(action, values)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "http://site.invalid/?verified=1" {
			t.Fatalf("the click answered %d Location %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		got := verifyCalls()
		if len(got) != 1 {
			t.Fatalf("GoTrue /verify saw %d calls %v, want exactly 1", len(got), got)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(got[0]), &sent); err != nil || !reflect.DeepEqual(sent, map[string]string{"type": "signup", "token_hash": "tok"}) {
			t.Errorf("GoTrue /verify got %s, want {\"type\":\"signup\",\"token_hash\":\"tok\"}", got[0])
		}
		requireIntake(t, next(t), verifyID)
	})
	t.Run("sign-in", func(t *testing.T) {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		body, _ := json.Marshal(map[string]string{"email": "x@corp.example", "password": "pw", "state": base64.RawURLEncoding.EncodeToString(raw)})
		resp, err := client.Post(base+"/auth/sign-in", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sign-in answered %d\n%s", resp.StatusCode, out)
		}
		requireIntake(t, next(t), signInID)
	})
	// The route is registered at runtime and its sink is main's: a demo request reaches notifications once.
	t.Run("demo request refusals forward nothing", func(t *testing.T) {
		for name, body := range map[string]string{
			"blank name":         `{"email":"d@corp.example","name":" ","company":"Corp"}`,
			"bad email":          `{"email":"not-an-email","name":"Dee","company":"Corp"}`,
			"blank consent text": `{"email":"d@corp.example","name":"Dee","company":"Corp","marketing_consent_text":"  "}`,
		} {
			resp, err := client.Post(base+"/contacts/demo-request", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s answered %d, want 400\n%s", name, resp.StatusCode, out)
			}
		}
	})
	t.Run("demo request preflight", func(t *testing.T) {
		for origin, want := range map[string]string{bootLandingOrigin: bootLandingOrigin, "https://evil.example": ""} {
			req, _ := http.NewRequest(http.MethodOptions, base+"/contacts/demo-request", nil)
			req.Header.Set("Origin", origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != want {
				t.Errorf("preflight from %s = %d grant %q, want 204 grant %q", origin, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"), want)
			}
		}
	})
	t.Run("demo request", func(t *testing.T) {
		body := `{"email":" d@corp.example ","name":" Dee Quill ","company":"Corp Ltd","marketing_consent_text":"I agree."}`
		req, _ := http.NewRequest(http.MethodPost, base+"/contacts/demo-request", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", bootLandingOrigin)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("demo request answered %d, want 202\n%s", resp.StatusCode, out)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != bootLandingOrigin {
			t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, bootLandingOrigin)
		}
		in := next(t)
		if in.path != "/internal/contacts/demo-requests" {
			t.Errorf("hand-off path = %q, want /internal/contacts/demo-requests", in.path)
		}
		if got := in.header.Get("X-Gateway-Token"); got != token {
			t.Errorf("X-Gateway-Token = %q, want the gateway token", got)
		}
		want := map[string]any{"email": "d@corp.example", "name": "Dee Quill", "company": "Corp Ltd", "marketing_consent_text": "I agree."}
		if !reflect.DeepEqual(in.body, want) {
			t.Errorf("hand-off body = %v, want %v", in.body, want)
		}
	})
	if n := len(intake); n != 0 {
		t.Errorf("notifications received %d further hand-offs, want none", n)
	}
}
