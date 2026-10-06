package main

// RED STUB (compile-only). The executor deletes this file and implements
// inviteSender in main.go.

import (
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

func inviteSender(getenv func(string) string, rt http.RoundTripper, logger *slog.Logger) accountmail.Sender {
	return accountmail.Off{}
}
