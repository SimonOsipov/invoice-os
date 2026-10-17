package blog

import (
	"testing"
	"time"
)

func TestLagosDate_TwentyThreeUTCIsTheNextDay(t *testing.T) {
	before := time.Date(2026, 10, 17, 22, 59, 59, 0, time.UTC)
	at := time.Date(2026, 10, 17, 23, 0, 0, 0, time.UTC)
	if got := LagosDate(before); got != "2026-10-17" {
		t.Errorf("LagosDate(22:59:59Z) = %q, want 2026-10-17", got)
	}
	if got := LagosDate(at); got != "2026-10-18" {
		t.Errorf("LagosDate(23:00:00Z) = %q, want 2026-10-18", got)
	}
}

func TestLagosDate_IgnoresTheInputZone(t *testing.T) {
	in := time.Date(2026, 10, 18, 0, 30, 0, 0, time.FixedZone("UTC+3", 3*3600))
	if got := LagosDate(in); got != "2026-10-17" {
		t.Errorf("LagosDate(00:30 UTC+3 = 21:30Z) = %q, want 2026-10-17", got)
	}
}

func TestCoverPath_IsThePublicPrefixIdAndVersion(t *testing.T) {
	if got, want := CoverPath("p", 1760000000123456), "/blog/covers/p/1760000000123456"; got != want {
		t.Errorf("CoverPath = %q, want %q", got, want)
	}
}
