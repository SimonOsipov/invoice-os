package platform

import (
	"strings"
	"unicode/utf8"
)

// MaxUserAgentRunes mirrors the CHECK on account_event_handoffs.user_agent.
const MaxUserAgentRunes = 512

// CleanUserAgent makes ua valid UTF-8 without NUL and at most MaxUserAgentRunes long.
func CleanUserAgent(ua string) string {
	ua = strings.ReplaceAll(strings.ToValidUTF8(ua, ""), "\x00", "")
	if utf8.RuneCountInString(ua) <= MaxUserAgentRunes {
		return ua
	}
	return string([]rune(ua)[:MaxUserAgentRunes])
}
