package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The public gateway must not link the Secret Manager client or the ERP store: the edge holds no secret
// rights (the grant secrets are the worker's alone). Only a dependency listing can observe what a binary links.
func TestGatewayBinaryLinksNeitherSecretManagerNorTheERPStore(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	has := func(pkg string) bool {
		for _, d := range deps {
			if d == pkg {
				return true
			}
		}
		return false
	}
	// Controls: the listing is real, and the leaf package the gateway is allowed to use is in it.
	if len(deps) < 100 || !has("github.com/SimonOsipov/invoice-os/internal/erp/erpweb") {
		t.Fatalf("the dependency listing looks wrong: %d packages, erpweb present = %v", len(deps), has("github.com/SimonOsipov/invoice-os/internal/erp/erpweb"))
	}
	for _, d := range deps {
		if d == "github.com/SimonOsipov/invoice-os/internal/erp" || strings.HasPrefix(d, "cloud.google.com/go/secretmanager") {
			t.Errorf("the gateway links %s", d)
		}
	}
}
