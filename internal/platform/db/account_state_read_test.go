package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestGrantAccountStateRead_ErrorsNeverCarryTheDSN(t *testing.T) {
	const secret = "s3cr3t-pw-marker"
	for name, dsn := range map[string]string{
		"unreachable": "postgres://supabase_auth_admin:" + secret + "@127.0.0.1:1/invoice_os?sslmode=disable",
		"unparsable":  "postgres://supabase_auth_admin:" + secret + "@127.0.0.1:notaport/invoice_os",
	} {
		granted, err := db.GrantAccountStateRead(context.Background(), dsn)
		if err == nil || granted {
			t.Fatalf("%s: GrantAccountStateRead = (%v, %v); want (false, error)", name, granted, err)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), dsn) {
			t.Errorf("%s: error carries the DSN or its password: %v", name, err)
		}
	}
}
