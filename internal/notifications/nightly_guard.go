package notifications

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

const envReceivingDomain = "RESEND_RECEIVING_DOMAIN"

// receivingDomainPattern mirrors domainPattern in internal/tools/nightlycontacts.
var receivingDomainPattern = regexp.MustCompile(`^[a-z0-9-]+\.resend\.app$`)

// guardNightly limits the real Resend calls and real mail of the nightly to its run addresses; every
// other address gets the fake client and the capture. Pinned by TestNightlyGuard_*.
func guardNightly(w *NewsletterWiring, domain string) error {
	domain = foldASCII(strings.TrimSpace(domain))
	if !receivingDomainPattern.MatchString(domain) {
		return errors.New("notifications: " + envReceivingDomain + " must be a <name>.resend.app domain for a real nightly newsletter or mail")
	}
	allow := func(email string) bool { return isNightlyRunAddress(email, domain) }
	if w.Mode == ModeReal {
		w.Resend = guardResend{real: w.Resend, allow: allow}
	}
	if w.MailMode == accountmail.ModeReal {
		w.Sender = &guardSender{real: w.Sender, capture: &accountmail.Capture{}, allow: allow}
	}
	return nil
}

// isNightlyRunAddress mirrors isRunContact in internal/tools/nightlycontacts. It folds ASCII only.
func isNightlyRunAddress(email, domain string) bool {
	e, d := foldASCII(email), foldASCII(domain)
	return strings.HasPrefix(e, "nightly-") && strings.HasSuffix(e, "@"+d)
}

func foldASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

type guardResend struct {
	real  BlogResendClient
	allow func(string) bool
}

func (g guardResend) SyncBlog(ctx context.Context, s BlogSubscriber) error {
	if !g.allow(s.Email) {
		return FakeBlogResend{}.SyncBlog(ctx, s)
	}
	return g.real.SyncBlog(ctx, s)
}

func (g guardResend) ContactUnsubscribed(ctx context.Context, email string) (bool, error) {
	if !g.allow(email) {
		return FakeBlogResend{}.ContactUnsubscribed(ctx, email)
	}
	return g.real.ContactUnsubscribed(ctx, email)
}

type guardSender struct {
	real    accountmail.Sender
	capture *accountmail.Capture
	allow   func(string) bool
}

func (g *guardSender) Send(ctx context.Context, msgs []accountmail.Message) error {
	var run, other []accountmail.Message
	for _, m := range msgs {
		if g.allow(m.To) {
			run = append(run, m)
		} else {
			other = append(other, m)
		}
	}
	if len(other) > 0 {
		if err := g.capture.Send(ctx, other); err != nil {
			return err
		}
	}
	if len(run) == 0 {
		return nil
	}
	return g.real.Send(ctx, run)
}
