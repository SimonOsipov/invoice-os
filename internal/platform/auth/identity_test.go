package auth

import (
	"context"
	"testing"
)

// TestIdentityContextRoundTrip covers the context helpers, which double as the
// cheap stub business-logic tests use to run as a given tenant/role without
// minting or verifying a token (AC-8).
func TestIdentityContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := IdentityFromContext(ctx); ok {
		t.Fatal("empty context should carry no identity")
	}

	want := Identity{Subject: testSubject, Role: "authenticated", TenantID: "tenant-x"}
	ctx = WithIdentity(ctx, want)

	got, ok := IdentityFromContext(ctx)
	if !ok {
		t.Fatal("identity not found after WithIdentity")
	}
	if got != want {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
}
