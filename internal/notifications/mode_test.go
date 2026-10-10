package notifications

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

var modeKeyVars = []string{"HUBSPOT_TOKEN", "RESEND_API_KEY", "RESEND_SEGMENT_ID", "RESEND_TOPIC_ID"}

func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// keyEnv sets the four key variables to distinct secrets, then applies extra.
func keyEnv(extra map[string]string) map[string]string {
	m := map[string]string{}
	for _, k := range modeKeyVars {
		m[k] = "sekret-" + strings.ToLower(k)
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func wantKeys() Keys {
	return Keys{
		HubSpotToken:    "sekret-hubspot_token",
		ResendAPIKey:    "sekret-resend_api_key",
		ResendSegmentID: "sekret-resend_segment_id",
		ResendTopicID:   "sekret-resend_topic_id",
	}
}

func assertNoSecrets(t *testing.T, err error, env map[string]string) {
	t.Helper()
	for k, v := range env {
		if strings.HasPrefix(v, "sekret-") && strings.Contains(err.Error(), v) {
			t.Errorf("error %q carries the value of %s", err, k)
		}
	}
}

func TestModeFromEnv_PreviewIsAlwaysFake(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"four keys", keyEnv(nil)},
		{"four keys, fake flag false", keyEnv(map[string]string{"CONTACTS_FAKE": "false"})},
		{"partial keys", map[string]string{"HUBSPOT_TOKEN": "sekret-hubspot_token"}},
		{"no keys", nil},
		// Each of these refuses outside preview.
		{"fake flag and one key", map[string]string{"CONTACTS_FAKE": "true", "HUBSPOT_TOKEN": "sekret-hubspot_token"}},
		{"fake flag and four keys", keyEnv(map[string]string{"CONTACTS_FAKE": "true"})},
		{"fake flag alone", map[string]string{"CONTACTS_FAKE": "true"}},
		{"bad fake flag", map[string]string{"CONTACTS_FAKE": "yes!"}},
		{"bad fake flag and four keys", keyEnv(map[string]string{"CONTACTS_FAKE": "yes!"})},
		{"padded fake flag", map[string]string{"CONTACTS_FAKE": " true"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, keys, err := ModeFromEnv(getenvFrom(c.env), platform.PosturePreview)
			if err != nil {
				t.Fatalf("ModeFromEnv() err = %v, want nil", err)
			}
			if mode != ModeFake {
				t.Errorf("mode = %q, want %q", mode, ModeFake)
			}
			if keys != (Keys{}) {
				t.Errorf("keys = %+v, want empty (preview discards them)", keys)
			}
		})
	}

	// The same four keys outside preview are real: posture is the only difference.
	mode, keys, err := ModeFromEnv(getenvFrom(keyEnv(nil)), platform.PostureHosted)
	if err != nil || mode != ModeReal || keys != wantKeys() {
		t.Errorf("hosted with four keys = (%q, %+v, %v), want real with the keys", mode, keys, err)
	}
}

