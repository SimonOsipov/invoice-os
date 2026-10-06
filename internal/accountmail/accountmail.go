// Package accountmail holds the branded account-mail templates.
package accountmail

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/url"
)

//go:embed layout.html confirmation.html invite.html
var files embed.FS

// LogoURL is the absolute public URL of the mark in every mail.
const LogoURL = "https://api.ascomply.com/emails/mark.png"

// Template returns layout.html plus <name>.html as one html/template source.
func Template(name string) ([]byte, error) {
	if name != "confirmation" && name != "invite" {
		return nil, fmt.Errorf("accountmail: unknown mail %q", name)
	}
	layout, err := files.ReadFile("layout.html")
	if err != nil {
		return nil, err
	}
	mail, err := files.ReadFile(name + ".html")
	if err != nil {
		return nil, err
	}
	return append(layout, mail...), nil
}

const (
	// InviteAcceptURL is the accept page; the token rides in the fragment so no server log receives it.
	InviteAcceptURL = "https://www.ascomply.com/invite"
	InviteSubject   = "You are invited to ASComply"
	// InviteValidDays is the invite lifetime the mail states.
	InviteValidDays = 7
)

// InviteMail is the data of one invite mail.
type InviteMail struct{ Workspace, Inviter, Email, Role, Token string }

type inviteView struct {
	Workspace, Inviter, Email, RoleLabel, AcceptURL string
	ValidDays                                       int
}

var roleLabels = map[string]string{"admin": "Admin", "preparer": "Preparer", "reviewer": "Reviewer"}

// RenderInvite renders the invite mail; the subject is fixed so it never carries workspace or inviter text.
func RenderInvite(m InviteMail) (subject, html string, err error) {
	label, ok := roleLabels[m.Role]
	if !ok {
		return "", "", fmt.Errorf("accountmail: unknown invite role %q", m.Role)
	}
	inviter := m.Inviter
	if inviter == "" {
		inviter = "A workspace admin"
	}
	src, err := Template("invite")
	if err != nil {
		return "", "", err
	}
	tpl, err := template.New("invite").Parse(string(src))
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	err = tpl.Execute(&buf, inviteView{
		Workspace: m.Workspace, Inviter: inviter, Email: m.Email, RoleLabel: label,
		AcceptURL: InviteAcceptURL + "#token=" + url.QueryEscape(m.Token), ValidDays: InviteValidDays,
	})
	if err != nil {
		return "", "", err
	}
	return InviteSubject, buf.String(), nil
}
