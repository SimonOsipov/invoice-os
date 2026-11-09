package main

import (
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

// registerPublicCheckRoutes is a red-test stub: it registers nothing.
func registerPublicCheckRoutes(_ *http.ServeMux, _ *readinesscheck.Service, _ accountmail.Sender) {}
