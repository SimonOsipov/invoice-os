package notifications

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	HubSpotBaseURL = "https://api.hubapi.com"
	ResendBaseURL  = "https://api.resend.com"
)

// RealTransport clones the default transport so HTTPS_PROXY and NO_PROXY still apply.
func RealTransport() http.RoundTripper { return http.DefaultTransport.(*http.Transport).Clone() }

// DeliveryClients returns fakes in fake mode, vendor clients on transport in real mode, and
// nil clients when off.
func DeliveryClients(getenv func(string) string, deployEnv string, transport http.RoundTripper) (Mode, HubSpotClient, ResendClient, error) {
	mode, keys, err := ModeFromEnv(getenv, platform.Posture(deployEnv))
	if err != nil {
		return "", nil, nil, err
	}
	switch mode {
	case ModeFake:
		return mode, FakeHubSpot{}, FakeResend{}, nil
	case ModeReal:
		hc := NewHTTPClient(transport)
		return mode, NewHubSpot(HubSpotBaseURL, keys, hc), NewResend(ResendBaseURL, keys, hc), nil
	}
	return mode, nil, nil, nil
}

// NewsletterWiring is the blog subscription setup. Enabled needs a newsletter mode and a mail
// sender that are both on; otherwise both routes answer 503 and the service still boots.
type NewsletterWiring struct {
	Mode     Mode
	MailMode accountmail.Mode
	Sender   accountmail.Sender
	Resend   BlogResendClient
	Enabled  bool
}

func NewsletterClients(getenv func(string) string, deployEnv string, contacts Mode, transport http.RoundTripper) (NewsletterWiring, error) {
	posture := platform.Posture(deployEnv)
	mode, keys, err := NewsletterModeFromEnv(getenv, posture, contacts)
	if err != nil {
		return NewsletterWiring{}, err
	}
	mailMode, mailKey := accountmail.ModeFromEnv(getenv, posture.Ephemeral())
	w := NewsletterWiring{Mode: mode, MailMode: mailMode, Sender: accountmail.NewSender(mailMode, mailKey, transport)}
	switch mode {
	case ModeFake:
		w.Resend = FakeBlogResend{}
	case ModeReal:
		w.Resend = NewBlogResend(ResendBaseURL, getenv("RESEND_API_KEY"), keys, NewHTTPClient(transport))
	}
	w.Enabled = mode != ModeOff && mailMode != accountmail.ModeOff
	return w, nil
}
