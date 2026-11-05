package main

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func TestSeedsCodeLists_OnlyOutsideProduction(t *testing.T) {
	for env, want := range map[string]bool{
		"": true, "pr-12": true, "nightly": true, "production": false, "staging": false,
	} {
		if got := seedsCodeLists(platform.Posture(env)); got != want {
			t.Errorf("DEPLOY_ENV %q: seeds = %v, want %v", env, got, want)
		}
	}
}
