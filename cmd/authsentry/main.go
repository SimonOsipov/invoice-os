// Command authsentry runs GoTrue and forwards its server-side failures to Sentry.
package main

import (
	"io"
	"log/slog"
	"os"
	"os/exec"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func main() {
	defer platform.ReportBootPanic()
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run returns the exit code of GoTrue.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return 0
}

// startGoTrue starts the auth binary with its stderr piped to the caller.
func startGoTrue(args []string) (*exec.Cmd, io.ReadCloser) {
	cmd := exec.Command("auth", args...)
	pipe, err := cmd.StderrPipe()
	if err != nil {
		platform.Fatal(slog.Default(), "authsentry: stderr pipe: %v", err)
	}
	return cmd, pipe
}
