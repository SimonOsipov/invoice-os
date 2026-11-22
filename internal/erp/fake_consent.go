package erp

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/erp/erpweb"
)

// FakeConsentPage serves the fake vendor's consent form over every fake code.
func FakeConsentPage() http.Handler { return erpweb.FakeConsentPage(fakeCodes()) }
