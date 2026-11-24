// Package erpweb holds the parts of the ERP seam the public gateway serves: the mode, the state
// shape and the fake consent page. It imports no database, queue or Google client, so the gateway
// binary stays free of them (TestGatewayBinaryLinksNeitherSecretManagerNorTheERPStore).
package erpweb

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Mode is how the deployment builds its connectors.
type Mode int

const (
	ModeOff Mode = iota
	ModeFake
	ModeSandbox
)

// RunsFake reports whether the fake connector is built: the fake itself, and the sandbox, which adds a real vendor.
func (m Mode) RunsFake() bool { return m == ModeFake || m == ModeSandbox }

// The only variables the mode reads; error text names them, never their values.
const (
	flagFake    = "ERP_FAKE"
	flagSandbox = "ERP_SANDBOX"
)

// flagOn reads a boolean flag; unset is false.
func flagOn(getenv func(string) string, name string) (bool, error) {
	raw := getenv(name)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New("erp: " + name + " is not a boolean")
	}
	return v, nil
}

// ModeFromEnv: the nightly is the sandbox and a preview is the fake, neither reading a flag;
// a hosted deployment refuses either flag; a local run follows ERP_SANDBOX, then ERP_FAKE.
func ModeFromEnv(getenv func(string) string, posture platform.PostureKind) (Mode, error) {
	switch posture {
	case platform.PostureNightly:
		return ModeSandbox, nil
	case platform.PosturePreview:
		return ModeFake, nil
	}
	sandbox, err := flagOn(getenv, flagSandbox)
	if err != nil {
		return ModeOff, err
	}
	fake, err := flagOn(getenv, flagFake)
	if err != nil {
		return ModeOff, err
	}
	switch {
	case sandbox && posture == platform.PostureHosted:
		return ModeOff, errors.New("erp: " + flagSandbox + " must not be enabled on a hosted deployment")
	case fake && posture == platform.PostureHosted:
		return ModeOff, errors.New("erp: " + flagFake + " must not be enabled on a hosted deployment")
	case sandbox:
		return ModeSandbox, nil
	case fake:
		return ModeFake, nil
	}
	return ModeOff, nil
}

const StateTailLen = 43 // base64url of 32 bytes, unpadded

// ParseState checks only the shape; the hash lookup under the tenant decides validity.
func ParseState(s string) (tenantID string, hash []byte, ok bool) {
	tenantID, tail, found := strings.Cut(s, ".")
	if !found || len(tenantID) != 36 || tenantID != strings.ToLower(tenantID) || len(tail) != StateTailLen {
		return "", nil, false
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return "", nil, false
	}
	if raw, err := base64.RawURLEncoding.Strict().DecodeString(tail); err != nil || len(raw) != 32 {
		return "", nil, false
	}
	h := sha256.Sum256([]byte(s))
	return tenantID, h[:], true
}
