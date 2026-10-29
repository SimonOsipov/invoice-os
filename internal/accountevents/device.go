package accountevents

import "strings"

// Device is the browser and operating system parsed from a user agent; either may be empty.
type Device struct{ Browser, OS string }

// unknownDevice is the label when the pair is incomplete.
const unknownDevice = "Unknown device"

// Order matters: Edge, Opera and the mobile wrappers also say Chrome or Safari.
var browsers = []struct{ token, name string }{
	{"Edg/", "Edge"},
	{"OPR/", "Opera"},
	{"SamsungBrowser/", "Samsung Internet"},
	{"Firefox/", "Firefox"},
	{"FxiOS/", "Firefox"},
	{"CriOS/", "Chrome"},
	{"Chrome/", "Chrome"},
	{"Chromium/", "Chrome"},
}

// Order matters: iPhone and Android UAs also say Mac OS X and Linux.
var systems = []struct {
	tokens []string
	name   string
}{
	{[]string{"Windows NT"}, "Windows"},
	{[]string{"iPhone", "iPad", "iPod"}, "iOS"},
	{[]string{"Android"}, "Android"},
	{[]string{"CrOS"}, "ChromeOS"},
	{[]string{"Mac OS X", "Macintosh"}, "macOS"},
	{[]string{"Linux"}, "Linux"},
}

// Parse reads the browser and OS from ua; anything it does not name stays empty.
func Parse(ua string) Device {
	var d Device
	for _, b := range browsers {
		if strings.Contains(ua, b.token) {
			d.Browser = b.name
			break
		}
	}
	if d.Browser == "" && strings.Contains(ua, "Version/") && strings.Contains(ua, "Safari/") {
		d.Browser = "Safari"
	}
	for _, s := range systems {
		for _, tok := range s.tokens {
			if strings.Contains(ua, tok) {
				d.OS = s.name
				return d
			}
		}
	}
	return d
}

// Label is "Browser on OS", or "Unknown device" unless both are known.
func (d Device) Label() string {
	if d.Browser == "" || d.OS == "" {
		return unknownDevice
	}
	return d.Browser + " on " + d.OS
}
