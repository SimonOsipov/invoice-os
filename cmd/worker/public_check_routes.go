package main

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

// registerPublicCheckRoutes serves the check; the capture route exists only while the sender captures.
// The parameter is named Mux because the seam scan finds routes by that name.
func registerPublicCheckRoutes(Mux *http.ServeMux, svc *readinesscheck.Service, sender accountmail.Sender) {
	Mux.Handle("POST /internal/public-checks/run", svc.RunHandler())
	Mux.Handle("POST /internal/public-checks/mapping", svc.MappingHandler())
	if c, ok := sender.(*accountmail.Capture); ok {
		Mux.Handle("POST /internal/public-checks/captured-mail", svc.CapturedMailHandler(c))
	}
}
