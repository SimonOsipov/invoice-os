package erp

import (
	"errors"
	"net/url"
	"strconv"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Mode is how the deployment builds its connectors.
type Mode int

const (
	ModeOff Mode = iota
	ModeFake
)

// flagFake is the only variable the mode reads; error text names it, never its value.
const flagFake = "ERP_FAKE"

// ModeFromEnv: a preview or the nightly always runs the fake and never reads the flag;
// a hosted deployment refuses it; a local run needs ERP_FAKE=true.
func ModeFromEnv(getenv func(string) string, posture platform.PostureKind) (Mode, error) {
	if posture.Ephemeral() {
		return ModeFake, nil
	}
	fake := false
	if raw := getenv(flagFake); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return ModeOff, errors.New("erp: " + flagFake + " is not a boolean")
		}
		fake = v
	}
	switch {
	case fake && posture == platform.PostureHosted:
		return ModeOff, errors.New("erp: " + flagFake + " must not be enabled on a hosted deployment")
	case fake:
		return ModeFake, nil
	}
	return ModeOff, nil
}

// envRedirectURL is the callback URL the vendor sends the browser back to; error text names it, never its value.
const envRedirectURL = "ERP_REDIRECT_URL"

// RegistryFromEnv is the only way to build connectors. It reads ERP_REDIRECT_URL only when it builds one.
func RegistryFromEnv(getenv func(string) string) (Registry, error) {
	posture := platform.Posture(platform.DeployEnvFrom(getenv))
	mode, err := ModeFromEnv(getenv, posture)
	if err != nil {
		return nil, err
	}
	if mode == ModeFake {
		redirect, err := redirectFromEnv(getenv, posture)
		if err != nil {
			return nil, err
		}
		return NewRegistry(NewFake(redirect))
	}
	return NewRegistry()
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
