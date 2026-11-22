// Package erpweb holds the parts of the ERP seam the public gateway serves: the mode, the state
// shape and the fake consent page. It imports no database, queue or Google client, so the gateway
// binary stays free of them (TestProductionGatewayBinaryCannotMint).
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
