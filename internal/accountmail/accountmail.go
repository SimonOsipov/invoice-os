// Package accountmail holds the branded account-mail templates.
package accountmail

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"
)

//go:embed layout.html blog_confirm.html confirmation.html deletion_cancelled.html deletion_notice.html invite.html new_device.html readiness_check.html readiness_confirm.html readiness_used.html recovery.html stranded_alert.html unsubscribe_url.html
var files embed.FS

// LogoURL is the absolute public URL of the mark in every mail.
const LogoURL = "https://api.ascomply.com/emails/mark.png"

// Template returns layout.html plus <name>.html as one html/template source.
func Template(name string) ([]byte, error) {
	switch name {
	case "blog_confirm", "confirmation", "deletion_cancelled", "deletion_notice", "invite", "new_device", "readiness_check", "readiness_confirm", "readiness_used", "recovery", "stranded_alert":
	default:
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
	src := append(layout, mail...)
	if name != "confirmation" && name != "recovery" {
		unsub, err := files.ReadFile("unsubscribe_url.html")
		if err != nil {
			return nil, err
		}
		src = append(src, unsub...)
	}
	return src, nil
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
	// AppURL is the app root, where the deletion mails send the reader.
	AppURL                   = "https://app.ascomply.com/"
	DeletionNoticeSubject    = "Your ASComply workspace is scheduled for deletion"
	DeletionCancelledSubject = "Your ASComply workspace is no longer scheduled for deletion"
)

// DeletionNoticeMail is the data of one deletion notice; Token is the plain cancel token.
type DeletionNoticeMail struct {
	Workspace, Requester, Token string
	DueAt                       time.Time
}

// DeletionCancelledMail is the data of one deletion-cancelled mail; DueAt is the cancelled due time.
type DeletionCancelledMail struct {
	Workspace, Canceller string
	DueAt                time.Time
}

type deletionView struct {
	Workspace, Requester, Canceller, ActionURL, DueDate, DueTime string
}

// renderDeletion executes the named mail; dates render in UTC so the zone of DueAt never shows.
func renderDeletion(name string, v deletionView, dueAt time.Time) (string, error) {
	if dueAt.IsZero() {
		return "", fmt.Errorf("accountmail: %s mail needs a due time", name)
	}
	due := dueAt.UTC()
	v.DueDate, v.DueTime = due.Format("2 January 2006")+" (UTC)", due.Format("2 January 2006, 15:04")+" UTC"
	src, err := Template(name)
	if err != nil {
		return "", err
	}
	tpl, err := template.New(name).Parse(string(src))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RenderDeletionNotice renders the deletion notice; the subject is fixed so it never carries user text.
func RenderDeletionNotice(m DeletionNoticeMail) (subject, html string, err error) {
	if m.Token == "" {
		return "", "", fmt.Errorf("accountmail: deletion notice needs a token")
	}
	requester := m.Requester
	if requester == "" {
		requester = "A workspace admin"
	}
	html, err = renderDeletion("deletion_notice", deletionView{
		Workspace: m.Workspace, Requester: requester,
		// The token rides in the fragment so no server log receives it.
		ActionURL: AppURL + "#cancel-deletion=" + url.QueryEscape(m.Token),
	}, m.DueAt)
	if err != nil {
		return "", "", err
	}
	return DeletionNoticeSubject, html, nil
}

// RenderDeletionCancelled renders the deletion-cancelled mail; the subject is fixed so it never carries user text.
func RenderDeletionCancelled(m DeletionCancelledMail) (subject, html string, err error) {
	canceller := m.Canceller
	if canceller == "" {
		canceller = "A workspace admin"
	}
	html, err = renderDeletion("deletion_cancelled", deletionView{
		Workspace: m.Workspace, Canceller: canceller, ActionURL: AppURL,
	}, m.DueAt)
	if err != nil {
		return "", "", err
	}
	return DeletionCancelledSubject, html, nil
}

const (
	// BlogConfirmURL is the blog confirm page; the page POSTs the token, so a mail scanner's GET confirms nothing.
	// ceiling: the token rides in the query, so load-balancer, Caddy and crawler-route logs see it; revisit if those logs leave our control or the token outlives 48 h.
	BlogConfirmURL        = "https://www.ascomply.com/blog/subscribe/confirm"
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

const (
	// ForgotPasswordURL opens the landing sign-in on its forgot-password view.
	ForgotPasswordURL = "https://www.ascomply.com/?forgot"
	NewDeviceSubject  = "New sign-in to your ASComply account"
)

// wat is Nigeria's fixed UTC+1 zone; it keeps no daylight saving.
var wat = time.FixedZone("WAT", 60*60)

// NewDeviceMail is the data of one new-device mail; an empty Device or IP renders as unknown.
type NewDeviceMail struct {
	Device, IP string
	At         time.Time
}

type newDeviceView struct {
	Device, IPNote, IPValue, When, ActionURL string
}

// RenderNewDevice renders the new-device mail; the subject is fixed so it never carries user text.
func RenderNewDevice(m NewDeviceMail) (subject, html string, err error) {
	if m.At.IsZero() {
		return "", "", fmt.Errorf("accountmail: new-device mail needs a time")
	}
	v := newDeviceView{
		Device: m.Device, IPValue: m.IP, IPNote: "(IP " + m.IP + ")",
		When: m.At.In(wat).Format("2 January 2006, 15:04") + " WAT", ActionURL: ForgotPasswordURL,
	}
	if v.Device == "" {
		v.Device = "Unknown device"
	}
	if m.IP == "" {
		v.IPValue, v.IPNote = "Unknown IP", "(Unknown IP)"
	}
	src, err := Template("new_device")
	if err != nil {
		return "", "", err
	}
	tpl, err := template.New("new_device").Parse(string(src))
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, v); err != nil {
		return "", "", err
	}
	return NewDeviceSubject, buf.String(), nil
}

const StrandedAlertSubject = "Invoices in your ASComply workspace have nobody to approve them"

// StrandedInvoice is one invoice left with nobody to approve it, and the role that has nobody.
type StrandedInvoice struct{ Number, Role string }

// StrandedAlertMail is the data of one stranded-invoice alert; More counts invoices not listed.
type StrandedAlertMail struct {
	Workspace string
	Invoices  []StrandedInvoice
	More      int
}

type strandedRow struct {
	Label, Value string
	Last         bool
}

type strandedView struct {
	Workspace, ActionURL string
	Rows                 []strandedRow
}

// RenderStrandedAlert renders the stranded-invoice alert; the subject is fixed so it never carries user text.
func RenderStrandedAlert(m StrandedAlertMail) (subject, html string, err error) {
	if len(m.Invoices) == 0 {
		return "", "", fmt.Errorf("accountmail: stranded alert needs an invoice")
	}
	rows := []strandedRow{{Label: "Organisation", Value: m.Workspace}}
	for _, inv := range m.Invoices {
		rows = append(rows, strandedRow{Label: inv.Role, Value: inv.Number})
	}
	if m.More > 0 {
		noun := "invoices"
		if m.More == 1 {
			noun = "invoice"
		}
		rows = append(rows, strandedRow{Label: fmt.Sprintf("And %d more %s", m.More, noun)})
	}
	rows[len(rows)-1].Last = true
	src, err := Template("stranded_alert")
	if err != nil {
		return "", "", err
	}
	tpl, err := template.New("stranded_alert").Parse(string(src))
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, strandedView{Workspace: m.Workspace, ActionURL: AppURL, Rows: rows}); err != nil {
		return "", "", err
	}
	return StrandedAlertSubject, buf.String(), nil
}

const (
	ReadinessCheckSubject   = "Your ASComply readiness check results"
	ReadinessCheckSignUpURL = "https://www.ascomply.com/?register"
)

// MailField is a label and a value; Value may be file text, so the template escapes it.
type MailField struct{ Label, Value string }

// ReadinessCheckInvoice is one checked invoice. A problem invoice has only Failures.
type ReadinessCheckInvoice struct {
	Number, IssueDate, Currency, Subtotal, VAT, Total string
	Buyer, Failures, Warnings                         []MailField
	NotChecked                                        []string
	Passes                                            bool
}

// ReadinessCheckMail is the data of one readiness-check results mail; Total counts invoices, not rows.
type ReadinessCheckMail struct {
	Invoices        []ReadinessCheckInvoice
	Checked, Total  int
	ChecksLeft      int
	DocumentType    string
	BlankNumberRows int
}

type readinessCheckView struct {
	ReadinessCheckMail
	SignUpURL, Heading, Accent, Intro, ChecksLine string
	Details                                       []MailField
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// RenderReadinessCheck renders the results mail; the subject is fixed so it never carries file text.
func RenderReadinessCheck(m ReadinessCheckMail) (subject, html string, err error) {
	v := readinessCheckView{ReadinessCheckMail: m, SignUpURL: ReadinessCheckSignUpURL,
		Heading: "Here is what we found.", Accent: "Fix it before you submit."}
	switch {
	case len(m.Invoices) == 0:
		v.Heading, v.Accent = "We found no invoice", "in this file."
		v.Intro = "We found no invoice in this file."
		if m.BlankNumberRows > 0 {
			v.Intro += fmt.Sprintf(" %d %s no invoice number.", m.BlankNumberRows, plural(m.BlankNumberRows, "row has", "rows have"))
		}
	case m.Total > m.Checked:
		v.Intro = fmt.Sprintf("%d of %d invoices checked. Sign up to check the rest.", m.Checked, m.Total)
	default:
		v.Intro = fmt.Sprintf("%d %s checked.", m.Checked, plural(m.Checked, "invoice", "invoices"))
	}
	if m.DocumentType != "" {
		v.Intro = "This looks like a " + m.DocumentType + "; it was checked as an invoice. " + v.Intro
	}
	if m.ChecksLeft > 0 {
		v.ChecksLine = fmt.Sprintf("You have %d free %s left.", m.ChecksLeft, plural(m.ChecksLeft, "check", "checks"))
	} else {
		v.ChecksLine = "You have no free checks left. Sign up to keep checking."
	}
	src, err := Template("readiness_check")
	if err != nil {
		return "", "", err
	}
	tpl, err := template.New("readiness_check").Parse(string(src))
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, v); err != nil {
		return "", "", err
	}
	return ReadinessCheckSubject, buf.String(), nil
}

const (
	// ReadinessConfirmURL is the confirm page; the page POSTs the token, so a mail scanner's GET confirms nothing.
	ReadinessConfirmURL        = "https://www.ascomply.com/readiness-check/confirm"
	ReadinessConfirmSubject    = "Confirm your email to get your readiness check results"
	ReadinessConfirmValidHours = 24
	ReadinessUsedSubject       = "This address has used its free readiness checks"
)

// ReadinessConfirmMail is the data of one readiness confirmation mail; FileType is csv, xlsx, pdf or docx.
type ReadinessConfirmMail struct{ Email, Token, FileType string }

// ReadinessUsedMail is the data of one used-checks mail.
type ReadinessUsedMail struct{ Email string }

type readinessConfirmView struct {
	Email, FileType, ConfirmURL string
	ValidHours                  int
}

var readinessFileTypes = map[string]string{"csv": "CSV", "xlsx": "XLSX", "pdf": "PDF", "docx": "DOCX"}

func renderReadinessMail(name string, view any) (string, error) {
	src, err := Template(name)
	if err != nil {
		return "", err
	}
	tpl, err := template.New(name).Parse(string(src))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, view); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RenderReadinessConfirm renders the confirmation mail; the subject is fixed so it never carries user text.
func RenderReadinessConfirm(m ReadinessConfirmMail) (subject, html string, err error) {
	label, ok := readinessFileTypes[m.FileType]
	if !ok {
		return "", "", fmt.Errorf("accountmail: unknown readiness file type %q", m.FileType)
	}
	if m.Token == "" {
		return "", "", fmt.Errorf("accountmail: readiness confirm mail needs a token")
	}
	html, err = renderReadinessMail("readiness_confirm", readinessConfirmView{
		Email: m.Email, FileType: label,
		ConfirmURL: ReadinessConfirmURL + "?t=" + url.QueryEscape(m.Token), ValidHours: ReadinessConfirmValidHours,
	})
	if err != nil {
		return "", "", err
	}
	return ReadinessConfirmSubject, html, nil
}

// RenderReadinessUsed renders the used-checks mail; it holds no invoice data and no confirm link.
func RenderReadinessUsed(m ReadinessUsedMail) (subject, html string, err error) {
	html, err = renderReadinessMail("readiness_used", struct{ Email, SignUpURL string }{m.Email, ReadinessCheckSignUpURL})
	if err != nil {
		return "", "", err
	}
	return ReadinessUsedSubject, html, nil
}
