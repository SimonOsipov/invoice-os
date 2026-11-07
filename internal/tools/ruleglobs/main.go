// Command ruleglobs fails when a glob in a .claude/rules front matter matches no tracked file.
//
//	go run ./internal/tools/ruleglobs
//
// Exit 0 is clean, 1 is a dead glob or a malformed paths list, 2 is a git or read error or no rule file.
package main

import "os"

func main() {
	os.Exit(run(".", os.Stdout, os.Stderr))
}
