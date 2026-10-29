// Command gotestretry runs `go test -json` into a file, then re-runs each failed package once.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, goRunner))
}

// goRunner runs `go args...`, streaming its stdout to out. Exit 1 when go cannot start.
func goRunner(args []string, out io.Writer) int {
	cmd := exec.Command("go", args...)
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "gotestretry: %v\n", err)
	return 1
}
