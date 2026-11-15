// Command dockerguard fails when an external FROM line in a tracked Dockerfile carries no
// @sha256 digest, or when the Dockerfile directories differ from the docker entry of
// .github/dependabot.yml.
//
//	go run ./internal/tools/dockerguard
//
// Exit 0 is clean, 1 is a problem, 2 is a git, read or parse error or no Dockerfile.
package main

import "os"

func main() {
	os.Exit(run(".", os.Stdout, os.Stderr))
}
