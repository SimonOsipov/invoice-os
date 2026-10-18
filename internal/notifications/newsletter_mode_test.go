package notifications

import (
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func TestNewsletterMode_Table(t *testing.T) {
	both := map[string]string{envBlogTopic: "t-1", envBlogSegment: "s-1"}
	cases := []struct {
		name     string
		posture  platform.PostureKind
		contacts Mode
		env      map[string]string
		want     Mode
		wantKeys BlogKeys
	}{
		{"preview ignores keys", platform.PosturePreview, ModeReal, both, ModeFake, BlogKeys{}},
		{"contacts fake", platform.PostureHosted, ModeFake, both, ModeFake, BlogKeys{}},
		{"contacts off with keys", platform.PostureHosted, ModeOff, both, ModeOff, BlogKeys{}},
		{"real with both", platform.PostureHosted, ModeReal, both, ModeReal, BlogKeys{TopicID: "t-1", SegmentID: "s-1"}},
		{"real with none", platform.PostureHosted, ModeReal, nil, ModeOff, BlogKeys{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, keys, err := NewsletterModeFromEnv(getenvFrom(c.env), c.posture, c.contacts)
			if err != nil || got != c.want || keys != c.wantKeys {
				t.Fatalf("got (%q, %+v, %v), want (%q, %+v, nil)", got, keys, err, c.want, c.wantKeys)
			}
		})
	}
}

func TestNewsletterMode_PartialKeysNameTheMissingVariable(t *testing.T) {
	cases := []struct {
		set, value, missing string
	}{
		{envBlogTopic, "t-secret", envBlogSegment},
		{envBlogSegment, "s-secret", envBlogTopic},
	}
	for _, c := range cases {
		t.Run(c.missing, func(t *testing.T) {
			_, _, err := NewsletterModeFromEnv(getenvFrom(map[string]string{c.set: c.value}), platform.PostureHosted, ModeReal)
			if err == nil {
				t.Fatal("err = nil, want a boot error")
			}
			if !strings.Contains(err.Error(), c.missing) {
				t.Errorf("error %q does not name %s", err, c.missing)
			}
			if strings.Contains(err.Error(), c.value) {
				t.Errorf("error %q carries the value of %s", err, c.set)
			}
		})
	}
}
