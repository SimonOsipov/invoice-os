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
	const body = "Someone signed in to your ASComply account from Chrome on macOS (IP 203.0.113.7) on 29 October 2026, 14:05 WAT. " +
		"If this was you, you can ignore this email. If it was not you, reset your password now: " +
		`<a href="https://www.ascomply.com/?forgot" style="color:#f5bb86">https://www.ascomply.com/?forgot</a>.`
	if !strings.Contains(out, ">"+body+"</td>") {
		t.Errorf("intro cell is not exactly the decided body %q", body)
	}
	for _, gone := range []string{"The button opens", "no action is needed"} {
		if strings.Contains(out, gone) {
			t.Errorf("mail holds invented line %q", gone)
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

func TestRenderNewDevice_WATAroundMidnight(t *testing.T) {
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 29, 22, 59, 59, 0, time.UTC), "29 October 2026, 23:59 WAT"},
		{time.Date(2026, 10, 29, 23, 0, 0, 0, time.UTC), "30 October 2026, 00:00 WAT"},
		{time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC), "1 January 2027, 00:30 WAT"},
		{time.Date(2026, 7, 1, 12, 0, 0, 0, time.FixedZone("EST", -5*3600)), "1 July 2026, 18:00 WAT"},
	} {
		_, out := renderND(t, NewDeviceMail{Device: "x", IP: "1.2.3.4", At: c.at})
		if !strings.Contains(out, c.want) {
			t.Errorf("at %s: mail lacks %q", c.at, c.want)
		}
	}
}

func TestRenderNewDevice_UnknownIPReadsOnceAndLeavesNoEmptyIP(t *testing.T) {
	_, out := renderND(t, NewDeviceMail{Device: "Chrome on macOS", At: newDeviceAt})
	if !strings.Contains(out, "(Unknown IP)") || !strings.Contains(out, ">Unknown IP<") {
		t.Error("an empty IP does not render as Unknown IP in the sentence and the detail row")
	}
	if strings.Contains(out, "(IP )") || strings.Contains(out, "(IP Unknown") {
		t.Error("an empty IP leaves a broken IP clause")
	}
}
