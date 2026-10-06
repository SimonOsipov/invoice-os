package accountmail

// RED STUB (RESEND-05-03, compile-only). The executor deletes this file and
// implements these symbols in accountmail.go per the subtask.

const (
	InviteAcceptURL = "https://www.ascomply.com/invite"
	InviteSubject   = "You are invited to ASComply"
	InviteValidDays = 7
)

type InviteMail struct{ Workspace, Inviter, Email, Role, Token string }

func RenderInvite(m InviteMail) (subject, html string, err error) { return "", "", nil }
