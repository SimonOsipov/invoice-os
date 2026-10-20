// Command advisorygate fails CI on a high or critical advisory found by osv-scanner.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	osv := flag.String("osv", "", "path to the osv-scanner binary (required)")
	root := flag.String("root", ".", "repo root to scan")
	api := flag.String("osv-api", "https://api.osv.dev", "base URL of the OSV API")
	flag.Parse()
	if *osv == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: advisorygate -osv <path> [-root <dir>] [-osv-api <url>]")
		os.Exit(2)
	}
	os.Exit(Run(*osv, *root, *api, time.Now().UTC(), os.Stdout, os.Stderr))
}
