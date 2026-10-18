// Package accountmail holds the branded account-mail templates.
package accountmail

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/url"
	"strings"
)

//go:embed layout.html blog_confirm.html confirmation.html invite.html recovery.html
var files embed.FS

// LogoURL is the absolute public URL of the mark in every mail.
const LogoURL = "https://api.ascomply.com/emails/mark.png"

// Template returns layout.html plus <name>.html as one html/template source.
func Template(name string) ([]byte, error) {
	if name != "blog_confirm" && name != "confirmation" && name != "invite" && name != "recovery" {
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

const (
	// BlogConfirmURL is the blog confirm page; the page POSTs the token, so a mail scanner's GET confirms nothing.
	BlogConfirmURL        = "https://blog.ascomply.com/subscribe/confirm"
	BlogConfirmSubject    = "Confirm your ASComply blog subscription"
	BlogConfirmValidHours = 48
)

// BlogConfirmMail is the data of one blog confirmation mail; Topics are category names in display order.
type BlogConfirmMail struct {
	Email, Token string
	Topics       []string
	Frequency    string
}

type blogConfirmView struct {
	Email, Topics, FrequencyLabel, ConfirmURL string
	ValidHours                                int
}

var frequencyLabels = map[string]string{"weekly": "Weekly digest", "every_post": "Every post", "monthly": "Monthly"}

// RenderBlogConfirm renders the blog confirmation mail; the subject is fixed so it never carries user text.
func RenderBlogConfirm(m BlogConfirmMail) (subject, html string, err error) {
	label, ok := frequencyLabels[m.Frequency]
	if !ok {
		return "", "", fmt.Errorf("accountmail: unknown blog frequency %q", m.Frequency)
	}
	src, err := Template("blog_confirm")
	if err != nil {
		return "", "", err
	}
	tpl, err := template.New("blog_confirm").Parse(string(src))
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	err = tpl.Execute(&buf, blogConfirmView{
		Email: m.Email, Topics: strings.Join(m.Topics, ", "), FrequencyLabel: label,
		ConfirmURL: BlogConfirmURL + "?token=" + url.QueryEscape(m.Token), ValidHours: BlogConfirmValidHours,
	})
	if err != nil {
		return "", "", err
	}
	return BlogConfirmSubject, buf.String(), nil
}
