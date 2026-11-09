package gateway_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
)

func TestReadinessCheck_FileCapIsTheEngineCap(t *testing.T) {
	if gateway.MaxCheckFileBytes != publiccheck.MaxFileBytes {
		t.Fatalf("gateway.MaxCheckFileBytes = %d, publiccheck.MaxFileBytes = %d", gateway.MaxCheckFileBytes, publiccheck.MaxFileBytes)
	}
}
