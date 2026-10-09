package db_test

import (
	"context"
	"net/url"
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

const badDSN = "::not a url"

func TestAuthAdminDSN_SwapsOnlyTheCredentials(t *testing.T) {
	got, err := db.AuthAdminDSN("postgresql://invoice_migrator:m@postgres.railway.internal:5432/railway?sslmode=disable", "pw")
	if err != nil {
		t.Fatalf("AuthAdminDSN: %v", err)
	}
	const want = "postgresql://" + authAdminRole + ":pw@postgres.railway.internal:5432/railway?sslmode=disable"
	if got != want {
		t.Errorf("AuthAdminDSN = %q, want %q", got, want)
	}

	t.Run("password_with_reserved_characters_round_trips", func(t *testing.T) {
		const pw = "p@ss/w:rd#1?x"
		got, err := db.AuthAdminDSN("postgres://invoice_migrator:m@h:5433/db_name?sslmode=require&connect_timeout=5", pw)
		if err != nil {
			t.Fatalf("AuthAdminDSN: %v", err)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("result does not parse: %v", err)
		}
		if u.User.Username() != authAdminRole {
			t.Errorf("user = %q, want %q", u.User.Username(), authAdminRole)
		}
		if gotPW, _ := u.User.Password(); gotPW != pw {
			t.Errorf("password = %q, want %q", gotPW, pw)
		}
		if u.Scheme != "postgres" || u.Host != "h:5433" || u.Path != "/db_name" || u.RawQuery != "sslmode=require&connect_timeout=5" {
			t.Errorf("scheme/host/path/query = %q %q %q %q, want postgres h:5433 /db_name sslmode=require&connect_timeout=5", u.Scheme, u.Host, u.Path, u.RawQuery)
		}
	})

	t.Run("a_dsn_without_credentials_gains_them", func(t *testing.T) {
		got, err := db.AuthAdminDSN("postgres://h:5432/railway", "pw")
		if err != nil {
			t.Fatalf("AuthAdminDSN: %v", err)
		}
		if want := "postgres://" + authAdminRole + ":pw@h:5432/railway"; got != want {
			t.Errorf("AuthAdminDSN = %q, want %q", got, want)
		}
	})
}

func TestAuthAdminDSN_RefusesEmptyOrBadInput(t *testing.T) {
	const secret = "s3cret-xyz"
	cases := map[string]struct{ dsn, password string }{
		"empty_migration_dsn":         {"", secret},
		"empty_password":              {"postgresql://invoice_migrator:m@h:5432/railway", ""},
		"unparseable_dsn":             {badDSN, secret},
		"bad_port_dsn_holds_password": {"postgres://invoice_migrator:" + secret + "@h:notaport/railway", secret},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := db.AuthAdminDSN(c.dsn, c.password)
			if err == nil {
				t.Fatalf("AuthAdminDSN(%q, %q) = %q, nil; want an error", c.dsn, c.password, got)
			}
			if got != "" {
				t.Errorf("AuthAdminDSN returned %q beside an error, want \"\"", got)
			}
			if c.password != "" && strings.Contains(err.Error(), c.password) {
				t.Errorf("error holds the password: %v", err)
			}
			if c.dsn != "" && strings.Contains(err.Error(), c.dsn) {
				t.Errorf("error holds the DSN: %v", err)
			}
		})
	}
}
