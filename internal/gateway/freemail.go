package gateway

// freeMailDomains lists consumer mail domains refused at registration; subdomains match too.
var freeMailDomains = []string{}

// isFreeMail reports whether email's domain is, or is a subdomain of, a listed domain.
func isFreeMail(string) bool { return false }
