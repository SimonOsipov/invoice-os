package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// Counted on the raw text: json.Unmarshal keeps the last of two duplicate keys.
func tenantKeyCount(line string) int { return strings.Count(line, `"tenant_id"`) }

func jevOneLine(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("got %d log line(s), want 1: %q", len(lines), buf.String())
	}
	return lines[0]
}

func TestContextHandler_AJevCallLineCarriesOneTenant(t *testing.T) {
	const tenant = "tnt-jev-1"
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")

	var buf bytes.Buffer
	logger := slog.New(&contextHandler{Handler: slog.NewJSONHandler(&buf, nil)})
	c, err := jev.FromEnv(logger)
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	req := jev.Request{
		Purpose: jev.PurposeValueCheck,
		State:   "Invoice No: INV-001\nTotal: 1,935.00",
		Questions: map[string]jev.Question{
			"total": {Type: jev.TypeNoul, Instructions: "Is the total 1935.00?"},
		},
	}

	// Counter control: a line that really carries the key twice must read 2.
	logger.InfoContext(WithTenantID(context.Background(), tenant), "x", slog.String("tenant_id", tenant))
	if n := tenantKeyCount(jevOneLine(t, &buf)); n != 2 {
		t.Fatalf("control line with tenant_id from the handler and an attr counts %d, want 2 -- the counter cannot see a duplicate", n)
	}
	buf.Reset()

	// Handler control: WithTenantID alone writes the key once.
	logger.InfoContext(WithTenantID(context.Background(), tenant), "x")
	if n := tenantKeyCount(jevOneLine(t, &buf)); n != 1 {
		t.Fatalf("control line through WithTenantID counts %d tenant_id key(s), want 1", n)
	}
	buf.Reset()

	identity := auth.WithIdentity(context.Background(), auth.Identity{Subject: "u-1", TenantID: tenant})
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"identity only", identity},
		{"identity plus WithTenantID", WithTenantID(identity, tenant)},
	} {
		buf.Reset()
		if _, err := c.Ask(tc.ctx, req); err != nil {
			t.Fatalf("%s: Ask: %v", tc.name, err)
		}
		line := jevOneLine(t, &buf)
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("%s: malformed line %q: %v", tc.name, line, err)
		}
		if m["msg"] != "jev call" {
			t.Fatalf("%s: msg = %v, want \"jev call\"", tc.name, m["msg"])
		}
		if n := tenantKeyCount(line); n != 1 {
			t.Errorf("%s: the jev call line carries %d tenant_id key(s), want 1: %s", tc.name, n, line)
		}
		if m["tenant_id"] != tenant {
			t.Errorf("%s: tenant_id = %v, want %q", tc.name, m["tenant_id"], tenant)
		}
	}
}
