//go:build !mockissuer

package main

import (
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// The production build serves no mint route even when the flag is on and the environment is not production.
func TestUntaggedGatewayServesNoMintRoutes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwks, login := mockIssuerRoutes("development", "true", func(h http.Handler) http.Handler { return h }, logger)
	if jwks != nil || login != nil {
		t.Errorf(`mockIssuerRoutes("development", "true") = (jwks %v, login %v), want (nil, nil) in a build without -tags mockissuer`, jwks, login)
	}
}

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
		"TestTaggedMockStaffRouteWiresTheGrant",
		"TestTaggedMockMemberRouteWiresTheGrant",
		"TestTaggedMockLoginIgnoresRailwayEnvironmentName",
	} {
		if !strings.Contains(string(out), "--- PASS: "+name+" (") {
			t.Errorf("go test -tags mockissuer: %s did not pass", name)
		}
	}
	if err != nil {
		t.Fatalf("go test -tags mockissuer -run ^TestTagged: %v\n%s", err, out)
	}
}
