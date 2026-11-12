package gateway

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

func TestReadinessCheck_WorkerFormatWordIsTheGatewaySentence(t *testing.T) {
	if checkFormatSentence != readinesscheck.FormatRefusal {
		t.Fatalf("checkFormatSentence = %q, readinesscheck.FormatRefusal = %q", checkFormatSentence, readinesscheck.FormatRefusal)
	}
}

// The gateway's wait for the press must outlast the worker's, or a slow send shows the retry page after it committed.
func TestReadinessConfirmTimeout_OutlastsTheWorkersConfirm(t *testing.T) {
	if readinessConfirmTimeout <= readinesscheck.ConfirmTimeout {
		t.Fatalf("gateway press timeout %v is not past the worker's %v", readinessConfirmTimeout, readinesscheck.ConfirmTimeout)
	}
}
