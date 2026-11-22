package erp

import (
	"errors"
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

// RegistryFromEnv is the only way to build connectors.
func RegistryFromEnv(getenv func(string) string) (Registry, error) {
	mode, err := ModeFromEnv(getenv, platform.Posture(platform.DeployEnvFrom(getenv)))
	if err != nil {
		return nil, err
	}
	if mode == ModeFake {
		return NewRegistry(NewFake())
	}
	return NewRegistry()
}