func TestModeFromEnv_Table(t *testing.T) {
	padded := map[string]string{
		"HUBSPOT_TOKEN":     " tok ",
		"RESEND_API_KEY":    "key\n",
		"RESEND_SEGMENT_ID": "\tseg",
		"RESEND_TOPIC_ID":   "top ",
	}
	rows := []struct {
		name     string
		posture  platform.PostureKind
		env      map[string]string
		wantMode Mode
		wantKeys Keys
	}{
		{"fake flag, local", platform.PostureLocal, map[string]string{"CONTACTS_FAKE": "true"}, ModeFake, Keys{}},
		{"fake flag, hosted", platform.PostureHosted, map[string]string{"CONTACTS_FAKE": "true"}, ModeFake, Keys{}},
		{"fake flag 1", platform.PostureHosted, map[string]string{"CONTACTS_FAKE": "1"}, ModeFake, Keys{}},
		{"four keys, hosted", platform.PostureHosted, keyEnv(nil), ModeReal, wantKeys()},
		{"four keys, local", platform.PostureLocal, keyEnv(nil), ModeReal, wantKeys()},
		{"four keys, fake flag false", platform.PostureHosted, keyEnv(map[string]string{"CONTACTS_FAKE": "false"}), ModeReal, wantKeys()},
		{"four keys, fake flag empty", platform.PostureHosted, keyEnv(map[string]string{"CONTACTS_FAKE": ""}), ModeReal, wantKeys()},
		{"four keys keep their padding", platform.PostureHosted, padded, ModeReal, Keys{
			HubSpotToken: " tok ", ResendAPIKey: "key\n", ResendSegmentID: "\tseg", ResendTopicID: "top ",
		}},
		{"no keys, hosted", platform.PostureHosted, nil, ModeOff, Keys{}},
		{"no keys, local", platform.PostureLocal, nil, ModeOff, Keys{}},
		{"no keys, empty strings", platform.PostureHosted, map[string]string{"HUBSPOT_TOKEN": "", "RESEND_API_KEY": ""}, ModeOff, Keys{}},
		{"no keys, fake flag false", platform.PostureHosted, map[string]string{"CONTACTS_FAKE": "false"}, ModeOff, Keys{}},
		{"no keys, fake flag 0", platform.PostureLocal, map[string]string{"CONTACTS_FAKE": "0"}, ModeOff, Keys{}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			mode, keys, err := ModeFromEnv(getenvFrom(r.env), r.posture)
			if err != nil {
				t.Fatalf("ModeFromEnv() err = %v, want nil", err)
			}
			if mode != r.wantMode {
				t.Errorf("mode = %q, want %q", mode, r.wantMode)
			}
			if keys != r.wantKeys {
				t.Errorf("keys = %+v, want %+v", keys, r.wantKeys)
			}
		})
	}
}

// refuses calls ModeFromEnv and requires a refusal to carry no mode and no keys.
func refuses(t *testing.T, getenv func(string) string, posture platform.PostureKind) error {
	t.Helper()
	mode, keys, err := ModeFromEnv(getenv, posture)
	if err != nil && (mode != "" || keys != (Keys{})) {
		t.Errorf("refusal returned mode %q and keys %+v, want neither", mode, keys)
	}
	return err
}

func TestModeFromEnv_FakeWithAKeyRefuses(t *testing.T) {
	for _, posture := range []platform.PostureKind{platform.PostureLocal, platform.PostureHosted} {
		for _, key := range modeKeyVars {
			t.Run(string(posture)+"/"+key, func(t *testing.T) {
				env := map[string]string{"CONTACTS_FAKE": "true", key: "sekret-" + strings.ToLower(key)}
				err := refuses(t, getenvFrom(env), posture)
				if err == nil {
					t.Fatal("ModeFromEnv() err = nil, want a refusal")
				}
				if !strings.Contains(err.Error(), key) {
					t.Errorf("err = %q, want it to name %s", err, key)
				}
				assertNoSecrets(t, err, env)
			})
		}
	}

	t.Run("all four keys", func(t *testing.T) {
		env := keyEnv(map[string]string{"CONTACTS_FAKE": "true"})
		err := refuses(t, getenvFrom(env), platform.PostureHosted)
		if err == nil {
			t.Fatal("ModeFromEnv() err = nil, want a refusal")
		}
		assertNoSecrets(t, err, env)
	})

	t.Run("a whitespace key counts as set", func(t *testing.T) {
		env := map[string]string{"CONTACTS_FAKE": "true", "RESEND_API_KEY": " "}
		err := refuses(t, getenvFrom(env), platform.PostureHosted)
		if err == nil || !strings.Contains(err.Error(), "RESEND_API_KEY") {
			t.Errorf("err = %v, want a refusal naming RESEND_API_KEY", err)
		}
	})
}

