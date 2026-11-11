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
