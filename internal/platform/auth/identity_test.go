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

func TestTenantlessCaller_SeparateFromIdentity(t *testing.T) {
	caller := Identity{Subject: testSubject, Role: "authenticated", Email: "ada@example.test"}

	ctx := WithTenantlessCaller(context.Background(), caller)
	got, ok := TenantlessCallerFromContext(ctx)
	if !ok {
		t.Fatal("tenant-less caller not found after WithTenantlessCaller")
	}
	if got != caller {
		t.Fatalf("tenant-less caller = %+v, want %+v", got, caller)
	}
	if id, ok := IdentityFromContext(ctx); ok {
		t.Fatalf("IdentityFromContext sees the tenant-less caller: %+v", id)
	}

	tenant := Identity{Subject: testSubject, Role: "authenticated", TenantID: "tenant-x"}
	ctx = WithIdentity(context.Background(), tenant)
	if _, ok := IdentityFromContext(ctx); !ok {
		t.Fatal("identity not found after WithIdentity")
	}
	if c, ok := TenantlessCallerFromContext(ctx); ok {
		t.Fatalf("TenantlessCallerFromContext sees the identity: %+v", c)
	}
}
