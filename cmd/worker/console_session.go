package main

import (
	"github.com/SimonOsipov/invoice-os/internal/consolesession"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// mountConsoleSession is a compile stub for the red tests (CSSO-01-03); the executor replaces it.
// It reads IAP_AUDIENCES from getenv and mounts the recovery-token route on app.Mux when set.
func mountConsoleSession(app *platform.App, getenv func(string) string, admin consolesession.RecoveryLinker, iap auth.IAPConfig) error {
	return nil
}
