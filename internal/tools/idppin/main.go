// Command idppin reads the pinned supabase/auth tag from sidecar/auth/Dockerfile,
// the one file that holds the tag and digest.
package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  idppin tag <dockerfile>                      print the pinned tag
  idppin latest-check <dockerfile> <latest-tag>
                                               exit 0 if the pin equals latest-tag, 1 if not`

// Exit 2 means a malformed call or an unreadable pin; 1 is reserved for a mismatch.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "tag", "latest-check":
		fmt.Fprintf(os.Stderr, "idppin: %s: not implemented\n", os.Args[1])
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "idppin: unknown subcommand %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}
