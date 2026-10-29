package accountevents

import "testing"

const (
	uaChromeMac    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	uaEdgeWin      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0"
	uaFirefoxLinux = "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0"
	uaSafariIPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
	uaSafariMac    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
	uaChromeAndr   = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36"
	uaSamsung      = "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36"
	uaOperaWin     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 OPR/115.0.0.0"
	uaCriOS        = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0.6723.37 Mobile/15E148 Safari/604.1"
	uaFxiOS        = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/131.0 Mobile/15E148 Safari/605.1.15"
	uaCrOS         = "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

// AC 1: the browser and OS table of [d-ua]; each UA also carries a lower-priority token
// (Chrome in an Edge UA, Mac OS X in an iPhone UA, Linux in an Android UA).
func TestDevice_ParseTable(t *testing.T) {
	cases := []struct {
		name, ua, browser, os, label string
	}{
		{"Chrome 130 macOS", uaChromeMac, "Chrome", "macOS", "Chrome on macOS"},
		{"Edge Windows", uaEdgeWin, "Edge", "Windows", "Edge on Windows"},
		{"Firefox Linux", uaFirefoxLinux, "Firefox", "Linux", "Firefox on Linux"},
		{"Safari iPhone", uaSafariIPhone, "Safari", "iOS", "Safari on iOS"},
		{"Safari Mac", uaSafariMac, "Safari", "macOS", "Safari on macOS"},
		{"Chrome Android", uaChromeAndr, "Chrome", "Android", "Chrome on Android"},
		{"SamsungBrowser", uaSamsung, "Samsung Internet", "Android", "Samsung Internet on Android"},
		{"OPR Windows", uaOperaWin, "Opera", "Windows", "Opera on Windows"},
		{"CriOS", uaCriOS, "Chrome", "iOS", "Chrome on iOS"},
		{"FxiOS", uaFxiOS, "Firefox", "iOS", "Firefox on iOS"},
		{"CrOS", uaCrOS, "Chrome", "ChromeOS", "Chrome on ChromeOS"},
	}
	if len(cases) != 11 {
		t.Fatalf("table has %d rows, want the 11 of the spec", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Parse(c.ua)
			if d.Browser != c.browser || d.OS != c.os {
				t.Errorf("Parse = {Browser %q, OS %q}, want {%q, %q}", d.Browser, d.OS, c.browser, c.os)
			}
			if got := d.Label(); got != c.label {
				t.Errorf("Label = %q, want %q", got, c.label)
			}
		})
	}
}

// AC 1: anything the table does not name is "Unknown device"; a half-parsed UA keeps its half.
func TestDevice_UnknownCases(t *testing.T) {
	for _, ua := range []string{"", "curl/8.4.0", "Chrome/130"} {
		d := Parse(ua)
		if got := d.Label(); got != "Unknown device" {
			t.Errorf("Parse(%q).Label() = %q, want %q", ua, got, "Unknown device")
		}
	}
	if d := Parse(""); d != (Device{}) {
		t.Errorf("Parse(\"\") = %+v, want the zero Device", d)
	}
	if d := Parse("curl/8.4.0"); d != (Device{}) {
		t.Errorf("Parse(curl) = %+v, want the zero Device", d)
	}
	if d := Parse("Chrome/130"); d.Browser != "Chrome" || d.OS != "" {
		t.Errorf("Parse(\"Chrome/130\") = %+v, want Browser Chrome, OS empty ([d-unknown-pair])", d)
	}
}
