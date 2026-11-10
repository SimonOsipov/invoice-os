package main

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

// The parameter is named Mux because the seam scan finds routes by that name.
func registerPublicCheckRoutes(Mux *http.ServeMux, svc *readinesscheck.Service) {
	Mux.Handle("POST /internal/public-checks/run", svc.RunHandler())
	Mux.Handle("POST /internal/public-checks/mapping", svc.MappingHandler())
}
