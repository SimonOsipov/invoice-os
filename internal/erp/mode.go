package erp

import (
	"errors"
	"net/url"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/erp/erpweb"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Mode is how the deployment builds its connectors; the gateway reads it through erpweb.
type Mode = erpweb.Mode

const (
	ModeOff     = erpweb.ModeOff
	ModeFake    = erpweb.ModeFake
	ModeSandbox = erpweb.ModeSandbox
)

// ModeFromEnv is erpweb.ModeFromEnv: the nightly is the sandbox, a preview the fake, a hosted deployment refuses both flags.
func ModeFromEnv(getenv func(string) string, posture platform.PostureKind) (Mode, error) {
	return erpweb.ModeFromEnv(getenv, posture)
}

// envRedirectURL is the callback URL the vendor sends the browser back to; error text names it, never its value.
const envRedirectURL = "ERP_REDIRECT_URL"

// The Intuit app's variables; error text names them, never their values.
const (
	envIntuitClientID        = "INTUIT_CLIENT_ID"
	envIntuitClientSecret    = "INTUIT_CLIENT_SECRET"
	envIntuitWebhookVerifier = "INTUIT_WEBHOOK_VERIFIER"
)

// RegistryFromEnv is the only way to build connectors. It reads ERP_REDIRECT_URL only when it builds
// one, and the INTUIT_* variables only in sandbox mode, where QuickBooks joins the fake.
func RegistryFromEnv(getenv func(string) string) (Registry, error) {
	posture := platform.Posture(platform.DeployEnvFrom(getenv))
	mode, err := ModeFromEnv(getenv, posture)
	if err != nil {
		return nil, err
	}
	if !mode.RunsFake() {
		return NewRegistry()
	}
	redirect, err := redirectFromEnv(getenv, posture)
	if err != nil {
		return nil, err
	}
	if mode != ModeSandbox {
		return NewRegistry(NewFake(redirect))
	}
	cfg, err := intuitFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return NewRegistry(NewFake(redirect), NewQuickBooks(redirect, cfg))
}

// intuitFromEnv reads the three variables in a fixed order; a padded value is a paste slip.
func intuitFromEnv(getenv func(string) string) (intuitConfig, error) {
	var vals [3]string
	for i, name := range []string{envIntuitClientID, envIntuitClientSecret, envIntuitWebhookVerifier} {
		v := getenv(name)
		if v == "" {
			return intuitConfig{}, errors.New("erp: " + name + " is not set")
		}
		if strings.TrimSpace(v) != v {
			return intuitConfig{}, errors.New("erp: " + name + " has surrounding whitespace")
		}
		vals[i] = v
	}
	return intuitConfig{clientID: vals[0], clientSecret: vals[1], webhookVerifier: vals[2]}, nil
}

// redirectFromEnv wants an absolute URL, https outside a local run.
func redirectFromEnv(getenv func(string) string, posture platform.PostureKind) (*url.URL, error) {
	raw := getenv(envRedirectURL)
	if raw == "" {
		return nil, errors.New("erp: " + envRedirectURL + " is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("erp: " + envRedirectURL + " is not an absolute http(s) URL")
	}
	if u.Scheme != "https" && posture != platform.PostureLocal {
		return nil, errors.New("erp: " + envRedirectURL + " must be https")
	}
	return u, nil
}
