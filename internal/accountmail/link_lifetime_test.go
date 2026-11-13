package accountmail

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var otpExpLine = regexp.MustCompile(`GOTRUE_MAILER_OTP_EXP=(\S+)`)

// lifetimePhrase words a link lifetime in seconds; a non-whole number of hours has no copy and fails.
func lifetimePhrase(secs int) (string, error) {
	switch {
	case secs <= 0 || secs%3600 != 0:
		return "", fmt.Errorf("GOTRUE_MAILER_OTP_EXP=%d is not a whole number of hours", secs)
	case secs == 3600:
		return "1 hour", nil
	}
	return fmt.Sprintf("%d hours", secs/3600), nil
}

// The mail states the lifetime the identity provider enforces, read from the image that sets it.
func TestLinkExpiryCopyMatchesTheIdPLifetime(t *testing.T) {
	df, err := os.ReadFile("../../sidecar/auth/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := otpExpLine.FindStringSubmatch(string(df))
	if m == nil {
		t.Fatal("the Dockerfile sets no GOTRUE_MAILER_OTP_EXP")
	}
	secs, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("GOTRUE_MAILER_OTP_EXP=%q: %v", m[1], err)
	}
	phrase, err := lifetimePhrase(secs)
	if err != nil {
		t.Fatal(err)
	}
	want := "This link expires in " + phrase + "."

	recov := renderRecovery(t, adaEmail, jsonMap{}, true)
	for name, out := range map[string]string{
		"confirmation (registrant)": renderConfirmation(t, adaEmail, jsonMap{}),
		"confirmation (invited)":    renderConfirmation(t, adaEmail, jsonMap{"invited": true}),
		"recovery":                  recov,
	} {
		got := text(out)
		if n := strings.Count(got, want); n != 1 {
			t.Errorf("%s holds %q %d times, want once", name, want, n)
		}
		if n := strings.Count(got, "expires in"); n != 1 {
			t.Errorf("%s holds %d \"expires in\" phrases, want only %q", name, n, want)
		}
	}
}
