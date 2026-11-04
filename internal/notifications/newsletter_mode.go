package notifications

import (
	"fmt"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// BlogKeys are the Resend ids of topic "Blog" and segment "Blog".
type BlogKeys struct {
	TopicID, SegmentID string
}

const (
	envBlogTopic   = "RESEND_BLOG_TOPIC_ID"
	envBlogSegment = "RESEND_BLOG_SEGMENT_ID"
)

// NewsletterModeFromEnv follows the contacts mode: a preview and fake are fake, off is off. Under real, no Blog id means
// off (fake on the nightly) and one id alone is a boot error naming the missing variable. The nightly is real once
// mail-on mounts both ids.
func NewsletterModeFromEnv(getenv func(string) string, posture platform.PostureKind, contacts Mode) (Mode, BlogKeys, error) {
	switch {
	case posture == platform.PosturePreview, contacts == ModeFake:
		return ModeFake, BlogKeys{}, nil
	case contacts != ModeReal:
		return ModeOff, BlogKeys{}, nil
	}
	k := BlogKeys{TopicID: getenv(envBlogTopic), SegmentID: getenv(envBlogSegment)}
	switch {
	case k.TopicID == "" && k.SegmentID == "" && posture == platform.PostureNightly:
		return ModeFake, BlogKeys{}, nil
	case k.TopicID == "" && k.SegmentID == "":
		return ModeOff, BlogKeys{}, nil
	case k.TopicID == "":
		return "", BlogKeys{}, fmt.Errorf("notifications: %s is set; missing %s", envBlogSegment, envBlogTopic)
	case k.SegmentID == "":
		return "", BlogKeys{}, fmt.Errorf("notifications: %s is set; missing %s", envBlogTopic, envBlogSegment)
	}
	return ModeReal, k, nil
}
