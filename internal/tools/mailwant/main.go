// Command mailwant prints the account mail the templates render, as the text oracle of the nightly mail check.
//
//	mailwant invite <workspace> <inviter> <email> <role>   token on stdin
//	mailwant recovery <email>                             link on stdin
//	mailwant confirmation <email>                         link on stdin (the invited variant)
//
// The token and the link come on stdin so a failed exec message cannot hold them.
package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"os"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(msg string) int {
		fmt.Fprintln(stderr, "mailwant: "+msg)
		return 2
	}
	if len(args) == 0 {
		return fail("usage: mailwant invite|recovery|confirmation ...")
	}
	var want int
	switch args[0] {
	case "invite":
		want = 5
	case "recovery", "confirmation":
		want = 2
	default:
		return fail("unknown mail")
	}
	if len(args) != want {
		return fail("wrong number of arguments")
	}
	raw, err := io.ReadAll(stdin)
	secret := strings.TrimSpace(string(raw))
	if err != nil || secret == "" {
		return fail("empty stdin")
	}

	var out string
	if args[0] == "invite" {
		_, out, err = accountmail.RenderInvite(accountmail.InviteMail{
			Workspace: args[1], Inviter: args[2], Email: args[3], Role: args[4], Token: secret,
		})
	} else {
		out, err = renderGoTrue(args[0], args[1], secret)
	}
	if err != nil {
		return fail(err.Error())
	}
	fmt.Fprint(stdout, out)
	return 0
}

// renderGoTrue executes a template with the data GoTrue hands it; the confirmation is the invited variant.
func renderGoTrue(name, email, link string) (string, error) {
	src, err := accountmail.Template(name)
	if err != nil {
		return "", err
	}
	tpl, err := template.New(name).Parse(string(src))
	if err != nil {
		return "", err
	}
	data := map[string]any{
		"SiteURL":         accountmail.InviteAcceptURL,
		"ConfirmationURL": link,
		"Email":           email,
		"Token":           "123456",
		"TokenHash":       "hash",
		"RedirectTo":      "https://www.ascomply.com/",
		"Data":            map[string]any{},
	}
	if name == "confirmation" {
		data["Data"] = map[string]any{"invited": true}
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
