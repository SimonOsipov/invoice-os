// Package main implements prenv, the CI helper for the GoTrue signing key and mail templates.
package main

import (
	"fmt"
	"net/http"
	"os"
)

const usage = `usage:
  prenv jwk-es256              print a fresh one-key ES256 private JWK array
  prenv jwk-check              validate a JWK array read on STDIN
                               (exit 0 = one ES256 signing key, 1 = refused)
  prenv mail-template-check <url>...
                               check each URL serves a GoTrue mail template
                               (exit 0 = all pass, 1 = any fails, 2 = bad call)
  prenv mail-subject-check <subject>
                               check a GoTrue mail subject parses and executes
                               (exit 0 = pass, 1 = fails, 2 = bad call)
  prenv mail-logo-check        check accountmail.LogoURL serves an image
                               (exit 0 = pass, 1 = fails)`

// main dispatches the subcommands. Exit 2 means a malformed call.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "jwk-es256":
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(RunJWKES256(os.Stdout))

	case "jwk-check":
		// STDIN, not argv: the input is a private key.
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(RunJWKCheck(os.Stdin, os.Stdout))

	case "mail-template-check":
		os.Exit(RunMailTemplateCheck(&http.Client{}, os.Args[2:], os.Stdout))

	case "mail-subject-check":
		os.Exit(RunMailSubjectCheck(os.Args[2:], os.Stdout))

	case "mail-logo-check":
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(runMailLogoCheckDefault(&http.Client{}, os.Stdout))

	default:
		fmt.Fprintf(os.Stderr, "prenv: unknown subcommand %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}
