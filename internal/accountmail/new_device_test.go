package accountmail

import (
	"strings"
	"testing"
	"time"
)

var newDeviceAt = time.Date(2026, time.October, 29, 13, 5, 0, 0, time.UTC)

func renderND(t *testing.T, m NewDeviceMail) (subject, out string) {
	t.Helper()
	subject, out, err := RenderNewDevice(m)
	if err != nil {
		t.Fatalf("RenderNewDevice(%+v): %v", m, err)
	}
	return subject, out
}

func TestNewDeviceTemplate_ComposesTheLayout(t *testing.T) { checkComposes(t, "new_device") }

func TestRenderNewDevice_Copy(t *testing.T) {
	subject, out := renderND(t, NewDeviceMail{Device: "Chrome on macOS", IP: "203.0.113.7", At: newDeviceAt})
	if subject != "New sign-in to your ASComply account" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{
		"Chrome on macOS", "(IP 203.0.113.7)", "29 October 2026, 14:05 WAT",
		"If this was you, you can ignore this email.", "If it was not you, reset your password now:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mail does not hold %q", want)
		}
	}
	var found bool
	for _, a := range anchors(out) {
		if a.href == ForgotPasswordURL {
			found = true
		}
	}
	if !found || ForgotPasswordURL != "https://www.ascomply.com/?forgot" {
		t.Errorf("no anchor with href %s", ForgotPasswordURL)
	}
	checkLayoutRules(t, out)
}

func TestRenderNewDevice_UnknownValues(t *testing.T) {
	_, out := renderND(t, NewDeviceMail{Device: "Unknown device", At: newDeviceAt})
	for _, want := range []string{"Unknown device", "(Unknown IP)"} {
		if !strings.Contains(out, want) {
			t.Errorf("mail does not hold %q", want)
		}
	}
	_, out = renderND(t, NewDeviceMail{At: newDeviceAt})
	if !strings.Contains(out, "Unknown device") {
		t.Error("an empty device does not render \"Unknown device\"")
	}
}

func TestRenderNewDevice_EscapesText(t *testing.T) {
	_, out := renderND(t, NewDeviceMail{Device: "<b>x</b> on y", IP: "1.2.3.4", At: newDeviceAt})
	if !strings.Contains(out, "&lt;b&gt;x&lt;/b&gt; on y") || strings.Contains(out, "<b>x") {
		t.Error("device text is not escaped")
	}
}

func TestRenderNewDevice_ZeroTimeFails(t *testing.T) {
	if _, _, err := RenderNewDevice(NewDeviceMail{Device: "x"}); err == nil {
		t.Error("a zero time rendered")
	}
}
