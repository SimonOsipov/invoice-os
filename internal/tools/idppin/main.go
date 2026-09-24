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
	case "tag":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		fmt.Println(readPin(os.Args[2]))
	case "latest-check":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		pinned, latest := readPin(os.Args[2]), os.Args[3]
		if pinned != latest {
			fmt.Printf("idppin: pinned %s, latest release %s\n", pinned, latest)
			os.Exit(1)
		}
		fmt.Printf("idppin: pinned %s is the latest release\n", pinned)
	default:
		fmt.Fprintf(os.Stderr, "idppin: unknown subcommand %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}

func readPin(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "idppin: %v\n", err)
		os.Exit(2)
	}
	tag, err := ParsePin(string(raw))
	if err != nil {
		fmt.Fprintf(os.Stderr, "idppin: %s: %v\n", path, err)
		os.Exit(2)
	}
	return tag
}
