//go:build !mockissuer

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The untagged suite also runs the -tags mockissuer tests, so `go test ./...` and
// mutationreplay (which passes no tags) both see a break in mockissuer.go.
func TestMockIssuerBuildPassesItsTaggedTests(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "test", "-count=1", "-tags", "mockissuer", "-v", "-run", "^TestTagged", ".").CombinedOutput()
	if strings.Contains(string(out), "[build failed]") || strings.Contains(string(out), "[setup failed]") {
		t.Fatalf("the -tags mockissuer package does not compile:\n%s", out)
	}
	for _, name := range []string{
		"TestTaggedGatewayRegistersMintRoutesOutsideProduction",
		"TestTaggedMockIssuerRoutesValueDomain",
		"TestTaggedMockIssuerRoutesKeepTheirWiring",
	} {
		if !strings.Contains(string(out), "--- PASS: "+name+" (") {
			t.Errorf("go test -tags mockissuer: %s did not pass", name)
		}
	}
	if err != nil {
		t.Fatalf("go test -tags mockissuer -run ^TestTagged: %v\n%s", err, out)
	}
}