func TestModeFromEnv_PartialKeysRefuse(t *testing.T) {
	for _, missing := range modeKeyVars {
		t.Run("missing "+missing, func(t *testing.T) {
			env := keyEnv(nil)
			delete(env, missing)
			err := refuses(t, getenvFrom(env), platform.PostureHosted)
			if err == nil {
				t.Fatal("ModeFromEnv() err = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("err = %q, want it to name %s", err, missing)
			}
			assertNoSecrets(t, err, env)
		})
	}

	t.Run("one key names the other three", func(t *testing.T) {
		env := map[string]string{"RESEND_TOPIC_ID": "sekret-resend_topic_id"}
		err := refuses(t, getenvFrom(env), platform.PostureLocal)
		if err == nil {
			t.Fatal("ModeFromEnv() err = nil, want a refusal")
		}
		for _, name := range []string{"HUBSPOT_TOKEN", "RESEND_API_KEY", "RESEND_SEGMENT_ID"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("err = %q, want it to name %s", err, name)
			}
		}
		assertNoSecrets(t, err, env)
	})

	// Values are not trimmed: a whitespace value is set, so one of them is partial, not off.
	t.Run("a whitespace key is not off", func(t *testing.T) {
		err := refuses(t, getenvFrom(map[string]string{"HUBSPOT_TOKEN": " "}), platform.PostureHosted)
		if err == nil {
			t.Error("ModeFromEnv() err = nil, want a refusal for one whitespace key")
		}
	})
}

func TestModeFromEnv_BadFakeFlagRefuses(t *testing.T) {
	for _, posture := range []platform.PostureKind{platform.PostureLocal, platform.PostureHosted} {
		for _, bad := range []string{"yes!", "zz-nope", "tr ue"} {
			t.Run(string(posture)+"/"+bad, func(t *testing.T) {
				err := refuses(t, getenvFrom(map[string]string{"CONTACTS_FAKE": bad}), posture)
				if err == nil {
					t.Fatal("ModeFromEnv() err = nil, want a refusal")
				}
				if !strings.Contains(err.Error(), "CONTACTS_FAKE") {
					t.Errorf("err = %q, want it to name CONTACTS_FAKE", err)
				}
				if strings.Contains(err.Error(), bad) {
					t.Errorf("err = %q carries the value %q", err, bad)
				}
			})
		}
	}

	// Not trimmed: a padded "true" is unparseable, never fake.
	for _, padded := range []string{" true", "true ", "true\n"} {
		t.Run("padded "+strings.TrimSpace(padded), func(t *testing.T) {
			err := refuses(t, getenvFrom(map[string]string{"CONTACTS_FAKE": padded}), platform.PostureHosted)
			if err == nil {
				t.Errorf("CONTACTS_FAKE=%q: err = nil, want a refusal", padded)
			}
		})
	}

	t.Run("a bad flag beats four keys", func(t *testing.T) {
		err := refuses(t, getenvFrom(keyEnv(map[string]string{"CONTACTS_FAKE": "yes!"})), platform.PostureHosted)
		if err == nil {
			t.Error("ModeFromEnv() err = nil, want a refusal, never real")
		}
	})
}

// failTransport fails the test when any request goes through it.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("fake made a network call: %s %s", r.Method, r.URL)
	return nil, http.ErrUseLastResponse
}

func TestFakes_MakeNoNetworkCallAndNeverFail(t *testing.T) {
	prev := http.DefaultTransport
	http.DefaultTransport = failTransport{t}
	t.Cleanup(func() { http.DefaultTransport = prev })

	var hs HubSpotClient = FakeHubSpot{}
	var rs ResendClient = FakeResend{}
	for _, c := range []Contact{
		fullContact("ada@corp.example"),
		{Email: "bare@corp.example"},
	} {
		if err := hs.Upsert(t.Context(), c); err != nil {
			t.Errorf("FakeHubSpot.Upsert(%q) err = %v, want nil", c.Email, err)
		}
		if err := hs.OpenDemoDeal(t.Context(), c, "Acme — demo request"); err != nil {
			t.Errorf("FakeHubSpot.OpenDemoDeal(%q) err = %v, want nil", c.Email, err)
		}
		for _, optIn := range []bool{false, true} {
			if err := rs.Sync(t.Context(), c, optIn); err != nil {
				t.Errorf("FakeResend.Sync(%q, %v) err = %v, want nil", c.Email, optIn, err)
			}
		}
	}

	// The real clients satisfy the same interfaces the worker takes.
	var _ HubSpotClient = (*HubSpot)(nil)
	var _ ResendClient = (*Resend)(nil)
}
