package gateway

import "strings"

// freeMailDomains lists consumer mail domains refused at registration; subdomains match too.
var freeMailDomains = []string{
	"gmail.com", "googlemail.com",
	"outlook.com", "hotmail.com", "live.com", "msn.com",
	"hotmail.co.uk", "hotmail.fr", "hotmail.de", "hotmail.es", "hotmail.it",
	"live.co.uk", "live.fr", "outlook.fr", "outlook.de", "outlook.es",
	"yahoo.com", "ymail.com", "rocketmail.com",
	"yahoo.co.uk", "yahoo.fr", "yahoo.de", "yahoo.es", "yahoo.it",
	"yahoo.ca", "yahoo.com.au", "yahoo.co.in",
	"icloud.com", "me.com", "mac.com",
	"aol.com", "aim.com",
	"proton.me", "protonmail.com", "pm.me", "protonmail.ch",
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
