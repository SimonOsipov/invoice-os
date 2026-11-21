package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

func TestIdP_IAPConsoleSession_UnknownAddressCreatesNoAccount(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	admin := adminClient(t, base)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const countSQL = `SELECT count(*) FROM auth.users WHERE lower(email) = lower($1)`

	// Control: a registered address gets a link, so ErrNoAccount below is GoTrue's answer for the unknown one.
	known := signUp(t, conn, base)
	link, err := admin.GenerateRecoveryLink(ctx, known.email)
	if err != nil || link.TokenHash == "" {
		t.Fatalf("control: GenerateRecoveryLink for a registered address = %+v, %v; want a token hash", link, err)
	}

	unknown := "iap-unknown-" + uuid.NewString() + "@example.test"
	if n := countAuthRows(t, conn, countSQL, unknown); n != 0 {
		t.Fatalf("control: %d accounts exist for the unknown address before the call", n)
	}
	got, err := admin.GenerateRecoveryLink(ctx, unknown)
	if !errors.Is(err, gotrueadmin.ErrNoAccount) {
		t.Errorf("GenerateRecoveryLink for an unknown address = %+v, %v; want ErrNoAccount", got, err)
	}
	if n := countAuthRows(t, conn, countSQL, unknown); n != 0 {
		t.Errorf("auth.users holds %d rows for the unknown address after the call, want 0", n)
	}
}
