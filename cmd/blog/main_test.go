package main

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfig_RefusesAMissingOrRelativeGatewayURL(t *testing.T) {
	for _, bad := range []string{"", "api.example", "ftp://x", "http://"} {
		_, _, err := config(env(map[string]string{"VITE_GATEWAY_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "VITE_GATEWAY_URL") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	_, u, err := config(env(map[string]string{"VITE_GATEWAY_URL": "https://api.example"}))
	if err != nil || u.Host != "api.example" {
		t.Errorf("ok case: %v %v", u, err)
	}
}

func TestConfig_PortDefaultsTo8080(t *testing.T) {
	for in, want := range map[string]string{"": "8080", "9000": "9000"} {
		port, _, err := config(env(map[string]string{"PORT": in, "VITE_GATEWAY_URL": "https://api.example"}))
		if err != nil || port != want {
			t.Errorf("PORT %q: %q %v", in, port, err)
		}
	}
}
