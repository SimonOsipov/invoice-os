package erp

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestState_RoundTripAndShape(t *testing.T) {
	tenant := uuid.NewString()
	s, h := NewState(tenant)
	got, gh, ok := ParseState(s)
	if !ok || got != tenant || !bytes.Equal(gh, h) || len(h) != 32 {
		t.Fatalf("ParseState = %q, %x, %v", got, gh, ok)
	}
	tail := s[strings.Index(s, ".")+1:]
	if raw, err := base64.RawURLEncoding.DecodeString(tail); err != nil || len(raw) != 32 {
		t.Errorf("tail = %d bytes, %v; want 32", len(raw), err)
	}
	s2, h2 := NewState(tenant)
	if s == s2 || bytes.Equal(h, h2) {
		t.Error("two states are equal")
	}
	for name, bad := range map[string]string{
		"bad uuid":      "not-a-uuid." + tail,
		"upper uuid":    strings.ToUpper(tenant) + "." + tail,
		"short tail":    tenant + "." + tail[:42],
		"long tail":     tenant + "." + tail + "A",
		"no dot":        tenant + tail,
		"padded tail":   tenant + "." + tail[:42] + "=",
		"not base64url": tenant + "." + strings.Repeat("+", 43),
		"empty":         "",
		"tenant dot":    tenant + ".",
	} {
		if _, _, ok := ParseState(bad); ok {
			t.Errorf("%s accepted", name)
		}
	}
}
