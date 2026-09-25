package gateway

import "strings"

// freeMailDomains lists consumer mail domains refused at registration; subdomains match too.
var freeMailDomains = []string{
	"gmail.com", "googlemail.com",
	"outlook.com", "hotmail.com", "live.com", "msn.com",
	"yahoo.com", "ymail.com", "rocketmail.com",
	"icloud.com", "me.com", "mac.com",
	"aol.com",
	"proton.me", "protonmail.com",
	"gmx.com", "gmx.net", "mail.com",
	"yandex.com",
	"zohomail.com",
}

// isFreeMail reports whether email's domain is, or is a subdomain of, a listed domain.
// The domain follows the last '@' because a quoted local part may itself contain '@'.
func isFreeMail(email string) bool {
	s := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return false
	}
	d := strings.TrimRight(s[at+1:], ".")
	if d == "" {
		return false
	}
	for _, b := range freeMailDomains {
		if d == b || strings.HasSuffix(d, "."+b) {
			return true
		}
	}
	return false
}
