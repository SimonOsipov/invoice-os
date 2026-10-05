package notifications

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

type Mode string

const (
	ModeReal Mode = "real"
	ModeFake Mode = "fake"
	ModeOff  Mode = "off"
)

const envFake = "CONTACTS_FAKE"

type Keys struct {
	HubSpotToken, ResendAPIKey, ResendSegmentID, ResendTopicID string
}

// ModeFromEnv never reads a preview environment's values, so preview never refuses to boot.
// Values are not trimmed. No error text carries a value.
func ModeFromEnv(getenv func(string) string, posture platform.PostureKind) (Mode, Keys, error) {
	if posture == platform.PosturePreview {
		return ModeFake, Keys{}, nil
	}
	var fake bool
	if raw := getenv(envFake); raw != "" {
		var err error
		if fake, err = strconv.ParseBool(raw); err != nil {
			return "", Keys{}, fmt.Errorf("notifications: %s is not a boolean", envFake)
		}
	}
	var k Keys
	vars := []struct {
		name string
		dst  *string
	}{
		{"HUBSPOT_TOKEN", &k.HubSpotToken},
		{"RESEND_API_KEY", &k.ResendAPIKey},
		{"RESEND_SEGMENT_ID", &k.ResendSegmentID},
		{"RESEND_TOPIC_ID", &k.ResendTopicID},
	}
	var set, missing []string
	for _, v := range vars {
		if *v.dst = getenv(v.name); *v.dst != "" {
			set = append(set, v.name)
		} else {
			missing = append(missing, v.name)
		}
	}
	switch {
	case fake && len(set) > 0:
		return "", Keys{}, fmt.Errorf("notifications: %s is true and %s set; unset one", envFake, strings.Join(set, ", "))
	case fake:
		return ModeFake, Keys{}, nil
	case len(set) == 0:
		return ModeOff, Keys{}, nil
	case len(missing) > 0:
		return "", Keys{}, fmt.Errorf("notifications: partial vendor keys; missing %s", strings.Join(missing, ", "))
	}
	return ModeReal, k, nil
}
