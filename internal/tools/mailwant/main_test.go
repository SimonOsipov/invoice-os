package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

const (
	testAddr = "invitee@mail.example"
	testLink = "https://gw.example/auth/verify?token=abc&type=signup"
)

func call(args []string, stdin string) (code int, out string) {
	var o, e bytes.Buffer
	code = run(args, strings.NewReader(stdin), &o, &e)
	return code, o.String()
}

func TestMailWant_InviteEqualsRenderInvite(t *testing.T) {
	_, want, err := accountmail.RenderInvite(accountmail.InviteMail{
		Workspace: "Acme", Inviter: "Ada", Email: testAddr, Role: "reviewer", Token: "tok-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, got := call([]string{"invite", "Acme", "Ada", testAddr, "reviewer"}, "tok-1\n")
	if code != 0 || got != want {
		t.Fatalf("code %d, stdout differs from RenderInvite: %v", code, got != want)
	}
}

func TestMailWant_RecoveryNamesTheAddressAndTheLink(t *testing.T) {
	code, got := call([]string{"recovery", testAddr}, testLink+"\n")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	escaped := strings.ReplaceAll(testLink, "&", "&amp;")
	if !strings.Contains(got, testAddr) || strings.Count(got, escaped) < 2 || strings.Contains(got, "{{") {
		t.Errorf("recovery lacks the address, the link in the button and text, or holds a placeholder")
	}
}

func TestMailWant_ConfirmationIsTheInvitedVariant(t *testing.T) {
	code, got := call([]string{"confirmation", testAddr}, testLink)
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	escaped := strings.ReplaceAll(testLink, "&", "&amp;") + "&amp;invite=1"
	if !strings.Contains(got, "Choose your password") || !strings.Contains(got, testAddr) ||
		!strings.Contains(got, escaped) || strings.Contains(got, "Confirm email address") {
		t.Error("confirmation is not the invited variant with the invite link")
	}
}

func TestMailWant_WrongCallExitsTwo(t *testing.T) {
	cases := map[string]struct {
		args  []string
		stdin string
	}{
		"no args":         {nil, "x"},
		"unknown mail":    {[]string{"newsletter", testAddr}, "x"},
		"invite 3 args":   {[]string{"invite", "Acme", testAddr}, "x"},
		"unknown role":    {[]string{"invite", "Acme", "Ada", testAddr, "owner"}, "x"},
		"empty stdin":     {[]string{"recovery", testAddr}, "\n"},
		"recovery 1 more": {[]string{"recovery", testAddr, "x"}, "x"},
	}
	for name, c := range cases {
		if code, out := call(c.args, c.stdin); code != 2 || out != "" {
			t.Errorf("%s: code %d, stdout %q", name, code, out)
		}
	}
}
