package ai

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCall_AttributionHeadersNeedBothValues(t *testing.T) {
	cases := []struct {
		name, url, app string
		want           bool
	}{
		{"both", "https://app.example/n", "Nightly App", true},
		{"neither", "", "", false},
		{"url only", "https://app.example/n", "", false},
		{"name only", "", "Nightly App", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec capture
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(envelope(validContent, "")))
			}))
			t.Cleanup(srv.Close)
			fc := &fakeClock{t: time.Unix(0, 0)}
			c := newClient(config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: fc.now, sleep: fc.sleep,
				appURL: tc.url, appName: tc.app}, nil)
			if _, err := c.Call(t.Context(), baseReq()); err != nil {
				t.Fatalf("Call() err = %v", err)
			}
			h, _, _ := rec.snapshot()
			ref, title := h.Values("HTTP-Referer"), h.Values("X-OpenRouter-Title")
			if tc.want {
				if len(ref) != 1 || ref[0] != tc.url || len(title) != 1 || title[0] != tc.app {
					t.Errorf("HTTP-Referer = %q, X-OpenRouter-Title = %q, want [%q] [%q]", ref, title, tc.url, tc.app)
				}
			} else if len(ref) != 0 || len(title) != 0 {
				t.Errorf("HTTP-Referer = %q, X-OpenRouter-Title = %q, want none", ref, title)
			}
			if got := h.Get("Authorization"); got != "Bearer k" {
				t.Errorf("Authorization = %q", got)
			}
			if got := h.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
		})
	}
}

func TestFromEnv_ReadsTheAttributionVariables(t *testing.T) {
	t.Setenv(EnvAppURL, "https://app.example/n")
	t.Setenv(EnvAppName, "Nightly App")
	c, err := FromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.cfg.appURL != "https://app.example/n" || c.cfg.appName != "Nightly App" {
		t.Errorf("appURL = %q, appName = %q", c.cfg.appURL, c.cfg.appName)
	}
}
