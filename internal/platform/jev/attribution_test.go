package jev

import (
	"net/http"
	"testing"
)

func TestAsk_AttributionHeadersNeedBothValues(t *testing.T) {
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
			ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				reply(w, http.StatusOK, noulOK)
			})
			c := clockClient(ts, newFakeClock())
			c.cfg.appURL, c.cfg.appName = tc.url, tc.app
			if _, err := c.Ask(t.Context(), noulReq("s")); err != nil {
				t.Fatalf("Ask() err = %v", err)
			}
			h := rec.only(t).header
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
