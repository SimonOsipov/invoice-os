package notifications

// STUB (AUTH-17-03 red): compile-only; the executor replaces it.

import "github.com/SimonOsipov/invoice-os/internal/platform"

type Mode string

const (
	ModeReal Mode = "real"
	ModeFake Mode = "fake"
	ModeOff  Mode = "off"
)

type Keys struct {
	HubSpotToken, ResendAPIKey, ResendSegmentID, ResendTopicID string
}

func ModeFromEnv(getenv func(string) string, posture platform.PostureKind) (Mode, Keys, error) {
	return ModeOff, Keys{}, nil
}
