package purgeargs

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPurgeArgs_KindKeysAndOneAttempt(t *testing.T) {
	a := Args{TenantID: uuid.NewString()}
	if got := a.Kind(); got != "workspace_purge" {
		t.Errorf("Kind() = %q, want workspace_purge", got)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if len(m) != 1 || m["tenant_id"] != a.TenantID {
		t.Errorf("args JSON = %s, want exactly the key tenant_id holding the id", raw)
	}
	if got := a.InsertOpts().MaxAttempts; got != 1 {
		t.Errorf("MaxAttempts = %d, want 1", got)
	}
}

// Tenancy imports this package for the kind; an internal/document import would link the S3 SDK into it (P11).
func TestPurgeArgs_ImportsNoDocumentPackage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if !slices.Contains(deps, "github.com/riverqueue/river") {
		t.Fatalf("deps = %v, want the River import the package exists for", deps)
	}
	for _, d := range deps {
		if strings.Contains(d, "invoice-os/internal/document") {
			t.Errorf("purgeargs depends on %s", d)
		}
	}
}
