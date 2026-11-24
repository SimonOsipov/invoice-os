package erp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// SecretPrefix is the one spelling of the secret-id prefix; the IAM conditions and preview.sh match it.
const SecretPrefix = "erp-grant-"

// envSecretScope names the slot on a preview or the nightly; error text names it, never its value.
const envSecretScope = "ERP_SECRET_SCOPE"

// SecretScopeFromEnv is the slot a preview or the nightly names its secrets after; empty elsewhere.
func SecretScopeFromEnv(getenv func(string) string) string { return getenv(envSecretScope) }

// SecretName is the secret id of a grant. scope is empty in production and locally, and the slot
// (slot-1, slot-2, nightly) on a preview or the nightly, so a slot's identity reads only its own.
func SecretName(scope, grantID string) string {
	if scope == "" {
		return SecretPrefix + grantID
	}
	return SecretPrefix + scope + "-" + grantID
}

// ErrSecretNotFound is the answer for a secret or version that does not exist.
var ErrSecretNotFound = errors.New("erp: secret not found")

// SecretStore keeps connector grants; Postgres holds only the secret id. No error carries a payload.
type SecretStore interface {
	// Create adds the secret and its first version; it deletes the secret when the version add fails.
	Create(ctx context.Context, id string, payload []byte) error
	// Access reads the version aliased "latest".
	Access(ctx context.Context, id string) (payload []byte, version string, err error)
	AddVersion(ctx context.Context, id string, payload []byte) (version string, err error)
	DisableVersion(ctx context.Context, id, version string) error
	// Delete treats a missing secret as deleted.
	Delete(ctx context.Context, id string) error
}

type memVersion struct {
	payload  []byte
	disabled bool
}

// MemorySecrets is the local store and the test double.
type MemorySecrets struct {
	mu sync.Mutex
	m  map[string][]memVersion
}

func NewMemorySecrets() *MemorySecrets { return &MemorySecrets{m: map[string][]memVersion{}} }

func (s *MemorySecrets) Create(_ context.Context, id string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; ok {
		return errors.New("erp: secret already exists")
	}
	s.m[id] = []memVersion{{payload: slices.Clone(payload)}}
	return nil
}

func (s *MemorySecrets) Access(_ context.Context, id string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := s.m[id]
	if len(vs) == 0 {
		return nil, "", ErrSecretNotFound
	}
	v := vs[len(vs)-1]
	if v.disabled {
		return nil, "", errors.New("erp: latest secret version is disabled")
	}
	return slices.Clone(v.payload), fmt.Sprint(len(vs)), nil
}

func (s *MemorySecrets) AddVersion(_ context.Context, id string, payload []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs, ok := s.m[id]
	if !ok {
		return "", ErrSecretNotFound
	}
	s.m[id] = append(vs, memVersion{payload: slices.Clone(payload)})
	return fmt.Sprint(len(vs) + 1), nil
}

func (s *MemorySecrets) DisableVersion(_ context.Context, id, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.m[id] {
		if fmt.Sprint(i+1) == version {
			s.m[id][i].disabled = true
			s.m[id][i].payload = nil
			return nil
		}
	}
	return ErrSecretNotFound
}

func (s *MemorySecrets) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

// SecretsFromEnv: no connector, no store (production today); local runs in memory; a preview, the
// nightly and a hosted deployment use Secret Manager, and a failed boot read is a boot error.
func SecretsFromEnv(ctx context.Context, getenv func(string) string, reg Registry) (SecretStore, error) {
	if len(reg) == 0 {
		return nil, nil
	}
	env := platform.DeployEnvFrom(getenv)
	posture := platform.Posture(env)
	if posture == platform.PostureLocal {
		return NewMemorySecrets(), nil
	}
	scope := getenv(envSecretScope)
	switch {
	case posture == platform.PostureHosted && scope != "":
		return nil, errors.New("erp: " + envSecretScope + " must not be set on a hosted deployment")
	case posture.Ephemeral() && !slices.Contains([]string{"slot-1", "slot-2", "nightly"}, scope):
		return nil, errors.New("erp: " + envSecretScope + " must be slot-1, slot-2 or nightly")
	}
	return newGCPSecretsFromMetadata(ctx, previewLabels(env, posture))
}
