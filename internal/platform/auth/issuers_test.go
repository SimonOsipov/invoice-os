package auth

import (
	"reflect"
	"testing"
)

func TestParseTrustedIssuers(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []TrustedIssuer
		wantErr bool
	}{
		{name: "empty", raw: ""},
		{name: "whitespace", raw: "  \n\t "},
		{
			name: "one entry",
			raw:  `[{"issuer":"urn:ascomply:auth:pr-1","jwks_url":"http://auth.railway.internal:8080/.well-known/jwks.json"}]`,
			want: []TrustedIssuer{{Issuer: "urn:ascomply:auth:pr-1", JWKSURL: "http://auth.railway.internal:8080/.well-known/jwks.json"}},
		},
		{
			name: "two entries",
			raw: `[{"issuer":"urn:ascomply:auth:pr-1","jwks_url":"http://auth.railway.internal:8080/.well-known/jwks.json"},` +
				`{"issuer":"urn:ascomply:auth:production","jwks_url":"https://auth.example/.well-known/jwks.json"}]`,
			want: []TrustedIssuer{
				{Issuer: "urn:ascomply:auth:pr-1", JWKSURL: "http://auth.railway.internal:8080/.well-known/jwks.json"},
				{Issuer: "urn:ascomply:auth:production", JWKSURL: "https://auth.example/.well-known/jwks.json"},
			},
		},
		{name: "malformed json", raw: `{`, wantErr: true},
		{name: "unknown field", raw: `[{"issuer":"urn:x","jwks_url":"https://auth.example/jwks","kid":"k1"}]`, wantErr: true},
		{name: "empty issuer", raw: `[{"issuer":"","jwks_url":"https://auth.example/jwks"}]`, wantErr: true},
		{name: "empty jwks_url", raw: `[{"issuer":"urn:x","jwks_url":""}]`, wantErr: true},
		{name: "ftp jwks_url", raw: `[{"issuer":"urn:x","jwks_url":"ftp://auth.example/jwks"}]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTrustedIssuers(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTrustedIssuers(%q) = %+v, nil; want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTrustedIssuers(%q): %v", tc.raw, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseTrustedIssuers(%q) returned %d issuers, want %d: %+v", tc.raw, len(got), len(tc.want), got)
			}
			if len(tc.want) > 0 && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseTrustedIssuers(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}
