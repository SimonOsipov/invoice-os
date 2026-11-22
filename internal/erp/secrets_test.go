package erp

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMemorySecrets_Contract(t *testing.T) {
	s := NewMemorySecrets()
	one, two := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(one)
	_, _ = rand.Read(two)

	if _, _, err := s.Access(ctx, "a"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("access of a missing secret: %v", err)
	}
	if err := s.Create(ctx, "a", one); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "a", two); err == nil {
		t.Error("a second Create of one id succeeded")
	}
	got, v1, err := s.Access(ctx, "a")
	if err != nil || !bytes.Equal(got, one) || v1 != "1" {
		t.Fatalf("access = %x, %q, %v", got, v1, err)
	}
	got[0] ^= 0xff
	if again, _, _ := s.Access(ctx, "a"); !bytes.Equal(again, one) {
		t.Error("the caller changed the stored payload")
	}
	v2, err := s.AddVersion(ctx, "a", two)
	if err != nil || v2 != "2" {
		t.Fatalf("AddVersion = %q, %v", v2, err)
	}
	if got, v, _ := s.Access(ctx, "a"); !bytes.Equal(got, two) || v != "2" {
		t.Errorf("latest = %x, %q", got, v)
	}
	if err := s.DisableVersion(ctx, "a", v1); err != nil {
		t.Error(err)
	}
	if got, v, err := s.Access(ctx, "a"); err != nil || !bytes.Equal(got, two) || v != "2" {
		t.Errorf("latest after disabling the old version = %x, %q, %v", got, v, err)
	}
	if err := s.DisableVersion(ctx, "a", v2); err != nil {
		t.Error(err)
	}
	if _, _, err := s.Access(ctx, "a"); err == nil || errors.Is(err, ErrSecretNotFound) {
		t.Errorf("access of a disabled latest: %v", err)
	}
	if err := s.DisableVersion(ctx, "a", "9"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("disable of a missing version: %v", err)
	}
	if _, err := s.AddVersion(ctx, "nope", one); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("AddVersion to a missing secret: %v", err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Error(err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Errorf("delete of a missing secret: %v", err)
	}
	if _, _, err := s.Access(ctx, "a"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("access after delete: %v", err)
	}
}

func TestSecretName_Scope(t *testing.T) {
	g := uuid.NewString()
	for scope, want := range map[string]string{"": "erp-grant-" + g, "slot-1": "erp-grant-slot-1-" + g, "nightly": "erp-grant-nightly-" + g} {
		if got := SecretName(scope, g); got != want {
			t.Errorf("SecretName(%q) = %q, want %q", scope, got, want)
		}
	}
}

func fakeReg(t *testing.T) Registry {
	t.Helper()
	r, err := NewRegistry(NewFake(testRedirect))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// stubGCP replaces the boot seams; metadata answers by suffix, or fails when fail is set.
func stubGCP(t *testing.T, fail bool) {
	t.Helper()
	oldMeta, oldClient := metadataGet, newSMClient
	t.Cleanup(func() { metadataGet, newSMClient = oldMeta, oldClient })
	metadataGet = func(_ context.Context, suffix string) (string, error) {
		if fail {
			return "", errors.New("no metadata")
		}
		return map[string]string{"project/project-id": "proj-x", "instance/region": "projects/1/regions/africa-south1"}[suffix], nil
	}
	newSMClient = func(context.Context) (*smClient, error) { return lazyClient(t), nil }
}

func TestSecretsFromEnv_ByPostureAndRegistry(t *testing.T) {
	empty, _ := NewRegistry()
	for _, d := range []string{"", "pr-3", "nightly", "production"} {
		if s, err := SecretsFromEnv(ctx, env(map[string]string{"DEPLOY_ENV": d}), empty); s != nil || err != nil {
			t.Errorf("empty registry on %q: %v, %v", d, s, err)
		}
	}
	stubGCP(t, false)
	if s, err := SecretsFromEnv(ctx, env(nil), fakeReg(t)); err != nil {
		t.Errorf("local: %v", err)
	} else if _, ok := s.(*MemorySecrets); !ok {
		t.Errorf("local store is %T", s)
	}
	s, err := SecretsFromEnv(ctx, env(map[string]string{"DEPLOY_ENV": "pr-3", "ERP_SECRET_SCOPE": "slot-1"}), fakeReg(t))
	if g, ok := s.(*gcpSecrets); err != nil || !ok || g.project != "proj-x" || g.region != "africa-south1" {
		t.Errorf("preview: %#v, %v", s, err)
	}
	stubGCP(t, true)
	if _, err := SecretsFromEnv(ctx, env(map[string]string{"DEPLOY_ENV": "pr-3", "ERP_SECRET_SCOPE": "slot-1"}), fakeReg(t)); err == nil {
		t.Error("a failed metadata read booted")
	}
}

func TestSecretsFromEnv_Scope(t *testing.T) {
	stubGCP(t, false)
	for _, c := range []struct {
		name         string
		deploy, scop string
		ok           bool
	}{
		{"preview ok", "pr-3", "slot-1", true},
		{"preview unset", "pr-3", "", false},
		{"preview unknown slot", "pr-3", "slot-9", false},
		{"preview wrong word", "pr-3", "prod", false},
		{"nightly ok", "nightly", "nightly", true},
		{"nightly unset", "nightly", "", false},
		{"hosted with scope", "production", "slot-1", false},
		{"hosted without", "production", "", true},
	} {
		_, err := SecretsFromEnv(ctx, env(map[string]string{"DEPLOY_ENV": c.deploy, "ERP_SECRET_SCOPE": c.scop}), fakeReg(t))
		if c.ok != (err == nil) {
			t.Errorf("%s: %v", c.name, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "ERP_SECRET_SCOPE") || (c.scop != "" && strings.Contains(err.Error(), c.scop))) {
			t.Errorf("%s: error %q must name the variable and not the value", c.name, err)
		}
	}
}
