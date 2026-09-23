package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Exact names as `go tool nm` prints them. A bare MockIssuer needle would also
// match the platform.MockIssuer /healthz variable.
const (
	symMint         = "github.com/SimonOsipov/invoice-os/internal/platform/auth.(*MockIssuer).Mint"
	symNewIssuer    = "github.com/SimonOsipov/invoice-os/internal/platform/auth.NewMockIssuer"
	symSignedString = "github.com/golang-jwt/jwt/v4.(*Token).SignedString"
	symECDSASign    = "github.com/golang-jwt/jwt/v4.(*SigningMethodECDSA).Sign"
	symVerify       = "github.com/SimonOsipov/invoice-os/internal/platform/auth.(*Verifier).Verify"
)

var mintSymbols = []string{symMint, symNewIssuer, symSignedString, symECDSASign}

// gatewayBuilds counts local builds, so a test can prove an override never falls back to one.
var gatewayBuilds atomic.Int32

// gatewayBinary returns the binary to scan: the path in envVar when that is set
// (docker-canary points it at an image's /service), else a fresh local build.
// A set-but-unusable path is an error: falling back would scan the wrong artifact.
func gatewayBinary(t *testing.T, envVar, tags string) (string, error) {
	t.Helper()
	if p, ok := os.LookupEnv(envVar); ok {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%q is set but unusable, refusing to build locally instead: %w", envVar, p, err)
		}
		t.Logf("scanning %s (from %s)", p, envVar)
		return p, nil
	}

	gatewayBuilds.Add(1)
	out := filepath.Join(t.TempDir(), "gw")
	args := []string{"build"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-o", out, ".")
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("CGO_ENABLED=0 go %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	t.Logf("scanning %s (local build, tags %q)", out, tags)
	return out, nil
}

// nmLines lists a binary's symbol table and refuses a near-empty one.
func nmLines(t *testing.T, path string) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "tool", "nm", path).CombinedOutput()
	if err != nil {
		t.Fatalf("go tool nm %s: %v\n%s", path, err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 1000 {
		t.Fatalf("go tool nm %s listed %d symbol(s); a stripped or truncated table makes every absence check vacuous", path, len(lines))
	}
	return lines
}

// hasSymbol reports whether nm's name column equals sym exactly.
func hasSymbol(lines []string, sym string) bool {
	for _, l := range lines {
		if strings.HasSuffix(l, " "+sym) {
			return true
		}
	}
	return false
}

func TestProductionGatewayBinaryCannotMint(t *testing.T) {
	// First, so a red scan below cannot hide it.
	t.Run("override_must_exist", func(t *testing.T) {
		self, err := os.Executable()
		if err != nil {
			t.Fatalf("os.Executable: %v", err)
		}
		for _, env := range []string{"GATEWAY_BINARY", "GATEWAY_BINARY_MOCKISSUER"} {
			t.Setenv(env, "/nonexistent")
			before := gatewayBuilds.Load()
			p, err := gatewayBinary(t, env, "")
			if err == nil {
				t.Errorf("%s=/nonexistent: gatewayBinary returned %q and no error", env, p)
			} else if !strings.Contains(err.Error(), env) {
				t.Errorf("%s=/nonexistent: error %q does not name the variable", env, err)
			}
			if n := gatewayBuilds.Load() - before; n != 0 {
				t.Errorf("%s=/nonexistent: gatewayBinary ran %d local build(s); the scan would pass on the wrong artifact", env, n)
			}

			// Positive pair: an existing override is returned as-is, still with no build.
			t.Setenv(env, self)
			before = gatewayBuilds.Load()
			p, err = gatewayBinary(t, env, "")
			if err != nil || p != self {
				t.Errorf("%s=%s: gatewayBinary = (%q, %v), want the override path and no error", env, self, p, err)
			}
			if n := gatewayBuilds.Load() - before; n != 0 {
				t.Errorf("%s=%s: gatewayBinary ran %d local build(s) despite the override", env, self, n)
			}
		}
	})

	bin, err := gatewayBinary(t, "GATEWAY_BINARY", "")
	if err != nil {
		t.Fatal(err)
	}
	lines := nmLines(t, bin)
	if !hasSymbol(lines, symVerify) {
		t.Fatalf("%s lacks the control symbol %s; the absence checks below prove nothing", bin, symVerify)
	}
	if len(mintSymbols) != 4 {
		t.Fatalf("mintSymbols has %d needle(s), want 4", len(mintSymbols))
	}
	for _, sym := range mintSymbols {
		for _, l := range lines {
			if strings.Contains(l, sym) {
				t.Errorf("the untagged gateway binary carries a minting symbol: %q", strings.TrimSpace(l))
				break
			}
		}
	}
}

// Control for the scan above: the same needles, spelled as the linker writes them.
func TestMockIssuerGatewayBinaryCanMint(t *testing.T) {
	bin, err := gatewayBinary(t, "GATEWAY_BINARY_MOCKISSUER", "mockissuer")
	if err != nil {
		t.Fatal(err)
	}
	lines := nmLines(t, bin)
	if len(mintSymbols) != 4 {
		t.Fatalf("mintSymbols has %d needle(s), want 4", len(mintSymbols))
	}
	for _, sym := range mintSymbols {
		if !hasSymbol(lines, sym) {
			t.Errorf("the -tags mockissuer gateway binary lacks %s", sym)
		}
	}
}
