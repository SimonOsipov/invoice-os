package main

import "os"

// main reads the `go test -json` event stream from stdin, renders it, and
// exits with evaluate's verdict code (see rlsgate.go).
func main() {
	os.Exit(evaluate(os.Stdin, os.Stdout))
}
